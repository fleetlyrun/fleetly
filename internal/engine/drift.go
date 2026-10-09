package engine

// ADR-0022 漂移检测口径：启动基线重放（重建归属/期望缓存）+ spec 对照
// 扫描（RuntimeInspector 子面；gen-only 降级）+ 稳态看门狗（workload.stopped
// 观测事件）。收敛仍 opt-in（ADR-0005）：drift 只发事件，Ensure 是唯一
// 写动词。

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// rebuildBaselines 按 succeeded 基线幂等重放 Ensure（启动一次）：每个
// "最近部署为 succeeded"的 App 以行上 Generation 重下发——载体未变即
// no-op，重建 workloadApp/expected/ensuredSpec 缓存。失败不阻断启动
// （下一扫描拍兜底，drift 降级 gen-only/状态观测）。
//
// 有活跃部署的 App 跳过：在途驱动器拥有该 App 的 Ensure 权（基线重放
// 与驱动互翻 Generation 标签会让载体多滚一轮——dind 场景 1 实证）；
// 该 App 终态后下一次重启补上。跳过判定有两层（N0.1 P1-3）：启动快照
// （ListDriving 一次）只作初筛；每 App 重放前在 App 级互斥内复查
// ActiveByApp——快照后受理的部署在此跳过，且 Submit 与重放共享同一
// 互斥，"复查后落行、重放再 Ensure 旧 Generation 与驱动器对翻标签"
// 的窗口不存在（健康部署被 L1 误判回滚的根因）。
func (e *Engine) rebuildBaselines(ctx context.Context) {
	driving, err := e.deployments.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("baseline replay: list driving", "err", err)
		return
	}
	busy := make(map[string]bool, len(driving))
	for _, d := range driving {
		busy[d.AppID] = true
	}
	apps, err := e.apps.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("baseline replay: list apps", "err", err)
		return
	}
	for _, a := range apps {
		if ctx.Err() != nil {
			return
		}
		if busy[a.ID] {
			continue
		}
		d, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), a.ID)
		if err != nil {
			continue // 无成功基线（从未部署成功）：无可重放
		}
		e.replayAppBaseline(ctx, &a, d)
	}
}

// replayAppBaseline 在 App 级互斥内复查活跃部署并重放单 App 基线：
// 有活跃即跳过（交给驱动器）；互斥与 Submit 共享，复查所见即终局。
func (e *Engine) replayAppBaseline(ctx context.Context, a *app.App, d *deployment.Deployment) {
	appMu := e.lockApp(a.ID)
	appMu.Lock()
	defer appMu.Unlock()

	active, err := e.deployments.ActiveByApp(ctx, e.db.Runner(), a.ID)
	if err != nil {
		e.log.Error("baseline replay: recheck active", "app", a.ID, "err", err)
		return
	}
	if len(active) > 0 {
		return // 快照后受理：驱动器拥有该 App 的 Ensure 权
	}
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
	if err := e.materialize(stepCtx, d, d.ToRevision, d.Generation, true); err != nil {
		e.log.Error("baseline replay: ensure", "app", a.ID, "generation", d.Generation, "err", err)
	}
	cancel()
}

