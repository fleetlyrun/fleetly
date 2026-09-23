package database

// 引擎适配器（managed-databases §2.6 EngineAdapter 的 S5 落地）：每引擎的
// 备份/回读校验/原地恢复命令词表 + 一次性 job 载荷拼装。执行体 = 平台
// dbtools 镜像的一次性 Swarm job（D-DB-6「执行体」行：replicated-job 钉
// 绑定节点、库凭据与 restic 目标经 env 注入——本文件拼装载荷并兑现接口；
// 编排/台账/事件在 backup.go/restore.go/upgrade.go）。
//
// 明文纪律（负面测试钉死）：密码只进 job env（PGPASSWORD / REDISCLI_AUTH /
// RESTIC_PASSWORD / AWS_*）——命令词表零密码；PG 恢复走 unix socket trust
//（官方镜像 pg_hba 对 local 全 trust）故恢复命令零凭据；restic 输出只含
// 路径与字节量。凭据明文字段（dbtemplate.BackupInput 等）只存活于内存链。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DefaultDatabaseToolsImage 是库备份/恢复/校验一次性 job 的平台镜像（E4
// D-DB-6：引擎工具 + restic，digest 钉定随平台 release——zot 同款双锚纪
// 律：tag 保留可读性、digest 为准，多架构 index 摘要 amd64/arm64 通吃）。
// S2 v0.2.x 重发为 debian/glibc 基底（deploy/Dockerfile.dbtools：基底 =
// postgres:16 与 dbtemplate.DefaultPostgresImage 同一钉定 digest，redis-cli
// 取自 debian 版 redis:7，restic 静态二进制照旧）——恢复单 job 的前提
//（本引擎与 dbtools 跨 libc 的 musl/glibc 重放风险随基底一致而消除）。
//
// 供应链：CI 首推 2026-09-23（run 35797985743），digest 已钉（多架构 index，
// buildx imagetools 独立解析）——中间态豁免已摘除，与平台其余镜像同构。
// 工具面：pg_dump 16.15/pg_restore/psql/pg_isready/pg_ctl/gosu（postgres
// 基底自带）、redis-cli、restic 0.19.1。重建随平台 release 由
// .github/workflows/dbtools.yml 承载。
const DefaultDatabaseToolsImage = "ghcr.io/fleetlyrun/dbtools:v0.2.1-dbtools.1@sha256:472e8a5dd6b7ab2722caa996f18fec956203f99aafec0dd4c10cb82d262e866b"

// 备份计划平台缺省（§5.4 配置键 databases.backup_*；实例 settings 零值
// 字段回落——平台缺省只在此处为常量，不进 config.yaml：备份计划属实例
// settings 资源面，S2 已受理展示）。
const (
	// DefaultBackupIntervalHours 是计划备份间隔（小时）。
	DefaultBackupIntervalHours = 24
	// DefaultBackupKeep 是保留份数。
	DefaultBackupKeep = 7
	// DefaultBackupHourUTC 是每日备份窗起点（UTC 小时）。
	DefaultBackupHourUTC = 3
)

// 一次性工具 job 的单步预算（平台常量，无配置面——cron 看门狗缺省同纪
// 律）：覆盖镜像分发 + 工具执行；job ctx 超预算即诚实判败（事件/事件面
// 台账承载结论）。
const (
	backupJobTimeout  = 10 * time.Minute  // pg_dump/--rdb 导出 + restic 入库（首传含镜像拉取）
	verifyJobTimeout  = 5 * time.Minute   // restic dump 回读 + 引擎级头校验
	restoreJobTimeout = 15 * time.Minute  // 停库重放（temp postgres 起停 + pg_restore）
	pruneJobTimeout   = 5 * time.Minute   // restic forget --prune（路径过滤）
	// upgradeWatchWindow 是升级健康门观察窗（provisioning 健康门预算同口
	// 径——start_period 30s + 探测窗；超窗判败 → digest 归位）。
	upgradeWatchWindow = 5 * time.Minute
	// upgradeOpTimeout 是升级编排整体预算（备份门 + 受控重建观察 + 归位
	// 余量——异步编排 ctx 与 Stop 排水的上界护栏）。
	upgradeOpTimeout = 30 * time.Minute
)

