package statebackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Manager 是状态备份管理器：触发入口（daily/pre_upgrade/post_deploy/manual）
// + 每日守护循环 + 健康上报（system status backup 组件）+ 保留期清理。
// 零框架依赖——lynx.Service 装配壳在 cmd/fleetlyd。
type Manager struct {
	cfg   Config
	store *state.Store
	log   *slog.Logger

	// keyPath 是主密钥文件路径（只读其字节算指纹，绝不复制进备份目录）。
	keyPath string
	// version 是平台版本（manifest.platform_version；构建 -ldflags 注入）。
	version string

	// mu 串行化备份执行（daily/post_deploy/manual 三路触发互斥——并发
	// VACUUM INTO 浪费 IO 且台账顺序难读；排队执行）。
	mu sync.Mutex

	// inflight 计数在途的 post-deploy 备份挂钩（X-7/MG-3，B6）：engine
	// 成功路径以 `go postDeploy(rec)` 逸出主链，此前 Stop 只等 daily 循环
	// ——关停时在途 VACUUM 与 Store.Close（OnPostStop 释放连接池）竞态。
	// RunPostDeploy 进入时 Add、完成 Done；Stop 在循环退出后等待在途
	//（带预算，见 postDeployStopBudget）。
	inflight sync.WaitGroup

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}

	// verifyFn 是回读校验步骤（测试注入点；nil = 真实校验。签名收窄为
	// 「快照路径 → 校验产物」，注入失败即 verify 失败路径）。
	verifyFn func(dbPath string) (snapshotFacts, error)

	// ── 上传轨（E3-3，WithUpload 装配；nil = 未接线——upload_status 如实
	// 保持 none，见 uploadSnapshot）──
	// runner 是 restic 钉版容器一次性执行端口（生产实现 = substrate.Client）。
	runner ResticRunner
	// box 是 envelope 加解密器（restic repo 口令的加解密与惰性生成，D-S3-5；
	// 与主密钥文件分离语义见 resticPassword 注）。
	box *secrets.Box
}

// WithUpload 装配上传轨（E3-3；链式构造，nil 合法——上传轨未接线的测试/
// 精简形态，upload_status 保持 none 如实可见）。runner 为 restic 钉版容器
// 执行器（生产 = *substrate.Client 的 RunRestic）；box 为 envelope 加解密
// 器（restic repo 口令的解密与惰性生成落库）。
func (m *Manager) WithUpload(runner ResticRunner, box *secrets.Box) *Manager {
	m.runner = runner
	m.box = box
	return m
}

// NewManager 构造备份管理器。dir 由装配点回落缺省（state 库同目录 backups）；
// keyPath 只用于指纹与分离性守卫。密钥文件与备份目录重叠是配置错误：
// 构造期即拒绝（fail-fast——「密钥绝不进备份目录」是硬约束，不静默降级）。
func NewManager(cfg Config, dir, keyPath, version string, st *state.Store, log *slog.Logger) (*Manager, error) {
	cfg.Dir = dir
	if cfg.Dir == "" {
		return nil, errors.New("statebackup: backup dir is empty")
	}
	if keyPath == "" {
		return nil, errors.New("statebackup: master key path is empty")
	}
	cfg = cfg.Normalize()
	if err := keySeparationViolation(cfg.Dir, keyPath); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	close(done)
	return &Manager{
		cfg:     cfg,
		store:   st,
		log:     log,
		keyPath: keyPath,
		version: version,
		stop:    make(chan struct{}),
		done:    done,
	}, nil
}

// keySeparationViolation 报告主密钥与备份目录的分离性：密钥文件位于备份
// 目录内（或备份目录就是密钥目录）→ 返回错误。比较经 Clean+Abs 归一，
// 前缀比对带分隔符边界（/var/lib/fleetly/backups 不误伤 /var/lib/fleetly2）。
func keySeparationViolation(dir, keyPath string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("statebackup: resolve backup dir: %w", err)
	}
	absKey, err := filepath.Abs(keyPath)
	if err != nil {
		return fmt.Errorf("statebackup: resolve key path: %w", err)
	}
	rel, err := filepath.Rel(absDir, absKey)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("statebackup: master key %s lives inside backup dir %s — "+
			"the key must never be backed up with the data (move secrets.key_path out of backup.dir)",
			keyPath, dir)
	}
	return nil
}