// driftScan 一拍漂移扫描：spec 对照（RuntimeInspector 可用时）+ 稳态
// 看门狗 + 跨域附件隔离不变式（F1.8）。每 App 一次 Inspect（N0 小团队
// 规模）；错误逐 App 记日志不阻断。
func (e *Engine) driftScan(ctx context.Context) {
	inspector := capability.FacesOf(e.runtime).Inspector // spec 对照子面（FacesOf 协商点；nil = Provider 未提供）

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

	// 扫描面 = 当前期望缓存（启动基线重放或部署 Ensure 建立）。spec 对照
	// 只走 App 域 owner——Database/受管域 owner 只入看门狗（expected 已
	// 登记），不进 App 表解析面（否则每拍一条噪声错；typed owner 域分派
	// 取代前缀匹配）。
	e.expect.mu.Lock()
	appIDs := make([]string, 0, len(e.expect.expected))
	for owner := range e.expect.expected {
		if owner.domain == ownerApp {
			appIDs = append(appIDs, owner.id)
		}
	}
	e.expect.mu.Unlock()

	for _, appID := range appIDs {
		if ctx.Err() != nil {
			return
		}
		if inspector == nil {
			continue
		}
		// 域解析走 appTeam（N0.1 P2-11：不再内联 Team:"default"——团队
		// 解析单一真源，F0.5 从 Project 行实取时此处随动）。
		team, a, err := e.appTeam(ctx, appID)
		if err != nil {
			e.log.Error("drift scan: resolve app", "app", appID, "err", err)
			continue
		}
		ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: appID}
		scanCtx, cancel := context.WithTimeout(ctx, e.opts.ManagedStepTimeout)
		obs, err := inspector.InspectWorkloads(scanCtx, ns)
		cancel()
		if err != nil {
			e.log.Error("drift scan: inspect", "app", appID, "err", err)
			continue
		}
		e.compareSpecs(ctx, appID, obs)
	}

	// 稳态看门狗：观测槽里当前 Generation 的 stopped（去抖；回 running 清）。
	type wlObs struct {
		wid string
		ev  capability.WorkloadEvent
	}
	expected := map[workloadOwner]uint64{}
	e.expect.mu.Lock()
	for owner, gen := range e.expect.expected {
		expected[owner] = gen
	}
	e.expect.mu.Unlock()

	var candidates []wlObs
	snap := e.obs.snapshot()
	for wid, owner := range snap.workloadApp {
		if expected[owner] == 0 {
			continue
		}
		// 在途 App 不发稳态 stopped（部署中的停止是部署链的事）；非 App
		// 域（Database/受管）无在途概念，恒可判。
		if owner.domain == ownerApp && inFlight[owner.id] {
			continue
		}
		ev, seen := snap.observations[wid]
		if seen && ev.State == capability.WorkloadStopped && uint64(ev.Generation) == expected[owner] {
			candidates = append(candidates, wlObs{wid: wid, ev: ev})
		}
	}
	for _, c := range candidates {
		e.emitSteadyStateStopped(ctx, c.wid, c.ev)
	}

	// 跨 Project 附件隔离不变式（ADR-0013 附录 A.4）：已撤销的挂靠在
	// 下一拍被剥离重收敛（撤销即时隔离的自愈兜底）。
	e.enforcePeerIsolation(ctx)
}

// compareSpecs 逐载体对照观测 spec 与缓存期望 spec：失配 → drift 事件
// （签名含 spec 指纹去抖；同一失配不重复发，回归即清）。obsMu.RLock 只
// 护缓存读——事件落账在锁外、经调用链 ctx（关停可取消；N0.1 P2-10）。
//
// 镜像逐字比对是正确口径（2026-10-01 双腿实证，N0.1 P1-2）：dind 里
// `docker service create` CLI 会尝试把 tag 解析钉版为 repo:tag@sha256
// （registry 不可达时存原样 tag 并告警）；但 fleetly 走 moby API 直传
// spec，swarm 服务端不重写 spec.Image——staging 现役三个 tag 部署载体
// spec 全为原样 tag，且 6 次部署 + 全天 30s 扫描零 drift 事件。假 drift
// 假设（API 路径钉版）不成立；若未来出现钉版形态（如 CLI 人工改同 tag
// 镜像），以显式证据重开此案，不在此预放行。
func (e *Engine) compareSpecs(ctx context.Context, appID string, obs []capability.WorkloadObservation) {
	e.expect.mu.Lock()
	expected := e.expect.expected[appOwner(appID)]
	e.expect.mu.Unlock()

	type pendingDrift struct {
		wid    string
		anchor uint64
		ev     capability.WorkloadEvent
	}
	var pending []pendingDrift
	type pendingRollout struct {
		wid    string
		gen    uint64
		detail string
	}
	var rollout []pendingRollout
	for _, o := range obs {
		want, ok := e.obs.ensuredSpecOf(o.WorkloadID)
		if !ok {
			continue // 非平台管辖（孤儿面：只登记原则）
		}

		// 滚动停摆观测（workload.rollout_stalled，runbook 记录·二十七）：
		// Ensure 已被编排器接受但滚动停在中间态——spec 面一致，spec drift
		// 恒无感，与 spec 失配分立去抖（同停摆不重复发，恢复即清）。判定
		// 在 gen 锚之前：停摆时 spec 通常是最新 gen，但 gen 偏离与停摆是
		// 两个独立信号，不互斥。
		if o.RolloutStalled {
			sig := "rollout|" + o.RolloutDetail
			e.drift.mu.Lock()
			if e.drift.rolloutSig[o.WorkloadID] != sig {
				e.drift.rolloutSig[o.WorkloadID] = sig
				rollout = append(rollout, pendingRollout{wid: o.WorkloadID, gen: uint64(o.Generation), detail: o.RolloutDetail})
			}
			e.drift.mu.Unlock()
		} else {
			e.drift.mu.Lock()
			delete(e.drift.rolloutSig, o.WorkloadID)
			e.drift.mu.Unlock()
		}

		// 逐载体对照锚（ADR-0048 决策 2：双代窗两代并存各自对照——
		// ensuredGen 在场即优先，缺席回落 App 级锚）。
		anchor := expected
		if g, ok := e.obs.ensuredGenOf(o.WorkloadID); ok {
			anchor = g
		}
		if uint64(o.Generation) != anchor {
			// gen 偏离由 Watch 流路径负责（detectDrift），此处不重复发。
			continue
		}
		mismatch := ""
		switch {
		case o.Image != want.Image:
			mismatch = fmt.Sprintf("image %q != expected %q", o.Image, want.Image)
		case !sameCommand(o.Command, want.Command):
			// 入口覆盖命令对照（ADR-0022）：nil 与空切片等价（无覆盖 =
			// 镜像默认），逐元素比对防假 drift。
			mismatch = fmt.Sprintf("command %q != expected %q", o.Command, want.Command)
		case o.Replicas != want.Replicas:
			mismatch = fmt.Sprintf("replicas %d != expected %d", o.Replicas, want.Replicas)
		}
		if mismatch == "" {
			e.drift.mu.Lock()
			delete(e.drift.sig, o.WorkloadID)
			e.drift.mu.Unlock()
			continue
		}
		sig := fmt.Sprintf("spec|%s", mismatch)
		e.drift.mu.Lock()
		if e.drift.sig[o.WorkloadID] == sig {
			e.drift.mu.Unlock()
			continue
		}
		e.drift.sig[o.WorkloadID] = sig
		e.drift.mu.Unlock()
		pending = append(pending, pendingDrift{
			wid:    o.WorkloadID,
			anchor: anchor,
			ev: capability.WorkloadEvent{
				WorkloadID: o.WorkloadID, Generation: o.Generation,
				State:   capability.WorkloadDegraded,
				Message: "spec drift: " + mismatch,
			},
		})
	}

	for _, p := range pending {
		if _, err := e.outbox.Append(ctx, e.db.Runner(),
			eventWorkloadDrift, "workload", p.wid,
			driftEventPayloadJSON(p.ev, appOwner(appID), p.anchor)); err != nil {
			e.log.Error("drift scan: event", "workload", p.wid, "err", err)
		}
	}
	for _, p := range rollout {
		if _, err := e.outbox.Append(ctx, e.db.Runner(),
			eventWorkloadRolloutStall, "workload", p.wid,
			rolloutStalledEventPayloadJSON(p.wid, appID, p.gen, p.detail)); err != nil {
			e.log.Error("drift scan: rollout event", "workload", p.wid, "err", err)
		}
	}
}

