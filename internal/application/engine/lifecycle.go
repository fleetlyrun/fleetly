package engine

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 引擎生命周期（B12 P3-6 冻结面）：Start/StartWatch/buildRootCtx/
// resetOrphanBuilds/Stop 自 engine.go 逐字迁移（P3 收官批 8 壳收口——换
// 文件不换语义，lifecycleMu 互斥与 wg 有界排水协议零漂移；仅 Options
// 按域切片的机械消费点改写）。

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
	e.resetOrphanBuilds(runCtx)
	e.sweepInterruptedBackups(runCtx)
	// Route 发布就绪门（H7）：启动基线重放收口前关闭——冷缓存的逐 Route
	// 跳过会把"半量快照"当成功并 remember（签名固化到慢通道重放），且
	// traefik 5s poll 可能拉到空/部分配置覆盖 last-known-good。
	e.routeReady.Store(false)
	e.wg.Add(len(e.rings))
	for _, r := range e.rings {
		r := r
		go func() {
			defer e.wg.Done()
			r.loop.Run(runCtx, e.opts.Ring.Tick, r.step)
		}()
	}
	e.StartWatch(runCtx)
	for _, r := range e.rings {
		r.loop.Kick() // 启动即收敛：进程重启后按 Generation 幂等重放（场景 1）
	}
	// ADR-0022：启动基线重放（异步；重建归属/期望缓存）+ 漂移扫描环
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
	// 回环中继（ADR-0049）：manager 节点的 exec 路由面。子面缺席或
	// 未配 URL 即静默跳过（exec 受理面诚实失败兜底）。
	if e.execFace != nil && e.opts.Relay.RelayLoopbackURL != "" {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			_ = e.execFace.RunNodeRelay(runCtx, capability.NodeRelayOptions{
				GatewayURL: e.opts.Relay.RelayLoopbackURL, Version: e.opts.Relay.RelayVersion,
			})
		}()
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

// resetOrphanBuilds 把崩溃遗留的 building 行重置 queued（重放：buildkit
// 缓存幂等）。输入登记不在此重建——driveBuilding 见到在途行时幂等重建
// （D-4）；归属部署已终态的孤儿行由 buildStep 前置检一跳到终态。
func (e *Engine) resetOrphanBuilds(ctx context.Context) {
	if len(e.builders) == 0 {
		return
	}
	active, err := e.repos.Builds.ListActive(ctx)
	if err != nil {
		e.log.Error("build: reset orphans", "err", err)
		return
	}
	for _, b := range active {
		if b.State != build.StateBuilding {
			continue
		}
		if _, err := e.transitBuild(ctx, &b,
			[]build.State{build.StateBuilding}, build.StateQueued, nil); err != nil {
			e.log.Error("build: reset orphan", "build", b.ID, "err", err)
		}
	}
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