// Dir 返回备份根目录。
func (m *Manager) Dir() string { return m.cfg.Dir }

// Keep 返回生效保留份数。
func (m *Manager) Keep() int { return m.cfg.Keep }

// ── 触发入口 ─────────────────────────────────────────────────────────────────

// RunDaily 执行一轮 daily 备份（守护循环与测试共用）。
func (m *Manager) RunDaily(ctx context.Context) (state.StateBackup, error) {
	return m.Trigger(ctx, state.BackupKindDaily)
}

// RunPreUpgrade 执行升级前热备快照（upgrade.sh 编排步骤 ②；失败必须阻断
// 升级——调用方拿 error 决定，脚本侧 verify_status 也二次核对）。
func (m *Manager) RunPreUpgrade(ctx context.Context) (state.StateBackup, error) {
	return m.Trigger(ctx, state.BackupKindPreUpgrade)
}

// RunPostDeploy 是引擎成功路径的挂钩形态（engine.PostDeployHook 签名）：
// 异步执行（部署主链不等备份），带独立预算；失败只落台账/审计 + 日志，
// 永不 panic 打穿引擎 tick。X-7/MG-3：进入/退出经 inflight 计数——Stop
// 据此等待在途快照收口（与 Store.Close 的竞态消除）。E3-3：预算 = 本地
// 快照轨（TriggerTimeout）+ 上传轨（uploadTimeout）——外层只作上界护栏，
// 两段各自预算在 Trigger 内生效。
func (m *Manager) RunPostDeploy(rec state.DeployRecord) {
	m.inflight.Add(1)
	defer m.inflight.Done()
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.TriggerTimeout+uploadTimeout)
	defer cancel()
	if _, err := m.Trigger(ctx, state.BackupKindPostDeploy); err != nil {
		m.log.Error("backup: post-deploy backup failed", "app", rec.AppName,
			"deployment", rec.ID, "error", err)
	}
}

// Trigger 同步执行一次备份（kind 必须是注册词表；manual/pre_upgrade 的
// 同步语义供 RPC 与升级脚本直接等待结果）。E3-3：同步语义延伸到上传步
// ——响应携带上传结论（verified 行回读上传后的最终台账投影）；本地快照
// 轨走 TriggerTimeout 预算、上传轨独立 uploadTimeout 预算（WithTimeout
// 只收紧不放宽：调用方 ctx 更短时以其为准）。
func (m *Manager) Trigger(ctx context.Context, kind string) (state.StateBackup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	upCtx, upCancel := context.WithTimeout(ctx, uploadTimeout)
	defer upCancel()
	ctx, cancel := context.WithTimeout(ctx, m.cfg.TriggerTimeout)
	defer cancel()
	return m.runOnce(ctx, upCtx, kind)
}

