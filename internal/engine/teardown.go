package engine

// ADR-0023 App 删除语义的 engine 面：TeardownApp 收口（Runtime.Remove 拆
// 域内载体 + 缓存清理）。副作用与审计/tombstone 不可同事务——API 层按
// "先收口后落账"编排，失败即整体失败（App 保持可操作，可重试删除）。

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TeardownApp 拆除 App 的全部 Runtime 载体（幂等；Remove 对不存在对象不
// 报错）并清归属/期望/drift/观测缓存。孤儿原则维持：Remove 只拆 fleetly
// 标记域，非管辖载体只登记不自动删（CONTEXT.md Orphan）。与 admission/
// 基线重放共享 App 级互斥（N0.1 P1-3）：收口期间受理与重放排队。
func (e *Engine) TeardownApp(ctx context.Context, appID string) error {
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()

	// 锁内预检（ADR-0023 修订）：拆载体前复查活跃部署。与 Submit 共享
	// appMu，此处所见即受理终局——API 无锁预检到收口之间受理的部署在
	// 一切副作用之前拒绝（否则拆掉的载体要靠在途部署重放自愈，白承受
	// 一次可用性抖动）。
	active, err := e.deployments.ActiveByApp(ctx, e.db.Runner(), appID)
	if err != nil {
		return err
	}
	if len(active) > 0 {
		return fmt.Errorf("%w: app %s has %d active deployment(s)", ErrActiveDeployment, appID, len(active))
	}

	a, err := e.apps.Get(ctx, e.db.Runner(), appID)
	if err != nil {
		return err
	}
	team, _, err := e.appTeam(ctx, appID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: a.ID}
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
	e.obs.mu.Lock()
	var wids []string
	for wid, owner := range e.obs.workloadApp {
		if owner == appID {
			wids = append(wids, wid)
		}
	}
	for _, wid := range wids {
		delete(e.obs.workloadApp, wid)
		delete(e.obs.ensuredGen, wid)
		delete(e.obs.ensuredSpec, wid)
		delete(e.obs.observations, wid)
	}
	e.obs.mu.Unlock()
	e.expect.mu.Lock()
	delete(e.expect.expected, appID)
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
	return nil
}
