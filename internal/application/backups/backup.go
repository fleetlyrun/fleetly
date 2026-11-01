// Package backups 是 Backup 域卫星（F2.2，ADR-0039 决策 2/7）：调度（到期
// 库铸台账行）→ 执行（工具容器 stdout 流式 Put ObjectStore）→ 恢复挂起
// 执行（流式三引擎 / 预置卷 redis）→ 保留滚动（行驱动，行/对象成对删）
// + Platform Backup 执行链（restic 快照控制面数据根，ADR-0039 决策 9，
// platformbackup.go）。单写者文化一次执行一件备份；执行带界
// （BackupTimeout 硬上限——单写者环卡死即全部时间看门狗失明，
// ManagedStepTimeout 同源教训）。
//
// P4 wave-1b（ADR-0088 决策 2/3）：Backup 域接收者下域——方法自 *Engine
// 迁至域 struct（方法体内核引用改写为卫星字段/机制端口）。P5 wave-3
// （ADR-0088 决策 2 P5 拆包形态，2026-11-01）：嵌入已解开——卫星独立成
// 包（internal/application/backups），核经构造注入持有（construct.go 装配
// 卫星 New）；gRPC Backup/platform 面与 lifecycle 启动清扫经核门面委托
// （消费点零改）；机制契约共享面（写路径类型/memo 端口/铸名与尾部帮手）
// 在 internal/application/engineports。禁回引 *engine.Engine
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
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
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
	// engineports.CommitWrite——commit* 前缀名使 enginefuncregistry 判据
	// 零漂移）；emitObservation 是 emitter 端口；
	// projectTeamOf/projectNetworkFactsNamesOnly/controlPlaneNode/
	// ensureDatabaseVolume/databasePassword 是 facts 端口（项目解析面五
	// 族，wave-0 收拢进核 resolve.go——卫星经端口消费、定义位置零改）；
	// eventTeamOfProject 是 emitter 侧 team 解析面；kickDatabases 是
	// preseed 完成后的收敛环唤醒（Loop 挂载留核，闭包注入）。
	lockMaintenance              func() func()
	commitWrite                  engineports.CommitWrite
	emitObservation              func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	eventTeamOfProject           func(ctx context.Context, s transact.Stores, projectID string) string
	projectTeamOf                func(projectID string) string
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	controlPlaneNode             func(ctx context.Context) (string, error)
	ensureDatabaseVolume         func(ctx context.Context, row *database.Database) error
	databasePassword             func(ctx context.Context, row *database.Database) (string, error)
	kickDatabases                func()
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
	CommitWrite                  engineports.CommitWrite
	EmitObservation              func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	EventTeamOfProject           func(ctx context.Context, s transact.Stores, projectID string) string
	ProjectTeamOf                func(projectID string) string
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	ControlPlaneNode             func(ctx context.Context) (string, error)
	EnsureDatabaseVolume         func(ctx context.Context, row *database.Database) error
	DatabasePassword             func(ctx context.Context, row *database.Database) (string, error)
	KickDatabases                func()
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
// 保留滚动同环清行（无对象产物，仅删行）。
const (
	backupFailureBackoff     = 5 * time.Minute
	backupFailedRowRetention = 48 * time.Hour
)

// Step 是 Backup 环的单次推进：恢复（用户在等，最高优先）→ 调度 →
// 执行一件 → 保留滚动 → Platform Backup 节拍。导出供核环表经方法值挂载
// （construct.go 的 e.rings step = e.backups.Step，logpipes/metrics 同款）。
func (bk *Backups) Step(ctx context.Context) {
	bk.restorePass(ctx)
	bk.schedulePass(ctx)
	bk.executeOneBackup(ctx)
	bk.prunePass(ctx)
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
func (bk *Backups) schedulePass(ctx context.Context) {
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
			RetentionSecs: row.BackupRetentionSecs,
		}
		if err := bk.repos.Backups.Create(ctx, b); err != nil {
			bk.log.Error("backup step: schedule row", "database", row.ID, "err", err)
		}
	}
}