// sameCommand 逐元素比较入口覆盖命令（nil 与空切片等价：两者都是
// "无覆盖，镜像默认"——载体观测与投影 spec 的切片形态差异不得产假 drift）。
func sameCommand(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// emitSteadyStateStopped 发稳态 workload.stopped（去抖：签名 = wid|gen|state；
// 观测回 running/degraded 清签名）。
func (e *Engine) emitSteadyStateStopped(ctx context.Context, wid string, ev capability.WorkloadEvent) {
	sig := fmt.Sprintf("stopped|%d", uint64(ev.Generation))
	e.drift.stoppedMu.Lock()
	if e.drift.stoppedSig[wid] == sig {
		e.drift.stoppedMu.Unlock()
		return
	}
	e.drift.stoppedSig[wid] = sig
	e.drift.stoppedMu.Unlock()
	// app_id 仅 App 域载体携带（typed owner 行为批）：内部路由键不再漏进
	// 用户可见载荷——数据库/受管载体的 stopped 事件不带伪 app_id。
	appID := ""
	if owner, ok := e.obs.ownerOf(wid); ok && owner.domain == ownerApp {
		appID = owner.id
	}
	_, err := e.outbox.Append(ctx, e.db.Runner(), eventWorkloadStopped, "workload", wid,
		stoppedEventPayloadJSON(wid, appID, ev))
	if err != nil {
		e.log.Error("steady-state watchdog: event", "workload", wid, "err", err)
	}
}

// clearStoppedSig 观测脱离 stopped（running/degraded）时清稳态签名（下次
// 停止可再发）。
func (e *Engine) clearStoppedSig(wid string) {
	e.drift.stoppedMu.Lock()
	delete(e.drift.stoppedSig, wid)
	e.drift.stoppedMu.Unlock()
}

// driftScanLoop 周期扫描（DriftScanInterval 节拍；架构 §0 唯一骨架——
// 经 engine.NewLoop 收敛，不自建 ticker）。整拍不设总预算（N0.1 P2-10：
// ManagedStepTimeout÷N 的总预算语义让多 App 拍随规模劣化）——每 App 的
// Inspect 各自带 ManagedStepTimeout 界，拍间由 ctx 取消收口。
func (e *Engine) driftScanLoop(ctx context.Context) {
	e.driftLoop.Run(ctx, e.opts.DriftScanInterval, func(ctx context.Context) {
		e.driftScan(ctx)
	})
}
