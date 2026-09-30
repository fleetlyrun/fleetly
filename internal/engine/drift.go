package engine

// ADR-0022 漂移检测口径：启动基线重放（重建归属/期望缓存）+ spec 对照
// 扫描（RuntimeInspector 子面；gen-only 降级）+ 稳态看门狗（workload.stopped
// 观测事件）。收敛仍 opt-in（ADR-0005）：drift 只发事件，Ensure 是唯一
// 写动词。

import (
	"context"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// rebuildBaselines 按 succeeded 基线幂等重放 Ensure（启动一次）：每个
// "最近部署为 succeeded"的 App 以行上 Generation 重下发——载体未变即
// no-op，重建 workloadApp/expected/ensuredSpec 缓存。失败不阻断启动
// （下一扫描拍兜底，drift 降级 gen-only/状态观测）。
func (e *Engine) rebuildBaselines(ctx context.Context) {
	apps, err := e.apps.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("baseline replay: list apps", "err", err)
		return
	}
	for _, a := range apps {
		if ctx.Err() != nil {
			return
		}
		d, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), a.ID)
		if err != nil {
			continue // 无成功基线（从未部署成功）：无可重放
		}
		stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
		err = e.replayBaseline(stepCtx, &a, d)
		cancel()
		if err != nil {
			e.log.Error("baseline replay: ensure", "app", a.ID, "generation", d.Generation, "err", err)
		}
	}
}

// replayBaseline 投影并 Ensure 单个 App 的基线（recordEnsured 重建缓存）。
func (e *Engine) replayBaseline(ctx context.Context, a *app.App, d *deployment.Deployment) error {
	spec, err := e.loadSpec(d.ToRevision)
	if err != nil {
		return fmt.Errorf("load revision spec: %w", err)
	}
	team, _, err := e.appTeam(ctx, a.ID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	digests, err := e.buildDigests(ctx, d)
	if err != nil {
		return fmt.Errorf("resolve build digests: %w", err)
	}
	ws, ns, err := Project(spec, team, digests)
	if err != nil {
		return fmt.Errorf("project spec: %w", err)
	}
	materials, err := e.resolveMaterials(ctx, spec, spec.GetApp().GetProject())
	if err != nil {
		return fmt.Errorf("resolve materials: %w", err)
	}
	e.applyVolumePinning(ctx, ws, spec.GetApp().GetProject())
	if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(d.Generation), materials); err != nil {
		return fmt.Errorf("runtime ensure: %w", err)
	}
	e.recordEnsured(d, d.Generation, ws)
	return nil
}

// driftScan 一拍漂移扫描：spec 对照（RuntimeInspector 可用时）+ 稳态
// 看门狗。每 App 一次 Inspect（N0 小团队规模）；错误逐 App 记日志不阻断。
func (e *Engine) driftScan(ctx context.Context) {
	inspector, hasInspector := e.runtime.(capability.RuntimeInspector)

	// 在途 App 集（稳态判定：不在途才发 workload.stopped）。
	driving, err := e.deployments.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("drift scan: list driving", "err", err)
		return
	}
	inFlight := make(map[string]bool, len(driving))
	for _, d := range driving {
		inFlight[d.AppID] = true
	}

	// 扫描面 = 当前期望缓存（启动基线重放或部署 Ensure 建立）。
	e.expectMu.Lock()
	appIDs := make([]string, 0, len(e.expected))
	for appID := range e.expected {
		appIDs = append(appIDs, appID)
	}
	e.expectMu.Unlock()

	for _, appID := range appIDs {
		if ctx.Err() != nil {
			return
		}
		if !hasInspector {
			continue
		}
		ns := capability.NamespaceRef{Team: "default", Project: "", App: appID}
		if a, err := e.apps.Get(ctx, e.db.Runner(), appID); err == nil {
			ns.Project = a.ProjectID
		} else {
			e.log.Error("drift scan: resolve app", "app", appID, "err", err)
			continue
		}
		scanCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
		obs, err := inspector.InspectWorkloads(scanCtx, ns)
		cancel()
		if err != nil {
			e.log.Error("drift scan: inspect", "app", appID, "err", err)
			continue
		}
		e.compareSpecs(appID, obs)
	}

	// 稳态看门狗：观测槽里当前 Generation 的 stopped（去抖；回 running 清）。
	type wlObs struct {
		wid string
		ev  capability.WorkloadEvent
	}
	expected := map[string]uint64{}
	e.expectMu.Lock()
	for appID, gen := range e.expected {
		expected[appID] = gen
	}
	e.expectMu.Unlock()

	var candidates []wlObs
	e.obsMu.RLock()
	for wid, appID := range e.workloadApp {
		if expected[appID] == 0 || inFlight[appID] {
			continue
		}
		ev, seen := e.observations[wid]
		if seen && ev.State == capability.WorkloadStopped && uint64(ev.Generation) == expected[appID] {
			candidates = append(candidates, wlObs{wid: wid, ev: ev})
		}
	}
	e.obsMu.RUnlock()
	for _, c := range candidates {
		e.emitSteadyStateStopped(ctx, c.wid, c.ev)
	}
}

