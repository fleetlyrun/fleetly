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
// 标记域，非管辖载体只登记不自动删（CONTEXT.md Orphan）。
func (e *Engine) TeardownApp(ctx context.Context, appID string) error {
	a, err := e.apps.Get(ctx, e.db.Runner(), appID)
	if err != nil {
		return err
	}
	team, _, err := e.appTeam(ctx, appID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: a.ID}
	if err := e.runtime.Remove(ctx, ns); err != nil {
		return fmt.Errorf("runtime remove: %w", err)
	}

	// 缓存收口：先收集该 App 名下的 Workload 集，再逐面清（drift/稳态
	// 签名只按 workloadID 键，跨 App 不得误删）。
	e.obsMu.Lock()
	var wids []string
	for wid, owner := range e.workloadApp {
		if owner == appID {
			wids = append(wids, wid)
		}
	}
	for _, wid := range wids {
		delete(e.workloadApp, wid)
		delete(e.ensuredGen, wid)
		delete(e.ensuredSpec, wid)
		delete(e.observations, wid)
	}
	e.obsMu.Unlock()
	e.expectMu.Lock()
	delete(e.expected, appID)
	e.expectMu.Unlock()
	e.driftMu.Lock()
	for _, wid := range wids {
		delete(e.drift, wid)
	}
	e.driftMu.Unlock()
	e.stoppedMu.Lock()
	for _, wid := range wids {
		delete(e.stoppedSig, wid)
	}
	e.stoppedMu.Unlock()
	return nil
}
