// Package logpipes 是采集环卫星（F2.4/ADR-0040 决策 2）：控制面集中采集。
// 每活跃隔离域一条 Follow 流 goroutine（Runtime.StreamLogs——swarm 集群面
// ServiceLogs，覆盖全部节点），帧 → 批汇 → Logging.Ingest。
//
// 断流自愈：游标（log_cursors）只在 Ingest 成功后推进。VL 不可达 →
// WriteLog 上抛 → 流中断 → 退避重启 Since=游标 → docker json-file 缓冲
// 重放补窗（补窗深度以 docker 日志文件在场为界——诚实边界）。
//
// P4 wave-1（ADR-0088 决策 2）：采集域接收者下域，Engine 嵌入 logpipes
// 卫星 struct。P5 wave-1（ADR-0088 决策 2 拆包形态，2026-11-01）：嵌入
// 解开——卫星独立成包，engine 核经构造注入持有（New 装配面），环表 step
// 与 LoggingProvider 消费面经卫星导出方法；依赖与机制端口（rootCtx/
// spawn）显式逐字段注入，禁回引 *engine.Engine（enginebackref 跨包执法；
// 机制契约共享面在 internal/application/engine/contract 叶子）。
package logpipes

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
)

// 采集参数（环本地常量；不进 Options——无运维面诉求）。
const (
	// logIngestBatchFrames 是单批帧数上限（达此即同步 flush——写端在
	// docker 流读侧天然背压）。单源在 engine/contract（与 build 出口批汇同
	// 契约，P5 wave-1 下沉），本别名保域内既名零漂移。
	logIngestBatchFrames = contract.LogIngestBatchFrames
	// logIngestIdleFlush 是空闲冲刷的闲置阈值（批非空且末帧入批超过本值
	// → Step 冲刷；低流量域的时间面承载，ADR-0040）。
	logIngestIdleFlush = time.Second
	// logCursorPersistInterval 是游标落盘节流（内存游标准确；落盘滞后
	// ≤ 本间隔 → 重启重放窗 ≤ 间隔+批窗，VL 服务端同流去重兜住边界行）。
	logCursorPersistInterval = 5 * time.Second
	// logStreamRetryBackoff 是流重启退避（上限封顶；成功跑满一窗即复位）。
	logStreamRetryBackoff = 30 * time.Second
	// logFirstCollectLookback 是首采回看窗（无游标域从该窗起采，不整灌
	// docker 既有历史——存量 json-file 可能无界，一次性灌库不可预期；
	// 增量面从启用时刻起算，ADR-0040）。
	logFirstCollectLookback = 5 * time.Minute
)

// LoggingProvider 返回受管日志存储（nil = Logging 面停用；API 双径路由
// 与 build 日志回读消费，ADR-0040——engine 核的门面方法委托本面）。
func (s *Logpipes) LoggingProvider() capability.Logging { return s.logging }

// Step 是采集环的收敛步（环表 step 面——engine construct.go 环表以方法
// 值挂载；P4 wave-1 前名 loggingStep，拆包导出更名）：①枚举活跃隔离域 →
// 对账常驻流 goroutine（新增域起流；消失域 cancel）；②空闲批冲刷（低流
// 量域的批不到帧量阈值永不 flush——批汇的时间面由本 step 的节拍承载，
// ADR-0040 "≤1s 或 256 帧"）。流本体生命周期跨 step（退避自愈在
// goroutine 内），step 只做账本对账与冲刷。
func (s *Logpipes) Step(ctx context.Context) {
	if s.logging == nil {
		return
	}
	namespaces, err := s.collectNamespaces(ctx)
	if err != nil {
		s.log.Error("logging step: enumerate namespaces", "err", err)
		// 账本不动：既有流继续跑（增量对账，读失败不拆流）；冲刷照常。
		s.flushIdleLogBatches()
		return
	}
	s.mu.Lock()
	live := map[string]bool{}
	for _, ns := range namespaces {
		key := ns.String()
		live[key] = true
		if _, ok := s.streams[key]; ok {
			continue
		}
		streamCtx, cancel := context.WithCancel(s.rootCtx()) //nolint:gosec // G118 误报：cancel 登记账本（streams map），域消失/Stop 时调用收口
		s.streams[key] = cancel
		s.spawn(func() { s.runLogCollector(streamCtx, ns) }) // 采集 goroutine 生命周期 = 引擎进程（step 起拍、Stop 有界排水；Add/Done 配对留在核）
	}
	for key, cancel := range s.streams {
		if live[key] {
			continue
		}
		cancel()
		delete(s.streams, key)
	}
	batches := make([]*nsBatch, 0, len(s.batches))
	for _, b := range s.batches {
		batches = append(batches, b)
	}
	s.mu.Unlock()
	// 冲刷在锁外（Ingest 有界 IO；批自身有互斥）。
	now := s.clock.Now()
	for _, b := range batches {
		b.flushIdle(now)
	}
}