// runOnce 是单次备份的完整链路（mu 已持有；ctx = 本地快照轨预算，
// upCtx = 上传轨预算）：建目录 → VACUUM INTO → 回读校验 → sha256/指纹 →
// manifest → 台账+审计 → （成功时）保留期清理 → （成功时）restic 上传步
// （E3-3 追加步：本地信任闭环不改写，任何上传失败只红上传面）。任何本地
// 步失败都收敛为「台账 failed 行 + 红色告警三件套」（审计随台账同事务；
// 组件健康读台账；错误日志由本函数写出）。
func (m *Manager) runOnce(ctx, upCtx context.Context, kind string) (state.StateBackup, error) {
	id := ulid.Make().String()
	dir := filepath.Join(m.cfg.Dir, id)
	dbPath := filepath.Join(dir, "fleetly.db")

	// 分离性守卫每次执行复核（运行期配置漂移的兜底；构造期已挡一轮）。
	sepErr := keySeparationViolation(m.cfg.Dir, m.keyPath)

	w := state.BackupWrite{
		ID:   id,
		Kind: kind,
		Path: dbPath,
	}

	fail := func(err error) (state.StateBackup, error) {
		w.Verify = state.BackupVerifyFailed
		w.Error = err.Error()
		// 台账落账自身也可能失败（库故障）：此时错误再向上抛，但台账/审计
		// 的红色面已尽力（日志恒在）。
		rec, recErr := m.store.RecordStateBackup(ctx, w)
		if recErr != nil {
			m.log.Error("backup: FAILED and ledger write failed (red alarm: no green path exists)",
				"id", id, "kind", kind, "backup_error", err.Error(), "ledger_error", recErr.Error())
			return state.StateBackup{}, fmt.Errorf("statebackup: backup failed (%v) and ledger write failed (%w)", err, recErr)
		}
		m.log.Error("backup: FAILED (red alarm: verify_status=failed recorded; "+
			"system status backup component is unhealthy until next verified backup)",
			"id", id, "kind", kind, "error", err.Error())
		return rec, fmt.Errorf("statebackup: backup %s failed: %w", id, err)
	}

	if sepErr != nil {
		return fail(sepErr)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(fmt.Errorf("create backup dir: %w", err))
	}
	if err := m.store.VacuumInto(ctx, dbPath); err != nil {
		return fail(err)
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		return fail(fmt.Errorf("stat snapshot: %w", err))
	}
	w.Size = info.Size()

	// sha256 先于校验落定（manifest 的核心字段；校验失败也要把已算出的
	// 产物事实写全——事后能对损坏文件做归因）。
	sum, err := fileSHA256(dbPath)
	if err != nil {
		return fail(fmt.Errorf("hash snapshot: %w", err))
	}
	w.SHA256 = sum

	facts, verr := m.verifySnapshot(dbPath)
	keyFP, kerr := m.keyFingerprint()
	if kerr != nil {
		verr = errors.Join(verr, fmt.Errorf("key fingerprint: %w", kerr))
	}
	if verr != nil {
		// manifest 也落（verify_status=failed 的诚实产物；恢复方不会误用
		// 一个带失败标记的目录）。
		man := Manifest{
			ID: id, Kind: kind, CreatedAt: time.Now().UTC().Format(time.RFC3339),
			PlatformVersion: m.version, SchemaVersion: facts.SchemaVersion,
			Database: backupDBName, SizeBytes: w.Size, SHA256: sum,
			VerifyStatus: state.BackupVerifyFailed, KeyFingerprint: keyFP,
			Error: verr.Error(),
		}
		if werr := writeManifest(dir, man); werr != nil {
			verr = errors.Join(verr, fmt.Errorf("write failed-manifest: %w", werr))
		}
		return fail(verr)
	}

	man := Manifest{
		ID: id, Kind: kind, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		PlatformVersion: m.version, SchemaVersion: facts.SchemaVersion,
		Database: backupDBName, SizeBytes: w.Size, SHA256: sum,
		VerifyStatus: state.BackupVerifyVerified, KeyFingerprint: keyFP,
		Tables: facts.Tables,
	}
	if err := writeManifest(dir, man); err != nil {
		return fail(fmt.Errorf("write manifest: %w", err))
	}

	w.Verify = state.BackupVerifyVerified
	rec, err := m.store.RecordStateBackup(ctx, w)
	if err != nil {
		return state.StateBackup{}, fmt.Errorf("statebackup: record verified backup: %w", err)
	}
	m.log.Info("backup: verified snapshot recorded", "id", id, "kind", kind,
		"size_bytes", w.Size, "sha256", sum[:12], "schema_version", facts.SchemaVersion)
	// 保留期清理先于上传（既有时点不变）：本地配额与台账/目录对账用本地
	// 预算 ctx（上传可达 10m，本地 ctx 届时会死）；本次刚落的行是最新
	// verified，永不入淘汰集（prune 按 keep 保留各 kind 最近份）。孤儿目录
	// 宽容期（24h）远大于上传预算，在途目录无误伤面。
	m.prune(ctx)
	// 上传步（E3-3）：verify 成功落账之后的追加步，独立预算（D-S3-4）。
	// 失败只红上传面（upload_status/事件/组件），不影响本次备份的成功语义
	// ——返回值携带上传后的最终台账投影（Trigger 同步响应据此反映）。
	rec = m.uploadSnapshot(upCtx, rec)
	return rec, nil
}