// executeOneBackup 执行最早一件 pending（单飞；同库双在途由 OldestPending
// 的在途闸兜底）。产物 = 工具容器 stdout 流式 Put（digest 由 Put 铸造，
// restore verify 校验锚）。
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
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
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
	db, err := bk.repos.Databases.Get(ctx, b.DatabaseID)
	if err != nil {
		fail(fmt.Sprintf("database row unavailable: %v", err))
		return
	}
	if db.Deleted() {
		fail("database deleted before backup executed")
		return
	}
	tpl, ok := dbtemplate.For(db.Engine)
	if !ok {
		fail(fmt.Sprintf("engine %q left the template registry", db.Engine))
		return
	}
	password, err := bk.databasePassword(execCtx, db)
	if err != nil {
		fail(err.Error())
		return
	}
	spec, err := tpl.Backup(engineports.DatabaseDNSName(db.ID), password)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := bk.repos.Backups.MarkRunning(ctx, b.ID); err != nil {
		bk.log.Error("backup step: mark running", "backup", b.ID, "err", err) // 下拍重取件
		return
	}
	key := backup.KeyMint(db.ProjectID, db.ID, b.ID, bk.clock.Now())
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
			Image:       tpl.Image(),
			Argv:        spec.Argv,
			Env:         spec.Env,
			Networks:    bk.projectNetworkFactsNamesOnly(execCtx, db.ProjectID),
			SecretFiles: spec.SecretFiles,
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
		if et := engineports.StderrTail(stderr.String()); et != "" {
			msg += ": " + et
		} else if s := tail.String(); s != "" {
			msg += ": " + s
		}
		fail(msg)
		return
	}
	if err := bk.repos.Backups.FinishSucceeded(ctx, b.ID, key, info.Digest, info.Size); err != nil {
		if errors.Is(err, state.ErrConflict) {
			// 迟到成功回执：行已被启动清扫落终态（终态是事实），安静
			// 丢弃——不覆写、不推进锚、不发事件。
			bk.log.Warn("backup step: late success receipt dropped, backup already terminal", "backup", b.ID)
			return
		}
		bk.log.Error("backup step: record success", "backup", b.ID, "err", err)
		return
	}
	if err := bk.repos.Databases.SetLastBackupAt(ctx, db.ID, state.FormatTime(bk.clock.Now())); err != nil {
		bk.log.Error("backup step: advance anchor", "database", db.ID, "err", err)
	}
	succ := *b
	succ.ObjectKey, succ.Digest, succ.SizeBytes = key, info.Digest, info.Size
	bk.emitBackupEvent(ctx, EventBackupSucceeded, &succ, "")
}