// flushIdleLogBatches 是枚举失败路径的冲刷面（流不动、批照冲）。
func (s *Logpipes) flushIdleLogBatches() {
	s.mu.Lock()
	batches := make([]*nsBatch, 0, len(s.batches))
	for _, b := range s.batches {
		batches = append(batches, b)
	}
	s.mu.Unlock()
	now := s.clock.Now()
	for _, b := range batches {
		b.flushIdle(now)
	}
}

// collectNamespaces 枚举活跃隔离域（权威表真源：用户域 apps/活跃 tasks/
// databases + 受管域三件）。受管域从各 Provider 的 Managed 声明实取
// （Proxy/Registry/Logging 自描述——新增受管 Provider 自动进采集面）。
func (s *Logpipes) collectNamespaces(ctx context.Context) ([]capability.NamespaceRef, error) {
	projects, err := s.repos.Projects.List(ctx)
	if err != nil {
		return nil, err
	}
	teamOf := make(map[string]string, len(projects))
	for i := range projects {
		teamOf[projects[i].ID] = projects[i].TeamID
	}
	nsFor := func(projectID string) (capability.NamespaceRef, bool) {
		team, ok := teamOf[projectID]
		if !ok {
			return capability.NamespaceRef{}, false // 已删 Project 的残留行跳过
		}
		return capability.NamespaceRef{Team: team, Project: projectID}, true
	}
	var out []capability.NamespaceRef
	apps, err := s.repos.Apps.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].Deleted() {
			continue
		}
		if ns, ok := nsFor(apps[i].ProjectID); ok {
			ns.App = apps[i].ID
			out = append(out, ns)
		}
	}
	tasks, err := s.repos.Tasks.ListDriving(ctx)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if ns, ok := nsFor(tasks[i].ProjectID); ok {
			ns.Task = tasks[i].ID
			out = append(out, ns)
		}
	}
	dbs, err := s.repos.Databases.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range dbs {
		if dbs[i].Deleted() {
			continue
		}
		if ns, ok := nsFor(dbs[i].ProjectID); ok {
			ns.Database = dbs[i].ID
			out = append(out, ns)
		}
	}
	// 受管域（Proxies 顺序：Proxy → Registry → Logging，与 reconciler 同源）。
	for _, p := range []capability.Provider{s.proxy, s.registry, s.logging} {
		if p == nil {
			continue
		}
		if faces := capability.FacesOf(p); faces.Managed != nil {
			out = append(out, faces.Managed.ManagedNamespace())
		}
	}
	return out, nil
}

// runLogCollector 是单域采集 goroutine：载入游标 → Follow 流（Since=游标）
// → 帧经 nsBatch 汇批入库；流断（错误/取消）即终批 flush 后按退避重启
// （连续故障退避封顶；健康跑满一窗即复位——瞬断快恢复）。
func (s *Logpipes) runLogCollector(ctx context.Context, ns capability.NamespaceRef) {
	logs := capability.FacesOf(s.runtime).Logs
	if logs == nil {
		return // Runtime 无日志子面：采集面停用（诚实缺省，不报错）
	}
	backoff := time.Second
	for ctx.Err() == nil {
		cursor, err := s.cursors.Get(ctx, ns.String())
		if err != nil {
			s.log.Error("log collector: load cursor", "namespace", ns.String(), "err", err)
			if !contract.SleepCtx(ctx, backoff) {
				return
			}
			backoff = min(2*backoff, logStreamRetryBackoff)
			continue
		}
		started := s.clock.Now()
		since := cursor
		if since.IsZero() {
			since = started.Add(-logFirstCollectLookback) // 首采：有界回看，不整灌存量
		}
		batch := &nsBatch{logging: s.logging, log: s.log, clock: s.clock, cursors: s.cursors, ns: ns, cursor: cursor, lastAppend: started}
		key := ns.String()
		s.mu.Lock()
		s.batches[key] = batch
		s.mu.Unlock()
		qerr := logs.StreamLogs(ctx, capability.LogQuery{
			Namespace: ns,
			Since:     since,
			Follow:    true,
		}, batch)
		finalErr := batch.flush(context.WithoutCancel(ctx))
		s.mu.Lock()
		if s.batches[key] == batch {
			delete(s.batches, key) // 流代际退出：摘批（下一轮重登）
		}
		s.mu.Unlock()
		if ctx.Err() != nil {
			return // 域消失/Stop：终批已 flush，干净退出
		}
		if qerr != nil {
			s.log.Warn("log collector: stream ended, retrying", "namespace", ns.String(), "err", qerr)
		} else if finalErr != nil {
			s.log.Warn("log collector: final flush failed, retrying", "namespace", ns.String(), "err", finalErr)
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // 健康跑满一窗：退避复位
		}
		if !contract.SleepCtx(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, logStreamRetryBackoff)
	}
}