// backupDBName 是备份目录内的快照文件名（布局契约：
// <backup.dir>/<ULID>/fleetly.db + manifest.json）。
const backupDBName = "fleetly.db"

// failedLedgerKeep 是失败台账行的独立保留上限（R3）：失败行入台账用于
// 诊断，但不占 kind 配额、也非无限保留——超出保留最近 N 条，更旧的删行。
const failedLedgerKeep = 10

// orphanGracePeriod 是孤儿备份目录回收的宽容期（X-7/MG-3）：runOnce 的
// 顺序是「先建目录 → VACUUM → 校验 → 落台账」——台账行落定前的目录没有
// 行可对账，只有 mtime 年龄可判；24h 宽容期保证任何在途备份（单次预算
// ≤5min）绝不误伤。
const orphanGracePeriod = 24 * time.Hour

// prune 保留期清理（一轮整改⑥ per-kind 配额 + 二轮 R3 failed 分账）：
//   - verified 行：每种 kind 各自保留最近 keep 份（keep 语义与配置键不变）
//     ——跨 kind 全局配额会让部署频繁的一天把 daily/pre_upgrade 全部挤出，
//     「每日备份」轨名存实亡；failed 行不占 kind 配额（连续失败风暴不得
//     挤掉 verified 恢复点——恢复点塌缩是备份信任闭环的杀手）；
//   - failed 行：独立小上限 failedLedgerKeep，保留最近 N 条用于诊断，
//     其余删行（失败行可能残留半成品目录，RemoveAll 对不存在路径为
//     no-op，一并兜住）。
//
// 超配额行 → 删行 + 审计 backup.pruned（审计行为不变）+ 目录清除。台账
// 只描述真实存在的备份（不保留幽灵行——ListBackups 的消费方看到的每一行
// 都可取用）。清理失败只告警不回滚本次备份。
func (m *Manager) prune(ctx context.Context) {
	rows, err := m.store.ListStateBackups(ctx, 0)
	if err != nil {
		m.log.Warn("backup: retention scan failed", "error", err)
		return
	}
	// ListStateBackups 按 created_at 倒序（新 → 旧）：verified 逐 kind 计
	// 数、超 keep 即淘汰；failed 独立计数、超 failedLedgerKeep 即淘汰。
	perKind := make(map[string]int)
	failed := 0
	for _, victim := range rows {
		overQuota := false
		if victim.VerifyStatus == state.BackupVerifyVerified {
			if perKind[victim.Kind] >= m.cfg.Keep {
				overQuota = true
			} else {
				perKind[victim.Kind]++
			}
		} else {
			failed++
			overQuota = failed > failedLedgerKeep
		}
		if !overQuota {
			continue
		}
		path, err := m.store.DeleteStateBackup(ctx, victim.ID)
		if err != nil {
			m.log.Warn("backup: retention delete row failed", "id", victim.ID, "error", err)
			continue
		}
		if path != "" {
			if err := os.RemoveAll(filepath.Dir(path)); err != nil {
				m.log.Warn("backup: retention remove dir failed", "id", victim.ID, "error", err)
			}
		}
		m.log.Info("backup: retention pruned", "id", victim.ID, "kind", victim.Kind)
	}
	// 孤儿目录扫描（X-7/MG-3）：台账外的半成品目录兜底对账。
	m.pruneOrphanDirs(ctx, rows)
}

