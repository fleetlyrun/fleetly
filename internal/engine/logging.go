package engine

// 采集环（第 9 收敛环，F2.4/ADR-0040 决策 2）：控制面集中采集。每活跃
// 隔离域一条 Follow 流 goroutine（Runtime.StreamLogs——swarm 集群面
// ServiceLogs，覆盖全部节点），帧 → 批汇 → Logging.Ingest。
//
// 断流自愈：游标（log_cursors）只在 Ingest 成功后推进。VL 不可达 →
// WriteLog 上抛 → 流中断 → 退避重启 Since=游标 → docker json-file 缓冲
// 重放补窗（补窗深度以 docker 日志文件在场为界——诚实边界）。

import (
	"context"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/logcursor"
)

// 采集参数（环本地常量；不进 Options——无运维面诉求）。
const (
	// logIngestBatchFrames 是单批帧数上限（达此即同步 flush——写端在
	// docker 流读侧天然背压）。
	logIngestBatchFrames = 256
	// logIngestIdleFlush 是空闲冲刷的闲置阈值（批非空且末帧入批超过本值
	// → loggingStep 冲刷；低流量域的时间面承载，ADR-0040）。
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
// 与 build 日志回读消费，ADR-0040）。
func (e *Engine) LoggingProvider() capability.Logging { return e.logging }

// loggingStep 是采集环的收敛步：①枚举活跃隔离域 → 对账常驻流 goroutine
// （新增域起流；消失域 cancel）；②空闲批冲刷（低流量域的批不到帧量阈值
// 永不 flush——批汇的时间面由本 step 的节拍承载，ADR-0040 "≤1s 或 256 帧"）。
// 流本体生命周期跨 step（退避自愈在 goroutine 内），step 只做账本对账与
// 冲刷。
func (e *Engine) loggingStep(ctx context.Context) {
	if e.logging == nil {
		return
	}
	namespaces, err := e.collectNamespaces(ctx)
	if err != nil {
		e.log.Error("logging step: enumerate namespaces", "err", err)
		// 账本不动：既有流继续跑（增量对账，读失败不拆流）；冲刷照常。
		e.flushIdleLogBatches()
		return
	}
	e.logpipe.mu.Lock()
	live := map[string]bool{}
	for _, ns := range namespaces {
		key := ns.String()
		live[key] = true
		if _, ok := e.logpipe.streams[key]; ok {
			continue
		}
		streamCtx, cancel := context.WithCancel(e.buildRootCtx()) //nolint:gosec // G118 误报：cancel 登记账本（streams map），域消失/Stop 时调用收口
		e.logpipe.streams[key] = cancel
		e.wg.Add(1)
		go func(ns capability.NamespaceRef) {
			defer e.wg.Done()
			e.runLogCollector(streamCtx, ns)
		}(ns) //nolint:gosec // 采集 goroutine 生命周期 = 引擎进程（step 起拍、Stop 有界排水）
	}
	for key, cancel := range e.logpipe.streams {
		if live[key] {
			continue
		}
		cancel()
		delete(e.logpipe.streams, key)
	}
	batches := make([]*nsBatch, 0, len(e.logpipe.batches))
	for _, b := range e.logpipe.batches {
		batches = append(batches, b)
	}
	e.logpipe.mu.Unlock()
	// 冲刷在锁外（Ingest 有界 IO；批自身有互斥）。
	now := e.clock.Now()
	for _, b := range batches {
		b.flushIdle(now)
	}
}

// flushIdleLogBatches 是枚举失败路径的冲刷面（流不动、批照冲）。
func (e *Engine) flushIdleLogBatches() {
	e.logpipe.mu.Lock()
	batches := make([]*nsBatch, 0, len(e.logpipe.batches))
	for _, b := range e.logpipe.batches {
		batches = append(batches, b)
	}
	e.logpipe.mu.Unlock()
	now := e.clock.Now()
	for _, b := range batches {
		b.flushIdle(now)
	}
}

// collectNamespaces 枚举活跃隔离域（权威表真源：用户域 apps/活跃 tasks/
// databases + 受管域三件）。受管域从各 Provider 的 Managed 声明实取
// （Edge/Registry/Logging 自描述——新增受管 Provider 自动进采集面）。
func (e *Engine) collectNamespaces(ctx context.Context) ([]capability.NamespaceRef, error) {
	projects, err := e.projects.List(ctx, e.db.Runner())
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
	apps, err := e.apps.List(ctx, e.db.Runner())
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
	tasks, err := e.tasks.ListDriving(ctx, e.db.Runner())
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if ns, ok := nsFor(tasks[i].ProjectID); ok {
			ns.Task = tasks[i].ID
			out = append(out, ns)
		}
	}
	dbs, err := e.databases.List(ctx, e.db.Runner())
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
	// 受管域（Edges 顺序：Edge → Registry → Logging，与 reconciler 同源）。
	for _, p := range []capability.Provider{e.edge, e.registry, e.logging} {
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
func (e *Engine) runLogCollector(ctx context.Context, ns capability.NamespaceRef) {
	logs := capability.FacesOf(e.runtime).Logs
	if logs == nil {
		return // Runtime 无日志子面：采集面停用（诚实缺省，不报错）
	}
	backoff := time.Second
	for ctx.Err() == nil {
		cursor, err := e.logpipe.cursors.Get(ctx, e.db.Runner(), ns.String())
		if err != nil {
			e.log.Error("log collector: load cursor", "namespace", ns.String(), "err", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(2*backoff, logStreamRetryBackoff)
			continue
		}
		started := e.clock.Now()
		since := cursor
		if since.IsZero() {
			since = started.Add(-logFirstCollectLookback) // 首采：有界回看，不整灌存量
		}
		batch := &nsBatch{e: e, ns: ns, cursor: cursor, lastAppend: started}
		key := ns.String()
		e.logpipe.mu.Lock()
		e.logpipe.batches[key] = batch
		e.logpipe.mu.Unlock()
		qerr := logs.StreamLogs(ctx, capability.LogQuery{
			Namespace: ns,
			Since:     since,
			Follow:    true,
		}, batch)
		finalErr := batch.flush(context.WithoutCancel(ctx))
		e.logpipe.mu.Lock()
		if e.logpipe.batches[key] == batch {
			delete(e.logpipe.batches, key) // 流代际退出：摘批（下一轮重登）
		}
		e.logpipe.mu.Unlock()
		if ctx.Err() != nil {
			return // 域消失/Stop：终批已 flush，干净退出
		}
		if qerr != nil {
			e.log.Warn("log collector: stream ended, retrying", "namespace", ns.String(), "err", qerr)
		} else if finalErr != nil {
			e.log.Warn("log collector: final flush failed, retrying", "namespace", ns.String(), "err", finalErr)
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second // 健康跑满一窗：退避复位
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, logStreamRetryBackoff)
	}
}

// sleepCtx 睡 d 直至完成或 ctx 取消（false = 已取消）——observ.go 同名
// 共享（单定义在彼处）。

// nsBatch 是单域批汇器：WriteLog 域归因盖戳（Kind=runtime + 域字段——
// 采集侧单点盖戳，Provider 面不重复）→ 攒批。flush 双触发：①达量
// （logIngestBatchFrames——写路径内联，背压天然成立）；②空闲冲刷
// （loggingStep 节拍发现批闲置 ≥logIngestIdleFlush 即冲——低流量域的批
// 永远到不了帧量阈值，时间面由环节拍承载，staging 真机实证 2026-10-04）。
// 达量 flush 失败上抛给 StreamLogs（断流重启触发面——游标不推进，重放
// 自愈）；空闲冲刷失败只告警（流不断，下一拍重试，gap 由重启重放兜底）。
type nsBatch struct {
	e      *Engine
	ns     capability.NamespaceRef
	cursor time.Time

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
	now := b.e.clock.Now()
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
		b.e.log.Warn("log collector: idle flush failed; will retry next tick", "namespace", b.ns.String(), "err", err)
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
	if err := b.e.logging.Ingest(ictx, frames); err != nil {
		b.mu.Lock()
		b.dropped += len(frames)
		b.mu.Unlock()
		b.e.log.Warn("log collector: ingest failed; will replay from cursor", "namespace", b.ns.String(), "dropped", b.dropped, "err", err)
		return err
	}
	if time.Since(b.cursorPersistAt) >= logCursorPersistInterval {
		if err := b.e.logpipe.cursors.Save(ctx, b.e.db.Runner(), b.ns.String(), cursor); err != nil {
			b.e.log.Error("log collector: persist cursor", "namespace", b.ns.String(), "err", err)
			// 游标落盘失败不致命：内存游标继续推进，重启多重放一段（VL 同流去重兜住）
		}
		b.cursorPersistAt = b.e.clock.Now()
	}
	return nil
}

// logpipeState 是采集环的实例态（C5：实例内聚合，不进包级全局）。
type logpipeState struct {
	mu      sync.Mutex
	streams map[string]context.CancelFunc // ns 键 → 域流 cancel（Stop/域消失收口）
	batches map[string]*nsBatch           // ns 键 → 在飞批（step 空闲冲刷面）
	cursors *logcursor.Repo
}