// nsBatch 是单域批汇器：WriteLog 域归因盖戳（Kind=runtime + 域字段——
// 采集侧单点盖戳，Provider 面不重复）→ 攒批。flush 双触发：①达量
// （logIngestBatchFrames——写路径内联，背压天然成立）；②空闲冲刷
// （Step 节拍发现批闲置 ≥logIngestIdleFlush 即冲——低流量域的批永远到
// 不了帧量阈值，时间面由环节拍承载，staging 真机实证 2026-10-04）。
// 达量 flush 失败上抛给 StreamLogs（断流重启触发面——游标不推进，重放
// 自愈）；空闲冲刷失败只告警（流不断，下一拍重试，gap 由重启重放兜底）。
// 显式最小依赖：logging/log/clock/cursors 直持端口与活体，不挂 *Engine
// 回引（enginebackref 守卫执法：域活体禁挂壳回引）。P4 wave-1 顺手摘除
// 死字段 db（构造即弃、全树零读点——卫星依赖面以实测为准）。
type nsBatch struct {
	logging capability.Logging // 受管日志存储端口
	log     *slog.Logger       // 丢批/落盘告警日志器
	clock   state.Clock        // 时间面（帧时间兜底/闲置判据/节流锚）
	cursors logcursor.Repo     // 游标 repo（Ingest 成功后推进）
	ns      capability.NamespaceRef
	cursor  time.Time

	mu     sync.Mutex
	frames []capability.LogFrame
	// lastAppend 是末帧入批时刻（空闲冲刷的闲置判据）。
	lastAppend time.Time
	// cursorPersistAt 是游标落盘节流锚（上次落盘时刻）。
	cursorPersistAt time.Time
	// dropped 是 flush 失败丢批计数（告警携带；诊断面）。
	dropped int
}

func (b *nsBatch) WriteLog(_ context.Context, f capability.LogFrame) error {
	now := b.clock.Now()
	if f.Time.IsZero() {
		f.Time = now // docker 时间戳缺席面（诚实兜底；游标不回退）
	}
	f.Team, f.Project, f.App = b.ns.Team, b.ns.Project, b.ns.App
	f.Kind = capability.LogKindRuntime
	f.Source = ""
	b.mu.Lock()
	b.frames = append(b.frames, f)
	b.lastAppend = now
	if f.Time.After(b.cursor) {
		b.cursor = f.Time
	}
	reach := len(b.frames) >= logIngestBatchFrames
	b.mu.Unlock()
	if reach {
		// 写路径内联 flush：调用方在 docker 流读侧，背压天然成立。
		return b.flush(context.Background())
	}
	return nil
}

// flushIdle 是 step 节拍驱动的空闲冲刷（非空且闲置即冲；失败仅告警）。
func (b *nsBatch) flushIdle(now time.Time) {
	b.mu.Lock()
	idle := len(b.frames) > 0 && now.Sub(b.lastAppend) >= logIngestIdleFlush
	b.mu.Unlock()
	if !idle {
		return
	}
	if err := b.flush(context.Background()); err != nil {
		b.log.Warn("log collector: idle flush failed; will retry next tick", "namespace", b.ns.String(), "err", err)
	}
}

