// Package backups 是 Backup 域卫星（F2.2，ADR-0039 决策 2/7；B4a manifest
// 换形，ADR-0096 决策 6）：调度（到期库铸台账行——B4a 起按 family
// descriptor 能力位过滤，红队 A9）→ 执行（工具容器 stdout 逐成员流式 Put
// ObjectStore——**manifest 最后写**，全成员成功才落 manifest + 台账
// ObjectKey；失败路径显式删已 Put 成员，红队 A3③）→ 恢复挂起执行
// （manifest 逐件拉取：流式三引擎 / 预置卷 redis）→ 保留滚动（manifest
// 驱动整集删除，行/对象成对）→ List 孤儿对账（manifest 缺失的成员对象
// 宽限窗外登记事件+删除）+ Platform Backup 执行链（restic 快照控制面数据
// 根，ADR-0039 决策 9，platformbackup.go——对 restic 只是文件，两层分立
// 不动）。单写者文化一次执行一件备份；执行带界（BackupTimeout 硬上限
// ——B4a 起语义 = **per-member** 单成员硬顶，整集时长 = 成员数 × 预算，
// 红队 A3②；单写者环卡死即全部时间看门狗失明，ManagedStepTimeout 同源
// 教训）。
//
// P4 wave-1b（ADR-0088 决策 2/3）：Backup 域接收者下域——方法自 *Engine
// 迁至域 struct（方法体内核引用改写为卫星字段/机制端口）。P5 wave-3
// （ADR-0088 决策 2 P5 拆包形态，2026-11-01）：嵌入已解开——卫星独立成
// 包（internal/application/backups），核经构造注入持有（construct.go 装配
// 卫星 New）；gRPC Backup/platform 面与 lifecycle 启动清扫经核门面委托
// （消费点零改）；机制契约共享面（写路径类型/memo 端口/铸名与尾部帮手）
// 在 internal/application/engine/contract。禁回引 *engine.Engine
// （enginebackref 跨包执法）。
package backups

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// Backups 是 Backup 域卫星实例态（P4 wave-1b 嵌入改形 → P5 wave-3 拆包，
// ADR-0088 决策 2/3/4）：状态面唯单飞锁 platformBackupMu（决策 4 随域走
// ——TryLock 入口在 runPlatformBackup，手动触发与节拍环共用）；执行链
// 装配面 objectStore/utility 随卫星持有（rotate 域的 utility 消费与
// componentrestart 的 providers 读经核门面/各自副本）；依赖显式逐字段 +
// 机制端口注入（construct.go 装配）。禁挂 *Engine 回引（enginebackref
// 守卫执法）。接收者名 bk（b 留给域内 backup 行局部变量——防活体遮蔽，
// execs=x 同款裁定）。
type Backups struct {
	// platformBackupMu 是 Platform Backup 的引擎级单飞锁（H6）：快照
	// （VACUUM INTO）与仓外步骤不在 restic 仓锁内——并发触发可产出损坏
	// 但 restic check 通过的恢复点，也会让手动触发误报失败。
	// runPlatformBackup 入口 TryLock，拿不到即 ErrPlatformBackupInProgress
	//（节拍环静默跳过、手动面映射 E_CONFLICT）；不阻塞排队，触发者立即
	// 得到诚实反馈。
	platformBackupMu sync.Mutex

	// 显式依赖（实测消费面）：objectStore 是产物承载端口（对象 Put/Get/
	// Delete——备份/恢复/保留三面；componentrestart 的 providers 读经核
	// 门面 ObjectStore 访问器）；utility 是 Runtime 工具容器子面（装配期
	// 经 FacesOf 探测；rotate 域的凭证轮换消费 databases 域自持副本=
	// FacesOf 单次探测两副本各持）；repos 是只读+行写聚合目录（Backups/
	// Databases/Volumes，transact.Stores 整体注入，ADR-0088 决策 3）；
	// db 是快照面（VACUUM INTO）；tx 是手动触发的事务入口；opts 是域配置
	// 快照（Backup 段 + DataRoot——构造后冻结，与 P4 嵌入期的核 Options
	// 副本同语义）；log/clock 域内日志与节拍。
	objectStore capability.ObjectStore
	utility     capability.RuntimeUtility
	repos       transact.Stores
	db          state.DB
	tx          transact.Tx
	opts        Options
	log         *slog.Logger
	clock       state.Clock

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（utility 附着项目网与
	// 网络重建串行化，ADR-0046；端口名对齐锁入口方法名集——
	// maintenanceguard 判据继续命中；签名零 engine 类型=基础形态直持，
	// 无须命名下沉）；commitWrite 是 writer 端口（签名单源
	// contract.CommitWrite——commit* 前缀名使 enginefuncregistry 判据
	// 零漂移）；emitObservation 是 emitter 端口；
	// projectTeamOf/projectNetworkFactsNamesOnly/controlPlaneNode/
	// ensureDatabaseVolume/databasePassword 是 facts 端口（项目解析面五
	// 族，wave-0 收拢进核 resolve.go——卫星经端口消费、定义位置零改）；
	// eventTeamOfProject 是 emitter 侧 team 解析面；kickDatabases 是
	// preseed 完成后的收敛环唤醒（Loop 挂载留核，闭包注入）。
	lockMaintenance              func() func()
	commitWrite                  contract.CommitWrite
	emitObservation              func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	eventTeamOfProject           func(ctx context.Context, s transact.Stores, projectID string) string
	projectTeamOf                func(projectID string) string
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	controlPlaneNode             func(ctx context.Context) (string, error)
	ensureDatabaseVolume         func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	databasePassword             func(ctx context.Context, row *database.Database) (string, error)
	kickDatabases                func()
	// pubsub 族端口（B4b，ADR-0096 决策 6：backups 卫星跨族单源——凭证
	// 解析面随族分叉）。
	pubsubPassword func(ctx context.Context, row *pubsub.PubSub) (string, error)
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值/闭包）。
type Deps struct {
	ObjectStore capability.ObjectStore
	Utility     capability.RuntimeUtility
	Repos       transact.Stores
	DB          state.DB
	Tx          transact.Tx
	// Options 是域配置快照（Backup 段三字段 + DataRoot；构造后冻结——
	// 与 P4 嵌入期 bk.opts=opts 的核 Options 副本同语义，P5 拆包起卫星
	// 自持本域切片）。
	Options Options
	Log     *slog.Logger
	Clock   state.Clock
	// 机制端口（核方法值/闭包绑定）。
	LockMaintenance              func() func()
	CommitWrite                  contract.CommitWrite
	EmitObservation              func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	EventTeamOfProject           func(ctx context.Context, s transact.Stores, projectID string) string
	ProjectTeamOf                func(projectID string) string
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	ControlPlaneNode             func(ctx context.Context) (string, error)
	EnsureDatabaseVolume         func(ctx context.Context, row *database.Database) (*volume.Volume, error)
	DatabasePassword             func(ctx context.Context, row *database.Database) (string, error)
	KickDatabases                func()
	// PubSubPassword 是 pubsub 族凭证解析端口（B4b）。
	PubSubPassword func(ctx context.Context, row *pubsub.PubSub) (string, error)
}

// New 构造 Backup 域卫星（不启动——环步与启动清扫由 engine 核驱动：
// 环表 step 经卫星方法值 e.backups.Step）。
func New(deps Deps) *Backups {
	bk := &Backups{
		objectStore:                  deps.ObjectStore,
		utility:                      deps.Utility,
		repos:                        deps.Repos,
		db:                           deps.DB,
		tx:                           deps.Tx,
		opts:                         deps.Options,
		log:                          deps.Log,
		clock:                        deps.Clock,
		lockMaintenance:              deps.LockMaintenance,
		commitWrite:                  deps.CommitWrite,
		emitObservation:              deps.EmitObservation,
		eventTeamOfProject:           deps.EventTeamOfProject,
		projectTeamOf:                deps.ProjectTeamOf,
		projectNetworkFactsNamesOnly: deps.ProjectNetworkFactsNamesOnly,
		controlPlaneNode:             deps.ControlPlaneNode,
		ensureDatabaseVolume:         deps.EnsureDatabaseVolume,
		databasePassword:             deps.DatabasePassword,
		kickDatabases:                deps.KickDatabases,
		pubsubPassword:               deps.PubSubPassword,
	}
	return bk
}

// ObjectStore 暴露产物承载端口（engine 核 componentrestart 的 providers
// 读面经核门面委托——单一真源随卫星，P5 wave-3 拆包形态）。
func (bk *Backups) ObjectStore() capability.ObjectStore { return bk.objectStore }

// 失败退避与失败行保留窗（2026-10-05 staging 实录收口）：失败不推进
// last_backup_at 也不退避 + 1s tick = 每秒重铸行重试——33h 累计 11.4 万
// 行台账/事件垃圾，叠加 utility 匿名卷泄漏成 11.6 万枚。退避走最新失败
// 行的 FinishedAt（f6040c4 platform 轨 5min last-attempt 锚同款教训）；
// 手动 TriggerBackup 直铸行不经调度面，不受退避影响。失败行 48h 后由
// 保留滚动同环清行（B4a 起失败路径显式删已 Put 成员，行面仍仅删行；
// 显式清理的漏网残留由 orphanPass 的 List 对账兜底，不经行清扫面）。
const (
	backupFailureBackoff     = 5 * time.Minute
	backupFailedRowRetention = 48 * time.Hour
	// backupOrphanGrace 是 List 孤儿对账的宽限窗（B4a，红队 A3③）：在途
	// 执行「成员已落位、manifest 未收尾」的窗内对象无 manifest 属正常
	// 中间态——ModTime 距今不足窗的对象集整集跳过，防与执行链竞态误删。
	// 1h » 单成员执行面（BackupTimeout 15m per-member + 排队余量）；B4b
	// NATS 多成员大集的逐成员串行总时长若逼近本窗须随批重估。
	backupOrphanGrace = time.Hour
)

// Step 是 Backup 环的单次推进：恢复（用户在等，最高优先）→ 调度 →
// 执行一件 → 保留滚动 → 孤儿对账 → Platform Backup 节拍。导出供核环表经
// 方法值挂载（construct.go 的 e.rings step = e.backups.Step，logpipes/
// metrics 同款）。
func (bk *Backups) Step(ctx context.Context) {
	bk.restorePass(ctx)
	bk.schedulePass(ctx)
	bk.executeOneBackup(ctx)
	bk.prunePass(ctx)
	bk.orphanPass(ctx)
	bk.platformBackupPass(ctx)
}

// platformBackupPass 到点执行 Platform Backup（restic 链；ADR-0039 决策 9）
// ——事件按整体成败落，节拍锚只在全链成功推进。
func (bk *Backups) platformBackupPass(ctx context.Context) {
	cfg := bk.opts.PlatformBackup
	if cfg == nil || !bk.platformBackupDue(*cfg) {
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	if err := bk.runPlatformBackup(execCtx, *cfg); err != nil {
		// 单飞未获（手动触发正在跑）：本拍不是失败事实，安静跳过——不落
		// fail 事件、不误报；下拍到点再试（H6）。
		if errors.Is(err, ErrPlatformBackupInProgress) {
			return
		}
		bk.log.Error("platform backup failed", "err", err)
		// 平台级事件（platform 聚合无 Team 轴）：team 留 '' = 全员可见。
		if err := bk.emitObservation(ctx, "", EventPlatformBackupFail, "platform", "platform-backup",
			platformBackupEventJSON("", err.Error())); err != nil {
			bk.log.Error("platform backup: emit event", "err", err)
		}
		return
	}
	bk.log.Info("platform backup completed", "retention", cfg.Retention.String())
	// 平台级事件（platform 聚合无 Team 轴）：team 留 '' = 全员可见。
	if err := bk.emitObservation(ctx, "", EventPlatformBackupOK, "platform", "platform-backup",
		platformBackupEventJSON(cfg.Retention.String(), "")); err != nil {
		bk.log.Error("platform backup: emit event", "err", err)
	}
}

// backupChainReady 报告执行链装配面（ObjectStore/Utility 缺席 = 备份停用，
// 装配期已日志；环空转零噪音）。
func (bk *Backups) backupChainReady() bool {
	return bk.objectStore != nil && bk.utility != nil
}

// schedulePass 到期库铸台账行（定时缺省 24h；锚 = last_backup_at，从未
// 成功退回 created_at——新库首个节拍 = 创建后一个 interval，手动触发补
// 即时面）。仅 running 库调度（非在服库的备份只有失败噪音）。
//
// B4a 能力位门控（红队 A9，ADR-0096 决策 6）：DataCapabilities.backup
// 过滤——无备份能力的族不铸台账行（否则 kafka 每 24h 铸必败行，「33h
// 11.4 万行」事故重演）。行源本批仍 database 族单源（repos.Databases）；
// 跨族行源（pubsub/bucketstore）随 B4b/B5 各族 descriptor 接入逐族扩，
// 每族同过本门。
func (bk *Backups) schedulePass(ctx context.Context) {
	if !dataservicetemplate.FamilyBacksUp(dataservicetemplate.FamilyDatabase) {
		return // 族级无备份面：零行铸造（受理位 TriggerBackup 同拒）
	}
	rows, err := bk.repos.Databases.List(ctx)
	if err != nil {
		bk.log.Error("backup step: list databases", "err", err)
		return
	}
	now := bk.clock.Now()
	for i := range rows {
		row := &rows[i]
		if row.Status != database.StatusRunning || row.BackupIntervalSecs <= 0 {
			continue
		}
		active, err := bk.repos.Backups.HasActiveFor(ctx, row.ID)
		if err != nil {
			bk.log.Error("backup step: probe active", "database", row.ID, "err", err)
			continue
		}
		if active {
			continue
		}
		// 失败退避窗：最新失败行完结未满窗即不重铸（防 1s tick 重试风暴；
		// 时间面异常的行不挡调度——退避失效比调度停摆便宜）。
		if latest, ok, ferr := bk.repos.Backups.LatestFailedFor(ctx, row.ID); ferr != nil {
			bk.log.Error("backup step: probe last failure", "database", row.ID, "err", ferr)
			continue
		} else if ok {
			if t, terr := time.Parse(time.RFC3339, latest.FinishedAt); terr == nil && now.Sub(t) < backupFailureBackoff {
				continue
			}
		}
		anchor := row.LastBackupAt
		if anchor == "" {
			anchor = row.CreatedAt
		}
		t, err := time.Parse(time.RFC3339, anchor)
		if err != nil {
			bk.log.Error("backup step: parse anchor", "database", row.ID, "err", err)
			continue
		}
		if now.Sub(t) < time.Duration(row.BackupIntervalSecs)*time.Second {
			continue
		}
		b := &backup.Backup{
			ProjectID:     row.ProjectID,
			DatabaseID:    row.ID,
			Engine:        row.Engine,
			EngineVersion: row.Version,
			RetentionSecs: row.BackupRetentionSecs,
		}
		if err := bk.repos.Backups.Create(ctx, b); err != nil {
			bk.log.Error("backup step: schedule row", "database", row.ID, "err", err)
		}
	}
	bk.schedulePubSubs(ctx, now)
}

// executeOneBackup 执行最早一件 pending（单飞；同库双在途由 OldestPending
// 的在途闸兜底）。产物 = manifest 锚定对象集（B4a，ADR-0096 决策 6）：逐
// 成员「工具容器 stdout → io.Pipe → 流式 Put」（sha256 边流边算——Put 回
// 执）→ **manifest 最后写**（全成员成功才落）→ 台账 FinishSucceeded
// （ObjectKey=manifest 键 / Digest=total_digest / SizeBytes=成员合计）。
// 任一成员失败/中断 → 显式删已 Put 成员（幂等容错）→ 台账 failed 无
// ObjectKey（红队 A3③）。
func (bk *Backups) executeOneBackup(ctx context.Context) {
	if !bk.backupChainReady() {
		return
	}
	b, ok, err := bk.repos.Backups.OldestPending(ctx)
	if err != nil || !ok {
		return
	}
	// 维护互斥读半边（ADR-0046）：utility 容器附着项目网（attachable 依赖
	// 面），网络重建窗内排队而非撞不可附着中间态；锁等待不占执行预算
	//（排队语义，BackupTimeout 只量执行本体）。
	unlockMaintenance := bk.lockMaintenance()
	defer unlockMaintenance()
	fail := func(errMsg string) {
		ferr := bk.repos.Backups.FinishFailed(ctx, b.ID, errMsg)
		if errors.Is(ferr, state.ErrConflict) {
			// 迟到失败回执：行已被启动清扫落终态（终态是事实），安静
			// 丢弃——不覆写、不重复发事件。
			bk.log.Warn("backup step: late failure receipt dropped, backup already terminal", "backup", b.ID)
			return
		}
		if ferr != nil {
			bk.log.Error("backup step: record failure", "backup", b.ID, "err", ferr)
		}
		bk.emitBackupEvent(ctx, EventBackupFailed, b, errMsg)
	}
	// 族分支（B4b）：pubsub 族走逐流 object_set 执行面（pubsub.go）；族判别
	// 单源 = 引擎注册表归属（三家族引擎名互斥）。
	if isPubSubEngine(b.Engine) {
		bk.executePubSubBackup(ctx, b, fail)
		return
	}
	db, err := bk.repos.Databases.Get(ctx, b.DatabaseID)
	if err != nil {
		fail(fmt.Sprintf("database row unavailable: %v", err))
		return
	}
	if db.Deleted() {
		fail("database deleted before backup executed")
		return
	}
	// B2c 起按行值绑版（备份工具面跨版本零分叉——绑版是纪律；弃用版本
	// 照常备份——存量全语义续跑，ADR-0094 决策 6）。
	tpl, err := databasetemplate.For(db.Engine, db.Version)
	if err != nil {
		fail(fmt.Sprintf("engine %q left the template registry: %v", db.Engine, err))
		return
	}
	password, err := bk.databasePassword(ctx, db)
	if err != nil {
		fail(err.Error())
		return
	}
	spec, err := tpl.Backup(contract.DatabaseDNSName(db.ID), password)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := bk.repos.Backups.MarkRunning(ctx, b.ID); err != nil {
		bk.log.Error("backup step: mark running", "backup", b.ID, "err", err) // 下拍重取件
		return
	}
	// 对象集前缀一次铸造（时间戳集内同段——成员与 manifest 同
	// <ts>-<id> 段，B4a 键形见 backup/manifest.go）。
	set := backup.KeyMint(db.ProjectID, db.ID, b.ID, bk.clock.Now())
	// 成员清单：单流引擎恒一成员（stdout 单产物，成员名 data）；多流
	// 形态（B4b NATS 逐流 .tgz）在此扩为逐流成员——每成员独立预算/独立
	// Put/独立失败清理（机制面本批即预留：循环体零单成员假设）。
	runs := []memberRun{{
		name:  backup.DefaultMemberName,
		argv:  spec.Argv,
		env:   spec.Env,
		files: spec.SecretFiles,
	}}
	putKeys := make([]string, 0, len(runs)+1) // 已落位键（失败清理面）
	members := make([]backup.ManifestMember, 0, len(runs))
	for i := range runs {
		key, kerr := backup.MemberKeyOf(set, runs[i].name)
		if kerr != nil {
			fail(fmt.Sprintf("backup execution failed: %v", kerr))
			return
		}
		info, rerr := bk.runMemberPut(ctx, b, db, tpl.Image(), runs[i], key)
		if rerr != nil {
			// 失败路径显式清理（红队 A3③）：删已 Put 成员（含本枚——Put
			// 中断的键可能已半落位，Delete 幂等）后落失败台账（无
			// ObjectKey）；残留（清理自身失败）由 orphanPass 对账兜底。
			bk.deleteObjectsQuiet(ctx, append(append([]string(nil), putKeys...), key))
			fail(rerr.Error())
			return
		}
		putKeys = append(putKeys, key)
		members = append(members, backup.ManifestMember{
			Key: key, SHA256: info.Digest, SizeBytes: info.Size,
		})
	}
	// manifest 最后写（全成员成功才落锚——半成集对消费面不可见；台账
	// ObjectKey 此时仍未落，失败即整集回滚）。
	manifest := &backup.Manifest{
		Family:        dataservicetemplate.FamilyDatabase,
		Engine:        db.Engine,
		EngineVersion: db.Version,
		Members:       members,
		TotalDigest:   backup.TotalDigest(members),
		CreatedAt:     state.FormatTime(bk.clock.Now()),
	}
	manifestKey := backup.ManifestKeyOf(set)
	manifestBytes, merr := manifest.Encode()
	if merr != nil {
		bk.deleteObjectsQuiet(ctx, putKeys)
		fail(fmt.Sprintf("backup execution failed: encode manifest: %v", merr))
		return
	}
	if _, perr := bk.objectStore.Put(ctx, manifestKey, bytes.NewReader(manifestBytes)); perr != nil {
		bk.deleteObjectsQuiet(ctx, append(putKeys, manifestKey))
		fail(fmt.Sprintf("backup execution failed: put manifest: %v", perr))
		return
	}
	putKeys = append(putKeys, manifestKey)
	if err := bk.repos.Backups.FinishSucceeded(ctx, b.ID, manifestKey, manifest.TotalDigest, manifest.TotalSize()); err != nil {
		if errors.Is(err, state.ErrConflict) {
			// 迟到成功回执：行已被启动清扫落终态（终态是事实），安静丢弃
			// ——不覆写、不推进锚、不发事件。产物集无人认领（行 failed 无
			// ObjectKey），整集显式回收（manifest 在场不会被 orphanPass 对
			// 账命中，此处是唯一回收点）。
			bk.log.Warn("backup step: late success receipt dropped, backup already terminal", "backup", b.ID)
			bk.deleteObjectsQuiet(ctx, putKeys)
			return
		}
		bk.log.Error("backup step: record success", "backup", b.ID, "err", err)
		return
	}
	if err := bk.repos.Databases.SetLastBackupAt(ctx, db.ID, state.FormatTime(bk.clock.Now())); err != nil {
		bk.log.Error("backup step: advance anchor", "database", db.ID, "err", err)
	}
	succ := *b
	succ.ObjectKey, succ.Digest, succ.SizeBytes = manifestKey, manifest.TotalDigest, manifest.TotalSize()
	bk.emitBackupEvent(ctx, EventBackupSucceeded, &succ, "")
}

// memberRun 是一枚待执行成员的渲染面（B4a：单成员 data；B4b NATS 逐流
// 多成员——每成员独立 argv/材料/预算，机制面见 executeOneBackup 循环体）。
type memberRun struct {
	name  string
	argv  []string
	env   map[string]string
	files map[string][]byte
}

// runMemberPut 执行单成员：工具容器 stdout → io.Pipe → 流式 Put（sha256
// 边流边算——Put 回执即成员回执）。**预算 per-member**（BackupTimeout
// B4a 起语义 = 单成员硬顶，整集时长 = 成员数 × 预算——红队 A3②：大体量
// 整集单预算 = 永久失败循环；单成员族行为与 B4a 前逐位一致）。
func (bk *Backups) runMemberPut(ctx context.Context, b *backup.Backup, db *database.Database, image string, run memberRun, key string) (capability.ObjectInfo, error) {
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	// stdout → io.Pipe → Put（流经纪：Put 在调用方读，工具容器在 goroutine
	// 写；任一侧失败经 Pipe 错误传导到对端）。tee 收 2KB 有界尾部：k8s 侧
	// 工具日志合流走 stdout，失败报文不带尾部对排障失明（restore 面同款
	// 修，e2e 批实证）。
	pr, pw := io.Pipe()
	// 读端生命周期归调用方（H5）：ObjectStore.Put 允许读错/未读尽即返回，
	// 写端会永久阻塞在 Pipe 上——读端必须在函数返回前关闭（Close 幂等，
	// 成功路径已读尽 EOF，不影响字节）。
	defer func() { _ = pr.Close() }()
	stderr := &bytes.Buffer{}
	tail := &boundedTail{}
	runErr := make(chan error, 1)
	go func() {
		err := bk.utility.RunUtility(execCtx, capability.UtilityRequest{
			ID:          b.ID,
			Namespace:   capability.NamespaceRef{Team: bk.projectTeamOf(db.ProjectID), Project: db.ProjectID, Database: db.ID},
			Image:       image,
			Argv:        run.argv,
			Env:         run.env,
			Networks:    bk.projectNetworkFactsNamesOnly(execCtx, db.ProjectID),
			SecretFiles: run.files,
		}, io.MultiWriter(pw, tail), stderr)
		// 收尾必关：成功 = 干净 EOF（Put 读尽完整产物）；失败 = 错误传导
		//（不关则 Put 永远阻塞在读面）。
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		runErr <- err
	}()
	info, putErr := bk.objectStore.Put(execCtx, key, pr)
	if putErr != nil {
		// 未读尽即返回错误（断网/磁盘满/ctx 取消）：先关读端解除写端阻塞，
		// 否则下面等运行侧收尾会互相等（H5 的挂死环）。
		_ = pr.Close()
	}
	// 排空运行侧：ctx 感知的等待（Put 返回后工具容器可能仍在收尾，超时/
	// 取消路径由 execCtx 收口；无界 <-runErr 会把备份环单写者钉死）。
	var rerr error
	select {
	case rerr = <-runErr:
	case <-execCtx.Done():
		_ = pr.Close()
		rerr = execCtx.Err()
	}
	if rerr != nil && putErr == nil {
		putErr = rerr
	}
	if putErr != nil {
		msg := fmt.Sprintf("backup execution failed: %v", putErr)
		if et := contract.StderrTail(stderr.String()); et != "" {
			msg += ": " + et
		} else if s := tail.String(); s != "" {
			msg += ": " + s
		}
		return capability.ObjectInfo{}, errors.New(msg)
	}
	return info, nil
}

// deleteObjectsQuiet 显式删除已落位对象（B4a 失败路径清理面，红队 A3③）。
// Delete 幂等（不存在不报错是 ObjectStore 端口契约）；清理自身失败只记
// 日志不阻塞失败回执落账——漏网残留由 orphanPass 的 List 对账在宽限窗
// 外兜底回收（此时已无 manifest，正中对账射程）。
func (bk *Backups) deleteObjectsQuiet(ctx context.Context, keys []string) {
	for _, key := range keys {
		if err := bk.objectStore.Delete(ctx, key); err != nil {
			bk.log.Error("backup step: cleanup put object", "key", key, "err", err)
		}
	}
}

// VerifyBackup manifest 驱动逐件重算（B4a，ADR-0039 决策 8 静态完整性 +
// ADR-0096 决策 6）：Get manifest → 逐成员 Get 重算 sha256/size 比对清单
// → 合计与台账 total_digest/SizeBytes 对账（腐损/截断/缺件可检出；API
// VerifyBackup 与恢复演练的共用单源）。ok=false 时 errText 携带可诊断差异
// （用户可见文本英文）。
// DownloadBackup 打开备份产物集的只读回传流（IA v3 二期⑤；与 Verify 同源
// manifest 读面——按成员序流式回传，调用方负责 Close）。单成员 = B4a 前
// 现状形状（响应流协议不变——裸字节流）；多成员序接拼接（成员边界框架
// 随 B4b NATS 输入契约同批定形，本批恒单成员）。仅 succeeded 台账行可
// 下载；database_id 是归属校验锚（错配按 not found 处理，不泄漏跨库存在性）。
func (bk *Backups) DownloadBackup(ctx context.Context, databaseID, backupID string) (*backup.Backup, io.ReadCloser, error) {
	// 行校验先行（归属/状态错误优先于环境缺口——错误语义对调用方更有用）。
	b, err := bk.repos.Backups.Get(ctx, backupID)
	if err != nil {
		return nil, nil, err
	}
	if b.DatabaseID != databaseID || b.Status != backup.StatusSucceeded || b.ObjectKey == "" {
		return nil, nil, state.ErrNotFound
	}
	if bk.objectStore == nil {
		return nil, nil, fmt.Errorf("backup: object store is not assembled")
	}
	m, err := bk.loadBackupManifest(ctx, b.ObjectKey)
	if err != nil {
		return nil, nil, err
	}
	readers := make([]io.ReadCloser, 0, len(m.Members))
	for i := range m.Members {
		obj, gerr := bk.objectStore.Get(ctx, m.Members[i].Key)
		if gerr != nil {
			_ = closeAll(readers)
			return nil, nil, fmt.Errorf("read backup member %q: %w", m.Members[i].Key, gerr)
		}
		readers = append(readers, obj)
	}
	return b, &sequentialReadCloser{readers: readers}, nil
}

func (bk *Backups) VerifyBackup(ctx context.Context, backupID string) (bool, string, error) {
	if bk.objectStore == nil {
		return false, "", fmt.Errorf("backup: object store is not assembled")
	}
	b, err := bk.repos.Backups.Get(ctx, backupID)
	if err != nil {
		return false, "", err
	}
	if b.Status != backup.StatusSucceeded || b.ObjectKey == "" {
		return false, "", fmt.Errorf("backup %s is not a succeeded backup with an object", backupID)
	}
	m, err := bk.loadBackupManifest(ctx, b.ObjectKey)
	if err != nil {
		return false, "", err
	}
	var totalSize int64
	for i := range m.Members {
		member := &m.Members[i]
		obj, gerr := bk.objectStore.Get(ctx, member.Key)
		if gerr != nil {
			return false, "", fmt.Errorf("read backup member %q: %w", member.Key, gerr)
		}
		h := sha256.New()
		size, cerr := io.Copy(h, obj)
		_ = obj.Close()
		if cerr != nil {
			return false, "", fmt.Errorf("stream backup member %q: %w", member.Key, cerr)
		}
		digest := hex.EncodeToString(h.Sum(nil))
		if digest != member.SHA256 {
			return false, fmt.Sprintf("member %q digest mismatch: object recomputes to %s, manifest records %s", member.Key, digest, member.SHA256), nil
		}
		if size != member.SizeBytes {
			return false, fmt.Sprintf("member %q size mismatch: object reads %d bytes, manifest records %d", member.Key, size, member.SizeBytes), nil
		}
		totalSize += size
	}
	switch {
	case m.TotalDigest != b.Digest:
		return false, fmt.Sprintf("total digest mismatch: manifest records %s, ledger records %s", m.TotalDigest, b.Digest), nil
	case totalSize != b.SizeBytes:
		return false, fmt.Sprintf("total size mismatch: members sum to %d bytes, ledger records %d", totalSize, b.SizeBytes), nil
	default:
		return true, "", nil
	}
}

// loadBackupManifest 取 manifest 对象并解析校验（verify/download/restore/
// prune 共用的 manifest 驱动单源）。完整性闸：manifest 全部成员必须住在
// manifest 键自身的集内（backup.ParseManifest 的形状闸 + WithinSet 的
// containment 闸——防篡改 manifest 把消费面指向集外任意键）。
func (bk *Backups) loadBackupManifest(ctx context.Context, manifestKey string) (*backup.Manifest, error) {
	obj, err := bk.objectStore.Get(ctx, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("read backup manifest %q: %w", manifestKey, err)
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("stream backup manifest %q: %w", manifestKey, err)
	}
	m, err := backup.ParseManifest(data)
	if err != nil {
		return nil, fmt.Errorf("parse backup manifest %q: %w", manifestKey, err)
	}
	if set, _, ok := backup.SplitSetKey(manifestKey); !ok || !m.WithinSet(set) {
		return nil, fmt.Errorf("backup manifest %q carries member keys outside its own object set", manifestKey)
	}
	return m, nil
}

// sequentialReadCloser 是成员序拼接读流（Download 的多成员回传面：读尽
// 当前成员接下一成员；Close 关全部已开句柄——任一成员半读即断流时也整
// 批收口，零句柄泄漏）。
type sequentialReadCloser struct {
	readers []io.ReadCloser
	// cur 是当前在读成员下标（读尽推进；越界 = EOF）。
	cur int
}

func (s *sequentialReadCloser) Read(p []byte) (int, error) {
	for s.cur < len(s.readers) {
		n, err := s.readers[s.cur].Read(p)
		if n > 0 {
			return n, nil
		}
		if errors.Is(err, io.EOF) {
			s.cur++
			continue // 成员读尽：接下一成员（空成员自然跳过）
		}
		if err != nil {
			return 0, err
		}
		// n==0 && err==nil：零字节读不推进（防死循环——交给上层重试）。
		return 0, nil
	}
	return 0, io.EOF
}

func (s *sequentialReadCloser) Close() error { return closeAll(s.readers) }

// closeAll 关闭全部已开句柄（聚合首错；下载中途失败/取消的收口面）。
func closeAll(readers []io.ReadCloser) error {
	var first error
	for _, r := range readers {
		if err := r.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// boundedTail 是 2KB 有界尾部捕获写者（backup 的 stdout 是产物流不可截
// 留——tee 侧仅留尾部，失败报文面专用）。
type boundedTail struct {
	buf []byte
}

func (t *boundedTail) Write(p []byte) (int, error) {
	const max = 2048
	t.buf = append(t.buf, p...)
	if len(t.buf) > max {
		t.buf = t.buf[len(t.buf)-max:]
	}
	return len(p), nil
}

func (t *boundedTail) String() string { return contract.TrimTrailing(string(t.buf)) }

// restorePass 执行恢复挂起（ADR-0039 决策 6/8）：流式形态（pg/mysql/
// mongo——目标库 running 后 stdin 注入）与预置卷形态（redis——载体首启
// 前把 RDB 落进数据卷，databaseLoop 对挂起中的 preseed 库跳过 Ensure）。
func (bk *Backups) restorePass(ctx context.Context) {
	if !bk.backupChainReady() {
		return
	}
	rows, err := bk.repos.Databases.ListRestorePending(ctx)
	if err != nil {
		bk.log.Error("restore pass: list pending", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		bk.restoreDatabase(ctx, &rows[i])
	}
	// pubsub 族恢复挂起（B4b：manifest 逐成员逐流注入，pubsub.go）。
	pubRows, err := bk.repos.PubSubs.ListRestorePending(ctx)
	if err != nil {
		bk.log.Error("restore pass: list pending pubsubs", "err", err)
		return
	}
	for i := range pubRows {
		if ctx.Err() != nil {
			return
		}
		bk.restorePubSub(ctx, &pubRows[i])
	}
}

func (bk *Backups) restoreDatabase(ctx context.Context, db *database.Database) {
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if err := bk.repos.Databases.SetRestoreError(ctx, db.ID, msg); err != nil {
			bk.log.Error("restore: record failure", "database", db.ID, "err", err)
		}
		bk.log.Error("restore failed", "database", db.ID, "err", msg)
	}
	src, err := bk.repos.Backups.Get(ctx, db.RestoreFromBackup)
	if err != nil {
		fail("source backup unavailable: %v", err)
		return
	}
	if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
		fail("source backup %s is not a succeeded backup with an object", src.ID)
		return
	}
	if src.Engine != db.Engine {
		fail("source backup engine %q does not match target engine %q", src.Engine, db.Engine)
		return
	}
	// B2c 起按行值绑版（受理位已做 RestoreCompat 版本轴检查——ADR-0094
	// 决策 5；此处是执行面按目标行版本绑版渲染恢复工具）。
	tpl, err := databasetemplate.For(db.Engine, db.Version)
	if err != nil {
		fail("engine %q left the template registry: %v", db.Engine, err)
		return
	}
	password, err := bk.databasePassword(ctx, db)
	if err != nil {
		fail("%v", err)
		return
	}
	spec, err := tpl.Restore(contract.DatabaseDNSName(db.ID), password)
	if err != nil {
		fail("%v", err)
		return
	}
	// 维护互斥读半边（ADR-0046）：同 executeOneBackup——utility 附着项目网
	// 与网络重建串行化；锁等待不占执行预算（排队语义）。
	unlockMaintenance := bk.lockMaintenance()
	defer unlockMaintenance()
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	// manifest 逐件拉取（B4a，ADR-0096 决策 6）：单成员 = B4a 前现状行为
	//（恢复输入面不动——stream stdin 文件挂载 / preseed 落卷都是单流；
	// 多成员恢复输入契约 B4b 随 NATS 定形，本批诚实拒）。
	srcManifest, err := bk.loadBackupManifest(execCtx, src.ObjectKey)
	if err != nil {
		fail("%v", err)
		return
	}
	if len(srcManifest.Members) != 1 {
		fail("source backup %s carries %d members; multi-member restore input lands with the pubsub family (B4b)", src.ID, len(srcManifest.Members))
		return
	}
	object, err := bk.objectStore.Get(execCtx, srcManifest.Members[0].Key)
	if err != nil {
		fail("read backup member %q: %v", srcManifest.Members[0].Key, err)
		return
	}
	defer func() { _ = object.Close() }()
	stderr := &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID:          "restore-" + db.ID,
		Namespace:   capability.NamespaceRef{Team: bk.projectTeamOf(db.ProjectID), Project: db.ProjectID, Database: db.ID},
		Image:       tpl.Image(),
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    bk.projectNetworkFactsNamesOnly(execCtx, db.ProjectID),
		SecretFiles: spec.SecretFiles,
		// 恢复流经文件挂载（BackupInputPath 单源——hijack stdin EOF 不可
		// 达的 dind 实证，ADR-0039 落地实录）。
		Input: &capability.UtilityInput{Content: object, Target: databasetemplate.BackupInputPath},
	}
	switch spec.Mode {
	case databasetemplate.RestoreStream:
		if db.Status != database.StatusRunning {
			return // 目标库未在服：等下一拍（收敛环推进 status 后再来）
		}
	case databasetemplate.RestorePreseed:
		if db.Generation != 0 {
			fail("cannot preseed a database that has already started; delete and re-create it, then restore again")
			return
		}
		vol, err := bk.prepareSeedVolume(execCtx, db)
		if err != nil {
			fail("%v", err)
			return
		}
		// VolumeID 与 Workload 投影同锚（卷行 id——ADR-0097 起 Provider 卷
		// 载体名解析的唯一标识面；databases 投影的 Volumes[0].VolumeID 同款
		// 事实）。
		req.Volume = &capability.UtilityVolumeMount{VolumeID: vol.ID, Target: databasetemplate.SeedMountPoint}
	default:
		fail("engine %q declares unknown restore mode %q", db.Engine, spec.Mode)
		return
	}
	// 工具输出捕获双通道：k3s 的 Utility 日志合流走 stdout（stderr 恒空），
	// 只收 stderr 的报文面对该形态失明（restore exit 1 无真相，e2e 取证
	// 实证）——失败报文带上 stdout 尾部（恢复流不产 stdout 产物，无混流面）。
	stdout := &bytes.Buffer{}
	if err := bk.utility.RunUtility(execCtx, req, stdout, stderr); err != nil {
		msg := err.Error()
		if tail := contract.StderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		if tail := contract.StderrTail(stdout.String()); tail != "" {
			msg += ": " + tail
		}
		fail("restore execution failed: %s", msg)
		return
	}
	if err := bk.repos.Databases.ClearRestoreSucceeded(ctx, db.ID); err != nil {
		bk.log.Error("restore: clear pending", "database", db.ID, "err", err)
		return
	}
	bk.kickDatabases() // preseed 完成 → 立即收敛首启（装载预置数据）
	if err := bk.emitObservation(ctx, bk.eventTeamOfProject(ctx, bk.repos, db.ProjectID),
		EventDatabaseRestored, "database", db.ID,
		databaseRestoredEventJSON(db, src)); err != nil {
		bk.log.Error("restore: emit event", "database", db.ID, "err", err)
	}
}

// prepareSeedVolume 保证挂靠卷行在场并钉住控制面节点（工具容器恒在控制
// 面 daemon 跑——预置落点与库首启调度点必须同节点，swarm 无卷感知调度，
// 平台钉住是既有唯一机制）。已钉到别处的卷 = 预置会落在错误节点，诚实
// 拒绝（用户先解除钉住或换名重建）。
func (bk *Backups) prepareSeedVolume(ctx context.Context, db *database.Database) (*volume.Volume, error) {
	vol, err := bk.ensureDatabaseVolume(ctx, db)
	if err != nil {
		return nil, err
	}
	managerID, err := bk.controlPlaneNode(ctx)
	if err != nil {
		return nil, err
	}
	if vol.PinnedNodeID == "" {
		if err := bk.repos.Volumes.Pin(ctx, db.ProjectID, vol.Name, managerID); err != nil {
			return nil, err
		}
	} else if vol.PinnedNodeID != managerID {
		return nil, fmt.Errorf("volume %q is pinned to node %s; restore pre-seeding requires the control-plane node %s", vol.Name, vol.PinnedNodeID, managerID)
	}
	return vol, nil
}

// prunePass 保留滚动（行驱动：created_at + 行上 retention 快照；**manifest
// 驱动整集删除**——manifest + 全成员 + 行成对，B4a，ADR-0096 决策 6）。
// 形态 = 装配（读台账前沿）→ 域裁决（backup.PruneSelection 选删集，
// ADR-0087 决策 7 归域）→ 执行（整集对象+行成对删）。
func (bk *Backups) prunePass(ctx context.Context) {
	// 装配：created_at 升序限量前沿（升序保证到期行必在前沿——限量分拍的
	// 前提，见 repo OldestByCreated）。
	rows, err := bk.repos.Backups.OldestByCreated(ctx, 100)
	if err != nil {
		bk.log.Error("prune pass: read ledger front", "err", err)
		return
	}
	// 域裁决：保留窗判定（时间作参数传入；无状态窗谓词——在途行老于行级
	// 窗理论上可达，R05-L6 登记在案随备份链完整性批另修）。
	due := backup.PruneSelection(rows, bk.clock.Now())
	for i := range due {
		b := &due[i]
		if b.ObjectKey != "" {
			if err := bk.deleteBackupSet(ctx, b.ObjectKey); err != nil {
				bk.log.Error("prune pass: delete object set", "key", b.ObjectKey, "err", err)
				continue // 对象删除失败保行（下拍重试，行/对象不拆对）
			}
		}
		if err := bk.repos.Backups.Delete(ctx, b.ID); err != nil {
			bk.log.Error("prune pass: delete row", "backup", b.ID, "err", err)
		}
	}
	// 失败行保留清扫（同环收尾；失败行无锚定产物——B4a 起失败路径显式删
	// 已 Put 成员，行面仅删行，重试风暴的台账垃圾由此出清，48h 窗见
	// backupFailureBackoff 注释；显式清理的漏网残留归 orphanPass）。
	stale, err := bk.repos.Backups.DueForFailedRowSweep(ctx, bk.clock.Now(), backupFailedRowRetention, 500)
	if err != nil {
		bk.log.Error("prune pass: stale failed rows", "err", err)
		return
	}
	for i := range stale {
		if err := bk.repos.Backups.Delete(ctx, stale[i].ID); err != nil {
			bk.log.Error("prune pass: delete failed row", "backup", stale[i].ID, "err", err)
			return
		}
	}
}

// deleteBackupSet 整集删除（manifest 驱动：manifest + 全成员；manifest 锚
// 最后摘——中途断在成员面上时残余成员已无 manifest，正入 orphanPass 对账
// 射程）。manifest 读取/解析失败 = legacy 单对象键（B4a 前台账行）或
// manifest 已失踪的半删态：退回直删 ObjectKey（幂等）——legacy 行的
// 行/对象成对律由此保持（旧键不迁移不承诺，ADR-0096 决策 6）。
func (bk *Backups) deleteBackupSet(ctx context.Context, objectKey string) error {
	m, err := bk.loadBackupManifest(ctx, objectKey)
	if err != nil {
		return bk.objectStore.Delete(ctx, objectKey)
	}
	for _, key := range m.MemberKeys() {
		if err := bk.objectStore.Delete(ctx, key); err != nil {
			return err
		}
	}
	return bk.objectStore.Delete(ctx, objectKey)
}

// orphanPass 是 List 孤儿对账（B4a，红队 A3③；**List 消费者首次入
// application 层**——此前 List 只在保留窗排序设想与 Provider 自测）：
// 扫 backups/ 前缀，manifest 缺失的对象集成员（部分成功残留——执行链
// 失败显式清理自身的漏网；历史半删态——整集删除中断在 manifest 前）登记
// 事件 + 删除。宽限窗（backupOrphanGrace）外才动手：在途执行「成员已
// 落位、manifest 未收尾」是正常中间态，ModTime 不足窗的集整集跳过。
//
// 射程注记：legacy 单对象键（B4a 前形态，恰四段无集）不在射程——台账行
// 仍指向它们，保留滚动按行/对象成对律出清；staging 旧键孤儿化由 manifest
// flag-day 重建窗承载（design §7.6）。manifest 在场而成员缺席/腐损是对账
// 不出的形态（verify 面诚实报差异，不自动删——锚定的集是用户数据）。
func (bk *Backups) orphanPass(ctx context.Context) {
	if bk.objectStore == nil {
		return
	}
	objs, err := bk.objectStore.List(ctx, backup.RootPrefix)
	if err != nil {
		bk.log.Error("orphan pass: list backup objects", "err", err)
		return
	}
	// 集分类：set → 成员面（manifest 在场标记 + 成员对象集）。
	type objectSet struct {
		hasManifest bool
		members     []capability.ObjectInfo
	}
	sets := map[string]*objectSet{}
	for _, o := range objs {
		set, name, ok := backup.SplitSetKey(o.Key)
		if !ok {
			continue // 非集形态（legacy 单对象键/异形键）：不在对账射程
		}
		s := sets[set]
		if s == nil {
			s = &objectSet{}
			sets[set] = s
		}
		if name == backup.ManifestObjectName {
			s.hasManifest = true
			continue
		}
		s.members = append(s.members, o)
	}
	// 确定性序（集字典序——事件/删除序稳定，golden 可钉）。
	setKeys := make([]string, 0, len(sets))
	for set := range sets {
		setKeys = append(setKeys, set)
	}
	sort.Strings(setKeys)
	now := bk.clock.Now()
	for _, set := range setKeys {
		s := sets[set]
		if s.hasManifest || len(s.members) == 0 {
			continue
		}
		// 宽限窗：任一成员 ModTime 不严格出窗（或无信号）即整集等待——
		// 在途执行竞态的防误删面；边界 = 严格窗外（恰满窗仍留，PruneDue
		// 同款多留不少留方向）；ModTime 缺席 = Provider 违约形态，诚实跳过
		// 等下拍。
		aged := true
		for i := range s.members {
			if s.members[i].ModTime.IsZero() || now.Sub(s.members[i].ModTime) <= backupOrphanGrace {
				aged = false
				break
			}
		}
		if !aged {
			continue
		}
		keys := make([]string, 0, len(s.members))
		for i := range s.members {
			keys = append(keys, s.members[i].Key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := bk.objectStore.Delete(ctx, key); err != nil {
				bk.log.Error("orphan pass: delete object", "key", key, "err", err)
				continue // 幂等重试面：下拍 List 重扫（集仍无 manifest 即再命中）
			}
		}
		bk.log.Warn("orphan pass: swept manifest-less backup objects", "set", set, "objects", len(keys))
		// 平台级对账事实（聚合轴 database；实例可能已删——team 解析面不可
		// 依赖，留 '' 全员可见，platform.backup_* 同口径）。id 轴取集前缀的
		// databaseID 段（解析失败 = 异形集键，id 留空）。
		aggregateID := ""
		if parts := strings.Split(set, "/"); len(parts) == 4 {
			aggregateID = parts[2]
		}
		if err := bk.emitObservation(ctx, "", EventBackupOrphanSwept, "database", aggregateID,
			orphanSweptEventJSON(keys)); err != nil {
			bk.log.Error("orphan pass: emit event", "set", set, "err", err)
		}
	}
}

// emitBackupEvent 落备份事件（成功/失败统一形态）。资源轴 Team 经
// Database → Project 链解析（行缺失/存储故障 → ”，按域 A 化补齐）；
// 解析面与发射面经 emitter 端口。
func (bk *Backups) emitBackupEvent(ctx context.Context, name string, b *backup.Backup, errMsg string) {
	team, aggregate := "", "database"
	if isPubSubEngine(b.Engine) {
		// pubsub 族（B4b）：聚合轴 pubsub，team 经 pubsub 行解析。
		aggregate = "pubsub"
		if row, err := bk.repos.PubSubs.Get(ctx, b.DatabaseID); err == nil {
			team = bk.eventTeamOfProject(ctx, bk.repos, row.ProjectID)
		} else {
			bk.log.Warn("events: resolve pubsub team", "pubsub", b.DatabaseID, "err", err)
		}
	} else if db, err := bk.repos.Databases.Get(ctx, b.DatabaseID); err == nil {
		team = bk.eventTeamOfProject(ctx, bk.repos, db.ProjectID)
	} else {
		bk.log.Warn("events: resolve database team", "database", b.DatabaseID, "err", err)
	}
	if err := bk.emitObservation(ctx, team, name, aggregate, b.DatabaseID,
		backupEventPayloadJSON(b, errMsg)); err != nil {
		bk.log.Error("backup: emit event", "backup", b.ID, "err", err)
	}
}

// SweepInterrupted 是启动清扫（重启打断的 running 行落 failed——
// resetOrphanBuilds 先例）。导出供核 lifecycle.Start 经卫星指针调用
// （P5 wave-3 拆包形态，消费点零改）。
func (bk *Backups) SweepInterrupted(ctx context.Context) {
	n, err := bk.repos.Backups.SweepInterrupted(ctx)
	if err != nil {
		bk.log.Error("backup: sweep interrupted rows", "err", err)
		return
	}
	if n > 0 {
		bk.log.Warn("backup: interrupted rows marked failed after restart", "count", n)
	}
}

// ---- 事件名与载荷（P5 wave-3 随域拆包自 engine events.go 迁入；核侧经
// 别名消费——schemareg 注册面与 eventcode 注册表同源） ----

// Backup 事件名锚定（F2.2，ADR-0039；B4a 扩 manifest 面；usage 反扫的字面
// 量命中点）。
const (
	// EventBackupSucceeded 是单库备份成功（engine 备份环发射；载荷回执
	// B4a 起 = manifest 锚三元组：object_key=manifest 键 / digest=
	// total_digest / size_bytes=成员合计）。
	EventBackupSucceeded = "database.backup_succeeded"
	// EventBackupFailed 是单库备份失败（engine 备份环发射）。
	EventBackupFailed = "database.backup_failed"
	// EventBackupOrphanSwept 是孤儿对账回收（B4a，红队 A3③：List 对账
	// 删除 manifest 缺失的成员对象——部分成功残留/历史半删态，宽限窗外）。
	EventBackupOrphanSwept = "database.backup_orphan_swept"
	// EventDatabaseRestored 是恢复完成（engine 恢复面发射；失败走行
	// restore_error）。
	EventDatabaseRestored = "database.restored"
	// EventPlatformBackupOK 是平台备份全链成功（restic 双仓）。
	EventPlatformBackupOK = "platform.backup_succeeded"
	// EventPlatformBackupFail 是平台备份失败（restic 错误尾随载荷）。
	EventPlatformBackupFail = "platform.backup_failed"
)

// BackupEventPayload 是 backup.*/database.restored 载荷 schema（F2.2，
// ADR-0039；字段只增）。统一形态：成功携带 ObjectStore 回执三元组
// （B4a 起 manifest 锚语义：key/digest/size 见 EventBackupSucceeded 注），
// 失败携带 error（用户可见文本英文）。
type BackupEventPayload struct {
	BackupID   string `json:"backup_id"`
	DatabaseID string `json:"database_id"`
	ProjectID  string `json:"project_id"`
	Engine     string `json:"engine"`
	ObjectKey  string `json:"object_key,omitempty"`
	Digest     string `json:"digest,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

// BackupOrphanSweptEventPayload 是 database.backup_orphan_swept 载荷
// schema（B4a：被回收的对象键清单——对象集前缀在事件 id 轴，键清单是
// 完整事实面）。
type BackupOrphanSweptEventPayload struct {
	Keys []string `json:"keys"`
}

// DatabaseRestoredEventPayload 是 database.restored 载荷 schema（恢复完成
// 事实：目标库 + 源备份）。
type DatabaseRestoredEventPayload struct {
	DatabaseID string `json:"database_id"`
	ProjectID  string `json:"project_id"`
	FromBackup string `json:"from_backup"`
	ObjectKey  string `json:"object_key,omitempty"`
}

// PlatformBackupEventPayload 是 platform.backup_* 载荷 schema（restic 链
// 整体成败；快照细节在仓库自身——事件只携带节拍事实）。
type PlatformBackupEventPayload struct {
	Retention string `json:"retention,omitempty"`
	Error     string `json:"error,omitempty"`
}

// backupEventPayloadJSON 序列化备份事件载荷（纯标量结构，Marshal 不
// 失败；失败即编程错误）。
func backupEventPayloadJSON(b *backup.Backup, errMsg string) []byte {
	out, _ := json.Marshal(BackupEventPayload{
		BackupID:   b.ID,
		DatabaseID: b.DatabaseID,
		ProjectID:  b.ProjectID,
		Engine:     b.Engine,
		ObjectKey:  b.ObjectKey,
		Digest:     b.Digest,
		SizeBytes:  b.SizeBytes,
		Error:      errMsg,
	})
	return out
}

// databaseRestoredEventJSON 序列化恢复完成事件载荷（engine events.go 随域
// 迁入，逐字零漂移）。
func databaseRestoredEventJSON(db *database.Database, src *backup.Backup) []byte {
	out, _ := json.Marshal(DatabaseRestoredEventPayload{
		DatabaseID: db.ID,
		ProjectID:  db.ProjectID,
		FromBackup: src.ID,
		ObjectKey:  src.ObjectKey,
	})
	return out
}

// orphanSweptEventJSON 序列化孤儿对账回收事件载荷（纯清单结构，Marshal
// 不失败；失败即编程错误）。
func orphanSweptEventJSON(keys []string) []byte {
	out, _ := json.Marshal(BackupOrphanSweptEventPayload{Keys: keys})
	return out
}

// platformBackupEventJSON 序列化平台备份事件载荷（同上随域迁入）。
func platformBackupEventJSON(retention, errMsg string) []byte {
	out, _ := json.Marshal(PlatformBackupEventPayload{
		Retention: retention,
		Error:     errMsg,
	})
	return out
}