// repo 内路径约定（§2.6 备份目标行：命名空间 db/<instance>/；与控制面快
// 照同 repo——D-DB-6「独立 repo 被否」）。restic forget 的路径过滤与恢复
// 的 restic dump 都以该路径寻址。
const (
	pgBackupFilename    = "db.dump"
	redisBackupFilename = "dump.rdb"
)

// backupFilename 取模板对应的导出文件名（repo 内路径 = db/<instance>/<文件>）。
func backupFilename(templateID string) (string, error) {
	switch templateID {
	case dbtemplate.TemplatePostgres16:
		return pgBackupFilename, nil
	case dbtemplate.TemplateRedis7:
		return redisBackupFilename, nil
	default:
		return "", fmt.Errorf("database: template %q has no backup adapter", templateID)
	}
}

// resticCmd 是带寻址形态的 restic 命令前缀（path-style → 扩展选项
// `-o s3.bucket-lookup=path`——rustfs 与 path_style=true 的 external 端点
// 必需；statebackup 上传轨同口径的 job 内形态）。
func resticCmd(pathStyle bool) string {
	if pathStyle {
		return "restic -o s3.bucket-lookup=path"
	}
	return "restic"
}

// backupJobScript 拼装备份命令（流式导出 | restic 入库；--json 产出
// summary 供 snapshot id / total_bytes 提取）：
//
//	PG    pg_dump -Fc（逻辑备份，运行中一致性）| restic backup --stdin
//	Redis redis-cli --rdb /dev/stdout（RDB 流式）| restic backup --stdin
//
// /dev/stdout 而非字面 "-"：redis-cli 的 --rdb 接收**文件名**参数，设备
// 文件是管道流的可靠形态（alpine 容器内恒存在）。
func backupJobScript(in dbtemplate.BackupInput) ([]string, error) {
	filename, err := backupFilename(in.TemplateID)
	if err != nil {
		return nil, err
	}
	repoPath := "db/" + in.Instance + "/" + filename
	var export string
	switch in.TemplateID {
	case dbtemplate.TemplatePostgres16:
		export = fmt.Sprintf("pg_dump -h %s -U fleetly -d %s -Fc",
			in.Instance, dbtemplate.DatabaseName(in.Instance))
	case dbtemplate.TemplateRedis7:
		export = fmt.Sprintf("redis-cli -h %s --no-auth-warning --rdb /dev/stdout", in.Instance)
	default:
		return nil, fmt.Errorf("database: template %q has no backup adapter", in.TemplateID)
	}
	return []string{"sh", "-c",
		fmt.Sprintf("%s | %s backup --stdin --stdin-filename %s --json",
			export, resticCmd(in.S3PathStyle), repoPath)}, nil
}

// verifyJobScript 拼装回读校验命令（§2.6 Verify 契约——「备份假成功」零
// 容忍的引擎级实现）：restic 读回快照 + 引擎级校验——
//
//	PG    restic dump > /tmp/v.dump && pg_restore --list（exit 0 = 归档有效）
//	Redis restic dump | head -c 5 == "REDIS"（RDB magic）
func verifyJobScript(in dbtemplate.BackupOutcome) ([]string, error) {
	filename, err := backupFilename(in.TemplateID)
	if err != nil {
		return nil, err
	}
	repoPath := "db/" + in.Instance + "/" + filename
	switch in.TemplateID {
	case dbtemplate.TemplatePostgres16:
		return []string{"sh", "-c",
			fmt.Sprintf("%s dump %s %s > /tmp/v.dump && pg_restore --list /tmp/v.dump > /dev/null",
				resticCmd(in.S3PathStyle), in.SnapshotID, repoPath)}, nil
	case dbtemplate.TemplateRedis7:
		return []string{"sh", "-c",
			fmt.Sprintf("%s dump %s %s | head -c 5 | grep -q REDIS",
				resticCmd(in.S3PathStyle), in.SnapshotID, repoPath)}, nil
	default:
		return nil, fmt.Errorf("database: template %q has no verify adapter", in.TemplateID)
	}
}