// flush 批入库：Ingest 成功才落游标（节流）；失败丢批计数并上抛。
// ctx 不受流取消影响（终批形态传 WithoutCancel；其余调用带 10s 上界）。
func (b *nsBatch) flush(ctx context.Context) error {
	b.mu.Lock()
	if len(b.frames) == 0 {
		b.mu.Unlock()
		return nil
	}
	frames := b.frames
	b.frames = nil
	cursor := b.cursor
	b.mu.Unlock()
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.logging.Ingest(ictx, frames); err != nil {
		b.mu.Lock()
		b.dropped += len(frames)
		b.mu.Unlock()
		b.log.Warn("log collector: ingest failed; will replay from cursor", "namespace", b.ns.String(), "dropped", b.dropped, "err", err)
		return err
	}
	if time.Since(b.cursorPersistAt) >= logCursorPersistInterval {
		if err := b.cursors.Save(ctx, b.ns.String(), cursor); err != nil {
			b.log.Error("log collector: persist cursor", "namespace", b.ns.String(), "err", err)
			// 游标落盘失败不致命：内存游标继续推进，重启多重放一段（VL 同流去重兜住）
		}
		b.cursorPersistAt = b.clock.Now()
	}
	return nil
}

// Logpipes 是采集环卫星的实例态（C5：实例内聚合，不进包级全局；P3 收官
// 批收编名 logpipeState → logpipes，ADR-0075 命名法则；P5 wave-1 拆包
// 导出更名）。行为随域、依赖显式逐字段 + 机制端口注入（New 装配面，
// engine construct.go 组合根调用），禁回引 *engine.Engine（enginebackref
// 跨包执法）。
type Logpipes struct {
	// 显式依赖（实测消费面）：logging 受管存储端口；runtime 的 Logs 子面
	// （Follow 流）；proxy/registry 的 Managed 声明（受管域枚举，与
	// reconciler 同源）；repos 只读聚合目录（权威表枚举——ADR-0088 决策 3
	// 允许 transact.Stores 整体注入）；log/clock 域内日志与时间面。
	logging  capability.Logging
	runtime  capability.Runtime
	proxy    capability.Proxy
	registry capability.Registry
	repos    transact.Stores
	log      *slog.Logger
	clock    state.Clock

	// 机制端口（函数字段最小面，New 注入面绑定核方法值/闭包——设计稿 §5
	// 端口面；engine 核导出面承接）：
	// rootCtx 是 lifecycle 派生根端口（L1#10）——采集 goroutine 的 ctx
	// 根随 Start/Stop 物化/取消（未 Start 的手动驱动形态退化为 Background）。
	rootCtx func() context.Context
	// spawn 是监督 goroutine 起拍端口——wg.Add/Done 配对单源留在核
	//（卫星不可破坏 Stop 有界排水纪律；Stop 排水覆盖采集 goroutine）。
	spawn func(fn func())

	mu      sync.Mutex
	streams map[string]context.CancelFunc // ns 键 → 域流 cancel（Stop/域消失收口）
	batches map[string]*nsBatch           // ns 键 → 在飞批（step 空闲冲刷面）
	cursors logcursor.Repo
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值/闭包）。
type Deps struct {
	Logging  capability.Logging
	Runtime  capability.Runtime
	Proxy    capability.Proxy
	Registry capability.Registry
	Repos    transact.Stores
	Log      *slog.Logger
	Clock    state.Clock
	Cursors  logcursor.Repo
	// RootCtx 是 lifecycle 派生根端口（L1#10；engine 侧绑定 buildRootCtx）。
	RootCtx func() context.Context
	// Spawn 是监督 goroutine 起拍端口（Add/Done 配对单源留核）。
	Spawn func(fn func())
}

// New 构造采集环卫星（map 账本初始化；不启动——step 由 engine 环表驱动，
// 流 goroutine 由 step 起拍）。
func New(deps Deps) *Logpipes {
	return &Logpipes{
		logging:  deps.Logging,
		runtime:  deps.Runtime,
		proxy:    deps.Proxy,
		registry: deps.Registry,
		repos:    deps.Repos,
		log:      deps.Log,
		clock:    deps.Clock,
		cursors:  deps.Cursors,
		rootCtx:  deps.RootCtx,
		spawn:    deps.Spawn,
		streams:  make(map[string]context.CancelFunc),
		batches:  make(map[string]*nsBatch),
	}
}