// pruneOrphanDirs 孤儿备份目录回收（X-7/MG-3，B6：资源台账兜底对账——
// 台账外资源不再静默累积）：备份根下无台账行对应的 ULID 目录（「建目录 →
// 落台账」之间行建失败/进程崩溃的半成品——prune 只按台账行清目录，这类
// 目录此前永不回收），mtime 超过宽容期（避开在途写入）→ 删除目录 + 审计
// backup.pruned_orphan（action 对齐 backup.pruned 风格——删必留痕）。
// 非 ULID 词形的目录不动（运维自置内容不猜）。
func (m *Manager) pruneOrphanDirs(ctx context.Context, rows []state.StateBackup) {
	ledger := make(map[string]bool, len(rows))
	for _, r := range rows {
		ledger[r.ID] = true
	}
	entries, err := os.ReadDir(m.cfg.Dir)
	if err != nil {
		if !os.IsNotExist(err) {
			m.log.Warn("backup: orphan dir scan failed", "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-orphanGracePeriod)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || ledger[name] {
			continue
		}
		if _, perr := ulid.Parse(name); perr != nil {
			continue // 非 ULID 词形：备份根下的运维自置内容，不猜不动
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			continue // 宽容期内：可能是在途备份的半成品
		}
		if rerr := os.RemoveAll(filepath.Join(m.cfg.Dir, name)); rerr != nil {
			m.log.Warn("backup: orphan dir remove failed", "dir", name, "error", rerr)
			continue
		}
		if aerr := m.store.InTx(ctx, func(tx *state.Tx) error {
			return tx.WriteAudit(ctx, state.AuditEntry{
				Actor:  "system",
				Action: "backup.pruned_orphan",
				Target: "backup:" + name,
				Result: "ok",
				DiffSummary: state.DiffSummary("id", name,
					"reason", "orphan dir without ledger row"),
			})
		}); aerr != nil {
			m.log.Warn("backup: orphan prune audit write failed", "dir", name, "error", aerr)
		}
		m.log.Info("backup: orphan dir pruned (no ledger row)", "dir", name)
	}
}

// ── 每日守护循环（Janitor 同款形态）────────────────────────────────────────

// Start 非阻塞启动每日备份循环（启动即备份一拍——新装平台在首启即有
// verified 备份，系统状态 backup 组件不长期红着；此后每 Interval 一拍）。
func (m *Manager) Start(ctx context.Context) error {
	m.done = make(chan struct{})
	go m.loop(ctx)
	return nil
}

// postDeployStopBudget 是 Stop 等待在途 post-deploy 备份的预算上界
// （X-7/MG-3）：60s 覆盖 v0.1 规模库的 VACUUM + 校验（亚秒级）且留足
// Stop 场景的现实耐心；调用方 ctx 剩余预算更小时取小。
const postDeployStopBudget = 60 * time.Second

// Stop 停止每日循环并等待退出。X-7/MG-3（B6）：循环退出后**等待在途的
// post-deploy 备份**（引擎成功路径逸出的异步 goroutine——此前无人等它，
// 关停时在途 VACUUM 与 Store.Close 竞态）；预算取 min(调用方 ctx 剩余
// 预算, postDeployStopBudget)，预算尽让位返回（在途 goroutine 随进程
// 退出兜底，台账会缺一行——诚实失败，非静默绿色）。
func (m *Manager) Stop(ctx context.Context) error {
	m.stopOnce.Do(func() { close(m.stop) })
	<-m.done
	waited := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(waited)
	}()
	budget := postDeployStopBudget
	if dl, ok := ctx.Deadline(); ok {
		if remain := time.Until(dl); remain < budget {
			budget = remain
		}
	}
	select {
	case <-waited:
	case <-time.After(budget):
		m.log.Warn("backup: stop timed out waiting for in-flight post-deploy backup (abandoned; process exit bounds it)")
	case <-ctx.Done():
		m.log.Warn("backup: stop ctx cancelled while waiting for in-flight post-deploy backup (abandoned; process exit bounds it)")
	}
	return nil
}

