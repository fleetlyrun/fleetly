package engine

// ADR-0023 App 删除语义的 engine 面：TeardownApp 收口（Runtime.Remove 拆
// 域内载体 + 缓存清理）。副作用与审计/tombstone 不可同事务——API 层按
// "先收口后落账"编排，失败即整体失败（App 保持可操作，可重试删除）。

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TeardownApp 拆除 App 的全部 Runtime 载体（幂等；Remove 对不存在对象不
// 报错）并清归属/期望/drift/观测缓存。孤儿原则维持：Remove 只拆 fleetly
// 标记域，非管辖载体只登记不自动删（CONTEXT.md Orphan）。与 admission/
// 基线重放共享 App 级互斥（N0.1 P1-3）：收口期间受理与重放排队。
func (e *Engine) TeardownApp(ctx context.Context, appID string) error {
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()

	// 锁内预检（ADR-0023 修订）：拆载体前复查**驱动权**（R02-M4）。
	// 谓词与 converger 同口径（DrivingByApp = 活跃态 + failed 待自动回滚
	// 窗）——ActiveByApp 不含 failed 待回滚行，单独使用会放行"驱动器马上
	// 要回滚重放"的 App，让回卷在收口后重建载体（无主孤儿）。与 Submit
	// 共享 appMu，此处所见即受理终局——API 无锁预检到收口之间受理的部署
	// 在一切副作用之前拒绝（否则拆掉的载体要靠在途部署重放自愈，白承受
	// 一次可用性抖动）。
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		return err
	}
	if len(driving) > 0 {
		return fmt.Errorf("%w: app %s has %d driving deployment(s)", ErrActiveDeployment, appID, len(driving))
	}

	a, err := e.repos.Apps.Get(ctx, appID)
	if err != nil {
		return err
	}
	team, _, err := e.appTeam(ctx, appID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: a.ID}
	// 维护互斥读半边（R13-L1）：Remove 是载体写动词，与 Ensure 族及网络
	// 重建（写半边 detach→rm→create→attach 全序）串行化——重建窗内不得
	// 插入拆载体（半拆网 × 半拆 App 交错会把 re-attach 面对已逝归属）。
	// 锁等待不占步预算（排队语义：读锁在带界 ctx 派生之前获取）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛环卡死同害——docker hang 时 DeleteApp 的
	// gRPC 永不返回，且 appMu 被握死堵住同 App 全部受理/重放）。带
	// ManagedStepTimeout 硬上限，超时如实上抛（App 保持可操作，可重试删除）。
	rctx, rcancel := e.boundedStep(ctx)
	removeErr := e.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("runtime remove: %w", removeErr)
	}

	// 缓存收口：先收集该 App 名下的 Workload 集，再逐面清（drift/稳态
	// 签名只按 workloadID 键，跨 App 不得误删）。
	wids := e.obs.forgetOwner(appOwner(appID))
	e.expect.mu.Lock()
	delete(e.expect.expected, appOwner(appID))
	e.expect.mu.Unlock()
	e.drift.mu.Lock()
	for _, wid := range wids {
		delete(e.drift.sig, wid)
	}
	e.drift.mu.Unlock()
	e.drift.stoppedMu.Lock()
	for _, wid := range wids {
		delete(e.drift.stoppedSig, wid)
	}
	e.drift.stoppedMu.Unlock()
	// ADR-0082 缺席检测的竞态闭合：删除收口完成后钉 teardown 标记（与
	// forgetOwner 同在 appMu 临界区）——收敛复查见到标记即放弃重建，重建
	// 权交还部署驱动器（teardown-abort 的唯一重建通道，ADR-0023； Submit
	// 受理成功清标记）。 expect 缓存缺席无法区分「重启冷窗」与「删除收口
	// 后」，标记才是闭合面。
	e.converge.markTeardown(appID)
	// 删除收口即账本作废：待重放标记随 App 一并清（ADR-0082 §6——已拆除的
	// App 无期望态可回卷，残留标记会让下一位重建者看到幽灵回卷信号）。
	e.converge.clearReplay(appID)
	return nil
}
