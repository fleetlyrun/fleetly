package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 引擎生命周期（B12 P3-6 冻结面）：Start/StartWatch/buildRootCtx/Stop 自
// engine.go 逐字迁移（P3 收官批 8 壳收口——换文件不换语义，lifecycleMu
// 互斥与 wg 有界排水协议零漂移；仅 Options 按域切片的机械消费点改写）。
// P4 wave-2b：resetOrphanBuilds 随 intake 域迁 builder.go → P5 wave-5
// 拆包导出 ResetOrphanBuilds（Start 调用点经应用模块指针
// e.intake.ResetOrphanBuilds，sweepInterruptedBackups 先例）。

// Start 进入驱动：收敛循环 + Watch 消费。重复 Start 幂等（第二次为
// no-op）。全程持 lifecycleMu（B12 P3-6）：判空/赋 cancel 与 wg.Add 对
// 并发 Stop 的 wg.Wait 互斥——裸读写是数据竞争，且并发双 Start 会双发
// 全套收敛循环 goroutine。Stop 后重启（cancel 已置 nil）沿旧语义放行。
func (e *Engine) Start(ctx context.Context) {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.cancel != nil {
		return
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.cancel = cancel
	e.runCtxMu.Lock()
	e.runCtx = runCtx // 构建goroutine 派生根（Q-7：Stop 取消 → 有界排水）
	e.runCtxMu.Unlock()
	if e.intake != nil {
		e.intake.ResetOrphanBuilds(runCtx)
	}
	if e.backups != nil {
		e.backups.SweepInterrupted(runCtx)
	}
	// Route 发布就绪门（H7）：启动基线重放收口前关闭——冷缓存的逐 Route
	// 跳过会把"半量快照"当成功并 remember（签名固化到慢通道重放），且
	// traefik 5s poll 可能拉到空/部分配置覆盖 last-known-good。
	e.routeReady.Store(false)
	e.wg.Add(len(e.rings))
	for _, r := range e.rings {
		r := r
		go func() {
			defer e.wg.Done()
			e.runLoopWithRestart(runCtx, r.loop, e.opts.Ring.Tick, r.step)
		}()
	}
	e.StartWatch(runCtx)
	// 循环 panic 自愈（R02-M7 后继）：runLoopWithRestart 退避重启，连续失败
	// 达上限才记 loop failure（HealthComponents/DeadRings 查询面），进程不
	// 崩；ctx 取消（优雅停）不记死、不重启。重启 runner 计入 wg，Stop 排水
	// 覆盖（退避睡眠经 sleepCtx 随 ctx 取消立即返回）。
	for _, r := range e.rings {
		r.loop.Kick() // 启动即收敛：进程重启后按 Generation 幂等重放（场景 1）
	}
	// ADR-0022：启动基线重放（异步；重建归属/期望缓存）+ 漂移扫描循环
	//（Loop.Run 阻塞至 ctx 取消——与其他环同款 goroutine 形态）。重放
	// goroutine 计入 wg（N0.1 P2-11）：Stop 排水覆盖重放，不再裸奔。
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.runBaselineReplay(runCtx)
	}()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.driftScanLoop(runCtx)
	}()
	// 回环中继（ADR-0049/H10）：manager 节点的 exec 路由面。扩展接口缺席或
	// 未配 URL 即静默跳过（exec 受理面诚实失败兜底）。凭证 = engine 内部
	// token（进程内单源；不落库、不入脚本——worker 形态的节点个体凭证
	// 走 fleetlyd --relay-credential）。P5 wave-2：execFace 已随 execs 应用模块
	// 拆包持有（ADR-0088 决策 2 P5）；engine 去应用模块 import 第六批后核只持
	// contract.Exec 接口，本挂载是组合根调用留核、经接口 ExecFace/
	// RelayInternalCredential 访问器读（工厂缺席 = 面停用，整段跳过）。
	if e.execs != nil {
		if relayFace := e.execs.ExecFace(); relayFace != nil && e.opts.Relay.RelayLoopbackURL != "" {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				_ = relayFace.RunNodeRelay(runCtx, capability.NodeRelayOptions{
					GatewayURL: e.opts.Relay.RelayLoopbackURL, Version: e.opts.Relay.RelayVersion,
					Credential: e.execs.RelayInternalCredential,
				})
			}()
		}
	}
}

// ringRestartPolicy 是循环 panic 后的有界重启策略：第 n 次重启前等待
// baseDelay×2^(n-1)，封顶 maxDelay；连续 panic 达到 maxAttempts 即终止该环
// （markRingDead），不再无限重启。优雅取消（ctx 取消）不计失败、不重启、
// 不记死。
type ringRestartPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
}