// restoreJobScript 拼装原地恢复命令（停库重放；实例服务已 scale 0、job 钉
// 绑定节点挂数据卷 rw——「远端 local 卷不可经 manager 读」约束下的唯一
// 执行位置）。PG = **单 job**：dbtools 自 v0.2.1-dbtools.1 起是 debian/
// glibc 基底（postgres:16，与 dbtemplate.DefaultPostgresImage 同一钉定
// digest——引擎二进制与 dbtools 内的工具逐位同源），restic 取回快照与
// 临时实例重放在同一 job 内完成；W4 时代的双 job（dbtools 只取 dump 落卷
// + 引擎镜像起临时 postgres 重放）随 musl/glibc 跨 libc 重放风险的消除
// 而回退（编排更短、job 数减半、少一次镜像分发）。Redis 无引擎参与，同
// 样单 job（restoreRedisFetchScript）。
//
// 保留的防御（W4-S6 实测链逐条延续，单 job 下语义不变）：
//   - 快照 dump 先落卷根暂存文件再重放（材料落盘可对账；暂存文件重放后
//     清场——残留只会误导人工排查）；
//   - 降权 uid 从数据目录属主探测（dbtools 的 postgres uid 与卷上文件属
//     主对齐以数据目录为准，gosu/su-exec 均接受数字 uid）；
//   - 临时实例拉起带 kill -0 看护重启（scale 0 与 job 之间无任务全停等待
//     窗，旧引擎下线期一次性起动必失败）；
//   - pg_ctl 停临时实例与起动同 uid（root 形态失败会经 set -e 误判重放
//     失败）。
const replayDumpFilename = "fleetly-replay.dump"

func restorePostgresJobScript(in dbtemplate.RestoreInput) ([]string, error) {
	filename, err := backupFilename(in.TemplateID)
	if err != nil {
		return nil, err
	}
	repoPath := "db/" + in.Instance + "/" + filename
	dumpPath := in.VolumeTarget + "/" + replayDumpFilename
	db := dbtemplate.DatabaseName(in.Instance)
	script := strings.Join([]string{
		"set -e",
		// ① 取回快照落卷根暂存文件（restic 材料在 env）。
		fmt.Sprintf("%s dump %s %s > %s",
			resticCmd(in.S3PathStyle), in.SnapshotID, repoPath, dumpPath),
		// ② 临时实例重放（停库重放本体）。
		`if command -v gosu >/dev/null 2>&1; then PRIVDROP="gosu"; elif command -v su-exec >/dev/null 2>&1; then PRIVDROP="su-exec"; else echo "no privilege-drop tool in job image" >&2; exit 64; fi`,
		`PGDATA=/var/lib/postgresql/data/pgdata; export PGDATA`,
		// 降权 uid 从数据目录属主探测（gosu/su-exec 均接受数字 uid——
		// dbtools 的 postgres passwd 条目与卷上文件属主一致，探测只是
		// 免假设的收口）。
		`PGUID=$(stat -c %u "$PGDATA")`,
		`$PRIVDROP "$PGUID" postgres &`,
		`PGPID=$!`,
		`i=0`,
		`until pg_isready -h /var/run/postgresql -U fleetly >/dev/null 2>&1; do`,
		`  i=$((i+1))`,
		`  if [ "$i" -gt 90 ]; then echo "temporary postgres did not become ready" >&2; exit 65; fi`,
		`  if ! kill -0 "$PGPID" 2>/dev/null; then sleep 2; $PRIVDROP "$PGUID" postgres & PGPID=$!; fi`,
		`  sleep 1`,
		`done`,
		fmt.Sprintf(`psql -h /var/run/postgresql -U fleetly -d postgres -v ON_ERROR_STOP=1 -c 'DROP DATABASE IF EXISTS "%s";' -c 'CREATE DATABASE "%s";'`, db, db),
		fmt.Sprintf(`pg_restore -h /var/run/postgresql -U fleetly -d "%s" --no-owner %s`, db, dumpPath),
		// 停临时实例与起动同 uid（pg_ctl 拒以 root 运行——`set -e` 下
		// 根形态失败会让成功的重放被误判为失败）。
		`$PRIVDROP "$PGUID" pg_ctl -D "$PGDATA" -m fast stop`,
		// ③ 暂存 dump 清场（卷根文件不属集群数据）。
		fmt.Sprintf(`rm -f %s`, dumpPath),
	}, "\n")
	return []string{"sh", "-c", script}, nil
}