// compareSpecs 逐载体对照观测 spec 与缓存期望 spec：失配 → drift 事件
// （签名含 spec 指纹去抖；同一失配不重复发，回归即清）。
func (e *Engine) compareSpecs(appID string, obs []capability.WorkloadObservation) {
	e.expectMu.Lock()
	expected := e.expected[appID]
	e.expectMu.Unlock()
	e.obsMu.RLock()
	defer e.obsMu.RUnlock()
	for _, o := range obs {
		want, ok := e.ensuredSpec[o.WorkloadID]
		if !ok {
			continue // 非平台管辖（孤儿面：只登记原则）
		}
		if uint64(o.Generation) != expected {
			// gen 偏离由 Watch 流路径负责（detectDrift），此处不重复发。
			continue
		}
		mismatch := ""
		switch {
		case o.Image != want.Image:
			mismatch = fmt.Sprintf("image %q != expected %q", o.Image, want.Image)
		case o.Replicas != want.Replicas:
			mismatch = fmt.Sprintf("replicas %d != expected %d", o.Replicas, want.Replicas)
		}
		if mismatch == "" {
			e.driftMu.Lock()
			delete(e.drift, o.WorkloadID)
			e.driftMu.Unlock()
			continue
		}
		sig := fmt.Sprintf("spec|%s", mismatch)
		e.driftMu.Lock()
		if e.drift[o.WorkloadID] == sig {
			e.driftMu.Unlock()
			continue
		}
		e.drift[o.WorkloadID] = sig
		e.driftMu.Unlock()
		ev := capability.WorkloadEvent{
			WorkloadID: o.WorkloadID, Generation: o.Generation,
			State:   capability.WorkloadDegraded,
			Message: "spec drift: " + mismatch,
		}
		if _, err := e.outbox.Append(context.Background(), e.db.Runner(),
			eventWorkloadDrift, "workload", o.WorkloadID,
			driftEventPayloadJSON(ev, appID, expected)); err != nil {
			e.log.Error("drift scan: event", "workload", o.WorkloadID, "err", err)
		}
	}
}

// emitSteadyStateStopped 发稳态 workload.stopped（去抖：签名 = wid|gen|state；
// 观测回 running/degraded 清签名）。
func (e *Engine) emitSteadyStateStopped(ctx context.Context, wid string, ev capability.WorkloadEvent) {
	sig := fmt.Sprintf("stopped|%d", uint64(ev.Generation))
	e.stoppedMu.Lock()
	if e.stoppedSig[wid] == sig {
		e.stoppedMu.Unlock()
		return
	}
	e.stoppedSig[wid] = sig
	e.stoppedMu.Unlock()
	appID := func() string {
		e.obsMu.RLock()
		defer e.obsMu.RUnlock()
		return e.workloadApp[wid]
	}()
	_, err := e.outbox.Append(ctx, e.db.Runner(), eventWorkloadStopped, "workload", wid,
		stoppedEventPayloadJSON(wid, appID, ev))
	if err != nil {
		e.log.Error("steady-state watchdog: event", "workload", wid, "err", err)
	}
}

// clearStoppedSig 观测脱离 stopped（running/degraded）时清稳态签名（下次
// 停止可再发）。
func (e *Engine) clearStoppedSig(wid string) {
	e.stoppedMu.Lock()
	delete(e.stoppedSig, wid)
	e.stoppedMu.Unlock()
}

// driftScanLoop 周期扫描（DriftScanInterval 节拍）。
func (e *Engine) driftScanLoop(ctx context.Context) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.opts.DriftScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			scanCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
			e.driftScan(scanCtx)
			cancel()
		}
	}()
}