// defaultRingRestartPolicy 是生产缺省：5 次尝试、1s→2s→4s→8s 退避（封顶
// 30s；第 5 次失败即终态）。测试可收紧 e.ringRestart。
func defaultRingRestartPolicy() ringRestartPolicy {
	return ringRestartPolicy{maxAttempts: 5, baseDelay: time.Second, maxDelay: 30 * time.Second}
}

// restartDelay 返回第 attempt 次重启（1 起）前的退避。
func (p ringRestartPolicy) restartDelay(attempt int) time.Duration {
	delay := p.baseDelay
	for i := 1; i < attempt && delay < p.maxDelay; i++ {
		delay *= 2
	}
	if delay > p.maxDelay {
		return p.maxDelay
	}
	return delay
}

// runLoopWithRestart 驱动一个循环跨 panic 代自愈：每代 Run 正常退出
// （ctx 取消）即返回；panic 终止且 ctx 未取消时按指数退避 Reset 后重跑，
// 连续 panic 达 maxAttempts 记 ring dead 并停止（健康面只报终态死亡）。
// 重启期间不做局部状态修补——step 按 Generation 幂等重放（领域模型场景 1）。
func (e *Engine) runLoopWithRestart(ctx context.Context, loop *Loop, tick time.Duration, step func(context.Context)) {
	policy := e.ringRestart
	if policy.maxAttempts <= 0 {
		policy.maxAttempts = defaultRingRestartPolicy().maxAttempts
	}
	if policy.baseDelay <= 0 {
		policy.baseDelay = defaultRingRestartPolicy().baseDelay
	}
	if policy.maxDelay <= 0 {
		policy.maxDelay = defaultRingRestartPolicy().maxDelay
	}
	if policy.maxDelay < policy.baseDelay {
		policy.maxDelay = policy.baseDelay
	}
	for attempt := 1; ; attempt++ {
		loop.Run(ctx, tick, step)
		if ctx.Err() != nil {
			return // 优雅停：不重启、不记死
		}
		if !loop.Panicked() {
			e.log.Error("engine loop exited without cancellation or panic", "loop", loop.Name())
			e.markRingDead(loop.Name(), loop.Restarts())
			return
		}
		if attempt >= policy.maxAttempts {
			e.markRingDead(loop.Name(), loop.Restarts())
			return
		}
		delay := policy.restartDelay(attempt)
		e.log.Warn("engine loop panicked; restarting after backoff",
			"loop", loop.Name(), "attempt", attempt, "max_attempts", policy.maxAttempts,
			"backoff", delay.String())
		if !sleepCtx(ctx, delay) {
			return // 退避窗内优雅停：不重启、不记死
		}
		loop.Reset()
		loop.Kick() // 重启代立即收敛一拍（不等兜底 tick）
	}
}

// StartWatch 只启动 Watch 消费（手动驱动形态配套：收敛循环不启动，观测
// 缓存仍需进——apitest golden 的确定性路径）。
func (e *Engine) StartWatch(ctx context.Context) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.consumeWatch(ctx)
	}()
}

// runBaselineReplay 是启动基线重放的收口包装（H7）：重放返回即开放 Route
// 发布门并触发一次即时发布——重放期间 publishRoutes 直接返回（不发半量
// 快照）；开门后的信号由 managed 环节拍（或 Kick）消费，在途路由变更即时
// 下发。重放失败也置 true：否则拉取端点永久 503，而基线/漂移扫描的既有
// 慢通道会继续自愈（单 App 失败不应让全平台路由面失明）。
func (e *Engine) runBaselineReplay(ctx context.Context) {
	e.rebuildBaselines(ctx)
	e.routeReady.Store(true)
	e.PublishRoutesNow()
}

// buildRootCtx 返回构建 goroutine 的派生根：Start 后是进程生命期 runCtx
// （Stop 取消 → executeBuild 优雅退出回 queued，排水有界）；未 Start 的
// 手动驱动形态（测试 DriveOnce）无 Stop 排水语义，退化为 Background 根
// （构建 goroutine 生命周期 = Build 行写者，独立于驱动调用的 ctx）。
func (e *Engine) buildRootCtx() context.Context {
	e.runCtxMu.Lock()
	defer e.runCtxMu.Unlock()
	if e.runCtx != nil {
		return e.runCtx
	}
	return context.Background()
}

// Stop 有界排水：取消循环并等待在途 step（Ensure 由 step 内 ctx 收口）
// 返回。in-flight Ensure 可安全中断重放（ADR-0005 优雅退出）。全程持
// lifecycleMu（B12 P3-6）：排水等待与并发 Start 的 wg.Add 互斥，双 Stop
// 串行在锁上（后者见 cancel=nil 幂等返回 nil）；Start 未完整（cancel 尚
// 未落）同样安全 no-op。
func (e *Engine) Stop(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()
	if e.cancel == nil {
		return nil
	}
	e.cancel()
	e.cancel = nil
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine drain exceeded deadline: %w", ctx.Err())
	}
}