// pruneJobScript 拼装保留对齐命令（forget 以本实例的 repo 路径过滤——共
// 享 repo 下无过滤的 forget 会波及全部快照〔控制面备份与他实例〕；路径
// 过滤使 keep-last N 恰为本实例的保留策略，§2.6「prune 沿用台账保留期
// 删除语义」）。
func pruneJobScript(in dbtemplate.BackupOutcome, keep int) ([]string, error) {
	filename, err := backupFilename(in.TemplateID)
	if err != nil {
		return nil, err
	}
	repoPath := "db/" + in.Instance + "/" + filename
	return []string{"sh", "-c",
		fmt.Sprintf("%s forget --keep-last %d --path %s --prune",
			resticCmd(in.S3PathStyle), keep, repoPath)}, nil
}

// ── dbtemplate.EngineAdapter 的 Manager 兑现（接口非装饰——编排紧邻底
//    座，兑现点即 Manager 方法；编排入口在 backup.go/restore.go）────────

// 编译期契约钉：Manager 兑现 §2.6 EngineAdapter 全接口（Backup/Restore/
// Verify/RotateCredential——S1 钉接口、S5 兑现）。
var _ dbtemplate.EngineAdapter = (*Manager)(nil)

// Backup 逻辑备份（dbtemplate.EngineAdapter 契约）：一次性 job 流式导出 →
// restic 入库（repo 内路径 db/<instance>/）。失败/无快照 id 都是诚实错误
// ——调用方（编排层）落 db.backup_failed 事件，无台账行。
func (m *Manager) Backup(ctx context.Context, in dbtemplate.BackupInput) (dbtemplate.BackupOutcome, error) {
	script, err := backupJobScript(in)
	if err != nil {
		return dbtemplate.BackupOutcome{}, err
	}
	net, err := naming.DBNetworkName(in.Instance)
	if err != nil {
		return dbtemplate.BackupOutcome{}, err
	}
	outcome, err := m.runToolsJob(ctx, toolsJobInput{
		instance:   in.Instance,
		purpose:    "backup",
		script:     script,
		env:        toolsJobEnv(in.Password, in.Repository, in.ResticPassword, in.S3AccessKeyID, in.S3SecretKey, in.S3Region),
		networks:   jobNetworks(in.AttachRustfsNetwork, net),
		timeout:    backupJobTimeout,
		bindNode:   in.BindNodeID,
	})
	if err != nil {
		return dbtemplate.BackupOutcome{}, err
	}
	if !outcome.Success() {
		return dbtemplate.BackupOutcome{}, fmt.Errorf("backup job failed: %s", jobFailureText(outcome))
	}
	snap, size := parseResticSummary(outcome.Stdout)
	if snap == "" {
		return dbtemplate.BackupOutcome{}, errors.New("restic backup produced no snapshot id (summary message missing)")
	}
	return m.outcomeContext(in, snap, size), nil
}

// Verify 回读校验（dbtemplate.EngineAdapter 契约）：restic 读回 + 引擎级
// 头校验。exit 0 = 有效；其余（含超预算）= 校验失败（红色告警面）。
func (m *Manager) Verify(ctx context.Context, in dbtemplate.BackupOutcome) error {
	script, err := verifyJobScript(in)
	if err != nil {
		return err
	}
	net, err := naming.DBNetworkName(in.Instance)
	if err != nil {
		return err
	}
	outcome, err := m.runToolsJob(ctx, toolsJobInput{
		instance: in.Instance,
		purpose:  "verify",
		script:   script,
		env:      toolsJobEnv("", in.Repository, in.ResticPassword, in.S3AccessKeyID, in.S3SecretKey, in.S3Region),
		networks: jobNetworks(in.AttachRustfsNetwork, net),
		timeout:  verifyJobTimeout,
		bindNode: in.BindNodeID,
	})
	if err != nil {
		return err
	}
	if !outcome.Success() {
		return fmt.Errorf("verify job failed: %s", jobFailureText(outcome))
	}
	return nil
}