// VerifyBackup 重算对象 sha256 比对 Put 回执（ADR-0039 决策 8：静态完整
// 性——腐损/截断可检出；API VerifyBackup 与恢复演练的共用单源）。ok=false
// 时 errText 携带可诊断差异（用户可见文本英文）。
// DownloadBackup 打开备份对象的只读流（IA v3 二期⑤；与 Verify 同源存储
// 读面——调用方负责 Close）。仅 succeeded 台账行可下载；database_id 是
// 归属校验锚（错配按 not found 处理，不泄漏跨库存在性）。
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
	obj, err := bk.objectStore.Get(ctx, b.ObjectKey)
	if err != nil {
		return nil, nil, fmt.Errorf("read backup object %q: %w", b.ObjectKey, err)
	}
	return b, obj, nil
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
	obj, err := bk.objectStore.Get(ctx, b.ObjectKey)
	if err != nil {
		return false, "", fmt.Errorf("read backup object %q: %w", b.ObjectKey, err)
	}
	defer func() { _ = obj.Close() }()
	h := sha256.New()
	size, err := io.Copy(h, obj)
	if err != nil {
		return false, "", fmt.Errorf("stream backup object %q: %w", b.ObjectKey, err)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	switch {
	case digest != b.Digest:
		return false, fmt.Sprintf("digest mismatch: object recomputes to %s, ledger records %s", digest, b.Digest), nil
	case size != b.SizeBytes:
		return false, fmt.Sprintf("size mismatch: object reads %d bytes, ledger records %d", size, b.SizeBytes), nil
	default:
		return true, "", nil
	}
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

func (t *boundedTail) String() string { return engineports.TrimTrailing(string(t.buf)) }

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
	tpl, ok := dbtemplate.For(db.Engine)
	if !ok {
		fail("engine %q left the template registry", db.Engine)
		return
	}
	password, err := bk.databasePassword(ctx, db)
	if err != nil {
		fail("%v", err)
		return
	}
	spec, err := tpl.Restore(engineports.DatabaseDNSName(db.ID), password)
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
	object, err := bk.objectStore.Get(execCtx, src.ObjectKey)
	if err != nil {
		fail("read backup object %q: %v", src.ObjectKey, err)
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
		Input: &capability.UtilityInput{Content: object, Target: dbtemplate.BackupInputPath},
	}
	switch spec.Mode {
	case dbtemplate.RestoreStream:
		if db.Status != database.StatusRunning {
			return // 目标库未在服：等下一拍（收敛环推进 status 后再来）
		}
	case dbtemplate.RestorePreseed:
		if db.Generation != 0 {
			fail("cannot preseed a database that has already started; delete and re-create it, then restore again")
			return
		}
		vol, err := bk.prepareSeedVolume(execCtx, db)
		if err != nil {
			fail("%v", err)
			return
		}
		// VolumeID 与 Workload 投影同锚（平台卷名——Provider 卷载体名解析
		// 的唯一标识面；database_test 的 Volumes[0].VolumeID 同款事实）。
		req.Volume = &capability.UtilityVolumeMount{VolumeID: vol.Name, Target: dbtemplate.SeedMountPoint}
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
		if tail := engineports.StderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		if tail := engineports.StderrTail(stdout.String()); tail != "" {
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
	if err := bk.ensureDatabaseVolume(ctx, db); err != nil {
		return nil, err
	}
	vol, err := bk.repos.Volumes.GetByName(ctx, db.ProjectID, db.Name)
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

// prunePass 保留滚动（行驱动：created_at + 行上 retention 快照；行/对象
// 成对删，单键迟到缺失不阻断——Delete 幂等口径）。形态 = 装配（读台账
// 前沿）→ 域裁决（backup.PruneSelection 选删集，ADR-0087 决策 7 归域）
// → 执行（对象+行成对删）。
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
			if err := bk.objectStore.Delete(ctx, b.ObjectKey); err != nil {
				bk.log.Error("prune pass: delete object", "key", b.ObjectKey, "err", err)
				continue // 对象删除失败保行（下拍重试，行/对象不拆对）
			}
		}
		if err := bk.repos.Backups.Delete(ctx, b.ID); err != nil {
			bk.log.Error("prune pass: delete row", "backup", b.ID, "err", err)
		}
	}
	// 失败行保留清扫（同环收尾；失败行无对象产物仅删行——重试风暴的台账
	// 垃圾由此出清，48h 窗见 backupFailureBackoff 注释）。
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

// emitBackupEvent 落备份事件（成功/失败统一形态）。资源轴 Team 经
// Database → Project 链解析（行缺失/存储故障 → ”，按域 A 化补齐）；
// 解析面与发射面经 emitter 端口。
func (bk *Backups) emitBackupEvent(ctx context.Context, name string, b *backup.Backup, errMsg string) {
	team := ""
	if db, err := bk.repos.Databases.Get(ctx, b.DatabaseID); err == nil {
		team = bk.eventTeamOfProject(ctx, bk.repos, db.ProjectID)
	} else {
		bk.log.Warn("events: resolve database team", "database", b.DatabaseID, "err", err)
	}
	if err := bk.emitObservation(ctx, team, name, "database", b.DatabaseID,
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

// Backup 事件名锚定（F2.2，ADR-0039；usage 反扫的字面量命中点）。
const (
	// EventBackupSucceeded 是单库备份成功（engine 备份环发射）。
	EventBackupSucceeded = "database.backup_succeeded"
	// EventBackupFailed 是单库备份失败（engine 备份环发射）。
	EventBackupFailed = "database.backup_failed"
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
// （key/digest/size），失败携带 error（用户可见文本英文）。
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

// platformBackupEventJSON 序列化平台备份事件载荷（同上随域迁入）。
func platformBackupEventJSON(retention, errMsg string) []byte {
	out, _ := json.Marshal(PlatformBackupEventPayload{
		Retention: retention,
		Error:     errMsg,
	})
	return out
}