// CheckHealth 是 system status backup 组件的检查器（红色告警的第三面）：
// 无任何备份记录 / 最近一次 verify 失败 → 不健康（错误原文可行动）；
// E3-3：最近一次本地 verified 但远端上传 failed → 不健康（degraded——
// 口径「local snapshot ok, remote upload failed」；本地 verify 语义不变，
// 上传失败不回写 verify_status）。upload_status=none 不红（s3.mode=unset
// 是合法态 / 上传步时序窗口内的中间态）。
func (m *Manager) CheckHealth() error {
	latest, err := m.store.LatestStateBackup(context.Background())
	if err != nil {
		return fmt.Errorf("statebackup: read ledger: %w", err)
	}
	if latest == nil {
		return errors.New("statebackup: no state backup recorded yet (daily loop pending or never ran)")
	}
	if latest.VerifyStatus != state.BackupVerifyVerified {
		msg := latest.Error
		if msg == "" {
			msg = "verify_status=" + latest.VerifyStatus
		}
		return fmt.Errorf("statebackup: last backup %s (%s) is NOT verified: %s",
			latest.ID, latest.Kind, msg)
	}
	if latest.UploadStatus == state.BackupUploadFailed {
		msg := latest.UploadError
		if msg == "" {
			msg = "upload_error missing (ledger row incomplete)"
		}
		return fmt.Errorf("statebackup: last backup %s (%s): local snapshot ok, remote upload failed: %s",
			latest.ID, latest.Kind, msg)
	}
	return nil
}

func (m *Manager) loop(ctx context.Context) {
	defer close(m.done)
	if _, err := m.RunDaily(ctx); err != nil {
		// 红色告警已在 runOnce 内三件套落全，这里只补一行运行面日志。
		m.log.Warn("backup: daily run failed", "error", err)
	}
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-ticker.C:
			if _, err := m.RunDaily(ctx); err != nil {
				m.log.Warn("backup: daily run failed", "error", err)
			}
		}
	}
}

// ── manifest 与指纹 ──────────────────────────────────────────────────────────

// Manifest 是备份目录内 manifest.json 的形态（备份集的元数据事实；恢复
// 流程第一步即核对：sha256 与快照一致 + key_fingerprint 与现役主密钥文件
// 的 sha256sum 一致，不一致拒绝半恢复——state-model §2.7 恢复顺序②）。
// 主密钥本体绝不入备份目录，manifest 只携带指纹。
type Manifest struct {
	ID              string           `json:"id"`
	Kind            string           `json:"kind"`
	CreatedAt       string           `json:"created_at"`
	PlatformVersion string           `json:"platform_version"`
	SchemaVersion   int64            `json:"schema_version"`
	Database        string           `json:"database"`
	SizeBytes       int64            `json:"size_bytes"`
	SHA256          string           `json:"sha256"`
	VerifyStatus    string           `json:"verify_status"`
	KeyFingerprint  string           `json:"key_fingerprint"`
	Tables          map[string]int64 `json:"tables,omitempty"`
	Error           string           `json:"error,omitempty"`
}

const manifestName = "manifest.json"

func writeManifest(dir string, man Manifest) error {
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("statebackup: encode manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600); err != nil { //nolint:gosec // manifest 为本包受控产物路径
		return fmt.Errorf("statebackup: write manifest: %w", err)
	}
	return nil
}

// keyFingerprint 计算主密钥文件的 sha256（hex）——恢复时操作员用
// `sha256sum /var/lib/fleetly/fleetly.key` 即可人工核对（runbook 口径），
// 不需要平台工具链。密钥字节本身不进任何产物。
func (m *Manager) keyFingerprint() (string, error) {
	sum, err := fileSHA256(m.keyPath)
	if err != nil {
		return "", err
	}
	return sum, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // 路径来自装配点配置（backup.dir / secrets.key_path），非不可信输入
	if err != nil {
		return "", fmt.Errorf("statebackup: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("statebackup: hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