// Restore 原地恢复（dbtemplate.EngineAdapter 契约）：纯执行体——scale 0/
// 事件/失败口径归 restore.go 编排；本方法只跑恢复 job。PG = 单 dbtools job
//（restic 取回 + 临时 postgres 重放同 job——v0.2.1-dbtools.1 起基底与引
// 擎同源 glibc，跨 libc 重放风险消除，见 restorePostgresJobScript 注）；
// Redis = 单 dbtools job（RDB 落卷）。
func (m *Manager) Restore(ctx context.Context, in dbtemplate.RestoreInput) error {
	net, err := naming.DBNetworkName(in.Instance)
	if err != nil {
		return err
	}
	materials := toolsJobEnv("", in.Repository, in.ResticPassword, in.S3AccessKeyID, in.S3SecretKey, in.S3Region)
	mounts := []JobMount{{VolumeName: in.VolumeName, Target: in.VolumeTarget, ReadOnly: false}}
	nets := jobNetworks(in.AttachRustfsNetwork, net)
	var script []string
	if in.TemplateID == dbtemplate.TemplatePostgres16 {
		script, err = restorePostgresJobScript(in)
	} else {
		// Redis：fetch 即重放完成（RDB 落卷 + AOF 目录清除在 fetch script
		// 的卷内收尾——dbtools 与 redis 引擎镜像同为 glibc 可执行面）。
		script, err = restoreRedisFetchScript(in)
	}
	if err != nil {
		return err
	}
	outcome, err := m.runToolsJob(ctx, toolsJobInput{
		instance: in.Instance,
		purpose:  "restore",
		script:   script,
		env:      materials,
		networks: nets,
		mounts:   mounts,
		timeout:  restoreJobTimeout,
		bindNode: in.BindNodeID,
	})
	if err != nil {
		return err
	}
	if !outcome.Success() {
		return fmt.Errorf("restore job failed: %s", jobFailureText(outcome))
	}
	return nil
}

// restoreRedisFetchScript 是 Redis 的单 job 恢复命令（RDB 落卷 + AOF 目录
// 清除——下次启动按 RDB 装载；无引擎参与，dbtools 内 restic 取回即完成）。
func restoreRedisFetchScript(in dbtemplate.RestoreInput) ([]string, error) {
	filename, err := backupFilename(in.TemplateID)
	if err != nil {
		return nil, err
	}
	repoPath := "db/" + in.Instance + "/" + filename
	script := strings.Join([]string{
		"set -e",
		fmt.Sprintf("%s dump %s %s > /data/dump.rdb", resticCmd(in.S3PathStyle), in.SnapshotID, repoPath),
		"rm -rf /data/appendonlydir",
	}, "\n")
	return []string{"sh", "-c", script}, nil
}

// RotateCredential 引擎侧热轮换（dbtemplate.EngineAdapter 契约——薄委托
// rotate.go 的既有原语，接口非装饰）：PG = 一次性容器 ALTER USER；Redis =
// 无引擎侧动作（spec 启动参数投递，收敛 duty 按哈希差换挂）。
func (m *Manager) RotateCredential(ctx context.Context, in dbtemplate.RotateInput) error {
	inst, err := m.store.GetDatabaseInstanceByName(ctx, in.Instance)
	if err != nil {
		return err
	}
	old, err := m.decryptCredential(&inst)
	if err != nil {
		return err
	}
	tpl, err := dbtemplate.Get(inst.Template)
	if err != nil {
		return err
	}
	switch inst.Template {
	case dbtemplate.TemplatePostgres16:
		return m.rotatePostgresCredential(ctx, &inst, tpl.Image, old, in.NewPassword)
	case dbtemplate.TemplateRedis7:
		return nil // 无引擎侧动作（凭据 = spec 启动参数，rotate.go 编排承载）
	default:
		return fmt.Errorf("database: template %q has no rotation adapter", inst.Template)
	}
}

// ── job 载荷归一 ─────────────────────────────────────────────────────────────

// toolsJobInput 是工具 job 的内部载荷（adapter 各方法 → runToolsJob 的归
// 一入口）。
type toolsJobInput struct {
	instance string
	purpose  string
	script   []string
	env      []string
	networks []string
	mounts   []JobMount
	timeout  time.Duration
	bindNode string
}

// restic 同仓写锁互斥的有界重试（W4-S6 e2e 实测）：库备份与控制面状态备
// 份共享同一 restic repo，而引擎的 post_deploy 钩子会在每次应用部署成功后
// 触发一次状态备份——与手动/计划库备份天然并发，先到者持独占写锁，后到者
// 以「unable to create lock in backend」退败。restic 无内建等锁，job 侧以
// 撞锁文本为条件做有界退避重试（12 × 10s ≈ 2 分钟容忍窗，落在各步预算内
// ——backupJobTimeout 等常量本就为「镜像分发 + 工具执行」留了余量）；非
// 撞锁失败零重试（诚实失败原则：真失败快速红，不烧预算）。
const (
	repoLockRetryAttempts = 12
	repoLockRetryDelay    = 10 * time.Second
)

// isRepoLockConflict 报告一次 job 产出是否为 restic 撞锁退败（重试判据；
// 文本来自 restic exit_error 的稳定措辞）。
func isRepoLockConflict(out JobRunOutcome, err error) bool {
	return strings.Contains(jobFailureText(out), "unable to create lock")
}

// runToolsJob 构造并执行一次工具 job（命名/label/env 公共面在此归一；
// HOME=/tmp 兜底 restic 的缓存目录解析——job 以任意用户身份运行时
// os.UserCacheDir 的 HOME 依赖不作假设；凭据材料在 env 由调用方注入，
// 本函数零展开）。撞 restic 互斥写锁时有界重试（见 repoLockRetryAttempts）。
func (m *Manager) runToolsJob(ctx context.Context, in toolsJobInput) (JobRunOutcome, error) {
	jctx, cancel := context.WithTimeout(ctx, in.timeout)
	defer cancel()
	env := append([]string{"HOME=/tmp"}, in.env...)

	run := func() (JobRunOutcome, error) {
		name, err := naming.DBJobName(in.instance, in.purpose, ulid.Make().String())
		if err != nil {
			return JobRunOutcome{}, err
		}
		// 放置钉定 = 平台节点身份 label 约束（node.labels.fleetly.node-id ==
		// <平台ID n_<ULID>>；placement.ConstraintFor 同公式的就地形态——
		// database 不反依赖 placement。W4-S6 e2e 实测修正：此前误用
		// `node.id == <平台ID>`——swarm node.id 是引擎侧节点 ID，与平台 ID
		// 恒不相等，job 永远 PENDING（"scheduling constraints not satisfied"）。
		// fake 底座不校验约束真实性，单测抓不到——真机闭环兜住的典型）。
		return m.docker.JobRun(jctx, JobRunInput{
			Name:        name,
			Image:       DefaultDatabaseToolsImage,
			Cmd:         in.script,
			Env:         env,
			Networks:    in.networks,
			Mounts:      in.mounts,
			Constraints: []string{"node.labels." + state.LabelNodeID + " == " + in.bindNode},
			Labels: map[string]string{
				state.LabelManaged:  state.ManagedLabelValue,
				state.LabelDatabase: in.instance,
			},
		})
	}

	out, err := run()
	for attempt := 1; err == nil && !out.Success() && isRepoLockConflict(out, err) && attempt < repoLockRetryAttempts; attempt++ {
		m.log.Info("database: tools job hit a restic repo lock (concurrent control-plane backup); retrying",
			"instance", in.instance, "purpose", in.purpose, "attempt", attempt)
		select {
		case <-jctx.Done():
			return out, err
		case <-time.After(repoLockRetryDelay):
		}
		out, err = run()
	}
	return out, err
}

// toolsJobEnv 组装工具 job 的 env 集（凭据材料只进 env；KEY 集恒定——
// 空值省略，命令词表与 env 键集一一对应）。RESTIC_REPOSITORY 由编排层解析
// 的 repo 目标携带——W4-S6 e2e 实测修正：此前漏设，restic 以
// 「Please specify repository location」诚实退败（词表与 env 键集声称
// 一一对应，缺这一键 = 备份/校验/恢复永远无 repo 可寻址；fake 底座不看
// env 真实性，单测抓不到）。
func toolsJobEnv(enginePassword, resticRepository, resticPassword, accessKey, secretKey, region string) []string {
	var env []string
	if enginePassword != "" {
		env = append(env, "PGPASSWORD="+enginePassword, "REDISCLI_AUTH="+enginePassword)
	}
	if resticRepository != "" {
		env = append(env, "RESTIC_REPOSITORY="+resticRepository)
	}
	env = append(env,
		"RESTIC_PASSWORD="+resticPassword,
		"AWS_ACCESS_KEY_ID="+accessKey,
		"AWS_SECRET_ACCESS_KEY="+secretKey,
	)
	if region != "" {
		env = append(env, "AWS_DEFAULT_REGION="+region)
	}
	return env
}

// jobNetworks 组装 job 网络挂接（rustfs 模式追加 fleetly-rustfs-net——托
// 管端点 http://rustfs:9000 只在该网可解析；实例共享网络由编排层以首元素
// 携带——备份/恢复都要以实例别名可达）。
func jobNetworks(attachRustfs bool, instanceNet string) []string {
	out := []string{instanceNet}
	if attachRustfs {
		out = append(out, state.RustfsNetworkName)
	}
	return out
}

// outcomeContext 把备份执行上下文随行到产出（Verify 的影子作业材料——
// 同 repo/节点/网络/材料重放）。
func (m *Manager) outcomeContext(in dbtemplate.BackupInput, snap string, size int64) dbtemplate.BackupOutcome {
	return dbtemplate.BackupOutcome{
		SnapshotID:          snap,
		SizeBytes:           size,
		Instance:            in.Instance,
		TemplateID:          in.TemplateID,
		BindNodeID:          in.BindNodeID,
		Repository:          in.Repository,
		ResticPassword:      in.ResticPassword,
		S3AccessKeyID:       in.S3AccessKeyID,
		S3SecretKey:         in.S3SecretKey,
		S3Region:            in.S3Region,
		S3PathStyle:         in.S3PathStyle,
		AttachRustfsNetwork: in.AttachRustfsNetwork,
	}
}

// jobFailureText 归一 job 失败诊断（任务 Err + 退出码 + 尾部输出摘要——
// 命令词表保证无凭据，文本再经编排层 scrub 兜底；单行化截断防失控）。
func jobFailureText(o JobRunOutcome) string {
	parts := []string{}
	if o.Err != "" {
		parts = append(parts, o.Err)
	}
	if o.ExitCode > 0 {
		parts = append(parts, fmt.Sprintf("exit code %d", o.ExitCode))
	}
	if tail := strings.TrimSpace(jobOutputTail(o.Stdout)); tail != "" {
		parts = append(parts, "output tail: "+tail)
	}
	if len(parts) == 0 {
		return "job ended without a task verdict"
	}
	return singleLine(strings.Join(parts, "; "))
}

// jobOutputTail 取输出尾行（最后一条非空行，256 字节——错误摘要在尾部；
// restic --json 的 summary/错误行是最后的结构化行）。
func jobOutputTail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if len(line) > 256 {
			line = line[:256]
		}
		return line
	}
	return ""
}

// resticSummary 是 restic backup --json 的 summary 消息（restic 0.19
// scripting 契约：message_type=summary、snapshot_id/total_bytes 在快照创
// 建成功时非空）。
type resticSummary struct {
	MessageType string `json:"message_type"`
	SnapshotID  string `json:"snapshot_id"`
	TotalBytes  int64  `json:"total_bytes"`
}

// parseResticSummary 从 restic backup --json 输出提取本次快照 id 与字节量
//（逐行 JSON：取 message_type=summary 的行；无 → 空串）。
func parseResticSummary(output string) (string, int64) {
	var snap string
	var size int64
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var s resticSummary
		if json.Unmarshal([]byte(line), &s) != nil {
			continue
		}
		if s.MessageType == "summary" && s.SnapshotID != "" {
			snap = s.SnapshotID
			size = s.TotalBytes
		}
	}
	return snap, size
}
