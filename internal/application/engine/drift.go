package engine

// ADR-0022 漂移检测口径：启动基线重放（重建归属/期望缓存；posture=detect
// 下改为只重建期望账本、不下发载体）+ spec 对照扫描（RuntimeInspector
// 子面；gen-only 降级）+ 稳态看门狗（workload.stopped 观测事件）。ADR-0082
// 收敛升格：检测面照发不变，driftScan 同时承载收敛判定——快通道四类信号
// （spec 失配/gen 偏离/缺席/终态后停摆，各自独立签名键）+ 慢通道
// ReconcileReplayInterval 强制重放（converge.go 统一原语）；Ensure 仍是唯一
// 写动词，升格的是 Ensure 的调用者。Watch 流 detectDrift 维持只读（事件面
// 单源，不重复发）。

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/drift"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
)

// drifts 是漂移扫描域的去抖签名（spec 对照签名 + 滚动停摆签名 +
// 稳态 stopped 签名，ADR-0022；rollout 面见 runbook 记录·二十七）。
// P3 收官批 8 收编：driftDomain → drifts（复数普通名词零后缀，ADR-0075
// 命名法则）；Engine 字段名 drift 保持不变；类型体自 engine.go 归位
// drift.go。
type drifts struct {
	mu         sync.Mutex
	sig        map[string]string // workloadID → 最后 drift 签名
	rolloutSig map[string]string // workloadID → 滚动停摆签名（rollout|detail）
	stoppedMu  sync.Mutex
	stoppedSig map[string]string // workloadID → 稳态 stopped 签名
}

// rebuildBaselines 按 succeeded 基线幂等重放 Ensure（启动一次）：每个
// "最近部署为 succeeded"的 App 以行上 Generation 重下发——载体未变即
// no-op，重建 workloadApp/expected/ensuredSpec 缓存。失败不阻断启动
// （下一扫描拍兜底，drift 降级 gen-only/状态观测）。
//
// posture=detect 时改走 rebuildBaselineExpectations：启动面**不下发载体**
// （零运行时写，ADR-0082 §5），只重建期望账本（ensuredSpec + ensuredBy）
// ——路由解析（routeExpectations）与 peer 隔离不变式（crossProjectRefs）
// 的前提。检测面在冷启动后仍在场的只有 spec/缺席/停摆三面（gen 与稳态
// stopped 锚失明，见 ADR §5 第 3 条）。
//
// 有活跃部署的 App 跳过：在途驱动器拥有该 App 的 Ensure 权（基线重放
// 与驱动互翻 Generation 标签会让载体多滚一轮——dind 场景 1 实证）；
// 该 App 终态后下一次重启补上。跳过判定有两层（N0.1 P1-3）：启动快照
// （ListDriving 一次）只作初筛；每 App 重放前在 App 级互斥内复查
// DrivingByApp（ADR-0082 收敛复查同口径——ActiveByApp 不含 failed 待
// 回滚窗，重放×回滚交错会开 vacuously ready 竞态窗）；Submit 与重放
// 共享同一互斥，"复查后落行、重放再 Ensure 旧 Generation 与驱动器对翻
// 标签"的窗口不存在（健康部署被 L1 误判回滚的根因）。
func (e *Engine) rebuildBaselines(ctx context.Context) {
	driving, err := e.repos.Deployments.ListDriving(ctx)
	if err != nil {
		e.log.Error("baseline replay: list driving", "err", err)
		return
	}
	busy := make(map[string]bool, len(driving))
	for _, d := range driving {
		busy[d.AppID] = true
	}
	apps, err := e.repos.Apps.List(ctx)
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
		d, err := e.repos.Deployments.LatestSucceeded(ctx, a.ID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 无成功基线（从未部署成功）：无可重放
			}
			// 存储故障不是「无基线」：单 App 失败不得成为永久自愈盲
			//（ADR-0082 账本纪律——扫描面「succeeded 无期望缓存 → 收敛
			// 触发」分支会在后续拍把本 App 纳入收敛重放，旧实现的裸
			// continue 与「下一扫描拍兜底」注释不符，此处把兜底做实：
			// 本拍跳过，期望缓存缺席由扫描面分支接手）。
			e.log.Error("baseline replay: latest succeeded", "app", a.ID, "err", err)
			continue
		}
		if e.opts.Converge.Posture == ConvergePostureDetect {
			if err := e.rebuildBaselineExpectations(ctx, a.ID, d); err != nil {
				e.log.Error("baseline expectations: rebuild", "app", a.ID, "err", err)
			}
			continue
		}
		e.replayAppBaseline(ctx, &a, d)
	}
}

// rebuildBaselineExpectations 在 posture=detect 下重建期望账本而不下发载体
// （ADR-0082 §5 第 2 条）：投影与缺席检测同源（expectedWorkloads =
// projectRevision + 代戳，同一 memo），只落归属 + ensuredSpec（期望 spec/
// 声明端口）+ ensuredBy（写入者 = 基线行）。不落 gen 锚，也不写 App 级
// expect.expected——wire-gen 只有 Ensure 回执可知，detect 不写就没有回执
// （gen/stopped 面成文失明，ADR-0082 §5 第 3 条）。
//
// Route 解析（routepublish.resolveBackend → obs.routeExpectations）与 peer
// 隔离不变式（obs.crossProjectRefs）都读 ensuredSpec——本函数的第二存在
// 理由：不重建则 detect 下用户路由全失、撤销 peer 连"该剥离"都算不出来。
// ensuredBy 必须写 d.ID：drift 的账本采用规则要求写入者 == 当前
// LatestSucceeded，否则 detect 下 spec 对照也失明（ADR-0082 §6）。
//
// 锁序与 replayAppBaseline 同口径：lockApp 内复查 teardown/驱动权——账本
// 重建与并发部署交错的窗口由同一互斥闭合（旧投影不覆盖新基线）。
func (e *Engine) rebuildBaselineExpectations(ctx context.Context, appID string, d *deployment.Deployment) error {
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()
	if e.converge.teardownMarked(appID) {
		return nil
	}
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		return err
	}
	if len(driving) > 0 {
		return nil // 驱动权在部署链：其 materialize 会写账本
	}
	ws, err := e.expectedWorkloads(ctx, d)
	if err != nil {
		return err
	}
	e.obs.recordProjectedSpecs(appOwner(appID), d.ID, ws)
	return nil
}

// replayAppBaseline 在 App 级互斥内复查活跃部署并重放单 App 基线：有活跃
// 即跳过（交给驱动器）；互斥与 Submit 共享，复查所见即终局。收敛动作走
// attemptConverge 统一原语（ADR-0082 §2）：与快/慢通道共用闸门序与冷却
// 记账（启动基线重放不受冷却豁免盲打）。posture=detect 下本函数仍受统一
// 门限（返回 convergeOutcomePostureDetect，零运行时写）——启动面在 detect
// 期不走本函数（rebuildBaselineExpectations 只重建账本）；本函数保留给
// all 态与 detect 期 peer 隔离重放，返回值供检测/隔离分支区分结局。
func (e *Engine) replayAppBaseline(ctx context.Context, a *app.App, d *deployment.Deployment) convergeOutcome {
	// 无签名触发（R02-M1）：基线/隔离重放不携带未收敛信号，冷却解除径②
	// 对它不适用——窗内重放只排队等窗满，不得凭空签名释放冷却。
	return e.attemptConverge(ctx, a.ID, d, convergeTrigger{}, convergeSourceBaseline)
}

// driftScan 一拍漂移扫描：spec 对照（RuntimeInspector 可用时）+ 稳态
// 看门狗 + 跨域附件隔离不变式（F1.8）。每 App 一次 Inspect（N0 小团队
// 规模）；错误逐 App 记日志不阻断。
func (e *Engine) driftScan(ctx context.Context) {
	inspector := capability.FacesOf(e.runtime).Inspector // spec 对照子面（FacesOf 协商点；nil = Provider 未提供）

	// 撤销 peer 的待剥离标记（R16-M4 异步语义）先于对照面执行：本拍剥离
	// 成功后期望账本已更新，后续 spec 对照/慢通道不会对同一次撤销做第二
	// 次重放（一拍一次）。失败保留下拍重试（失败可见在 drain 内）。
	e.drainPeerIsolation(ctx)

	// 在途 App 集（稳态判定：不在途才发 workload.stopped）。
	driving, err := e.repos.Deployments.ListDriving(ctx)
	if err != nil {
		e.log.Error("drift scan: list driving", "err", err)
		return
	}
	inFlight := make(map[string]bool, len(driving))
	for _, d := range driving {
		inFlight[d.AppID] = true
	}

	// 扫描面（ADR-0082 账本纪律）：A = 当前期望缓存（启动基线重放或部署
	// Ensure 建立）；B = 有 succeeded 基线但无期望缓存的存活 App——重启
	// 冷窗/基线重放单 App 失败不再永久自愈盲（「succeeded 无期望缓存 →
	// 收敛触发」，重建期望缓存本身是收敛动作）。spec 对照只走 App 域
	// owner——Database/受管域 owner 只入看门狗（expected 已登记），不进
	// App 表解析面（否则每拍一条噪声错；typed owner 域分派取代前缀匹配）。
	e.expect.mu.Lock()
	face := make([]string, 0, len(e.expect.expected))
	inFace := make(map[string]bool, len(e.expect.expected))
	for owner := range e.expect.expected {
		if owner.domain == ownerApp {
			face = append(face, owner.id)
			inFace[owner.id] = true
		}
	}
	e.expect.mu.Unlock()
	apps, err := e.repos.Apps.List(ctx)
	if err != nil {
		e.log.Error("drift scan: list apps", "err", err)
	} else {
		for _, a := range apps {
			if !inFace[a.ID] {
				face = append(face, a.ID)
				inFace[a.ID] = true
			}
		}
	}

	for _, appID := range face {
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
		scanCtx, cancel := context.WithTimeout(ctx, e.opts.Ring.ManagedStepTimeout)
		obs, err := inspector.InspectWorkloads(scanCtx, ns)
		cancel()
		if err != nil {
			e.log.Error("drift scan: inspect", "app", appID, "err", err)
			continue
		}
		// 基线锚（ADR-0082 §1：缺席检测与收敛的决策输入 = LatestSucceeded
		// Revision 拍内重投影，不是期望缓存）。无成功基线的 App 无可收敛
		// 锚——spec 对照照旧，缺席面与收敛跳过。
		d, derr := e.repos.Deployments.LatestSucceeded(ctx, appID)
		if derr != nil && !errors.Is(derr, state.ErrNotFound) {
			e.log.Error("drift scan: latest succeeded", "app", appID, "err", derr)
			continue
		}
		if derr != nil {
			d = nil
		}
		signals := e.compareSpecs(ctx, appID, obs, d)
		e.convergeStep(ctx, appID, d, signals)
	}

	// 稳态看门狗：观测槽里当前 Generation 的 stopped（去抖；回 running 清）。
	// 候选判定核在 domain/delivery/drift（SteadyStopCandidate，ADR-0087
	// 决策 7：期望锚在场 + App 域在途让位 + stopped@gen 匹配——非 App 域
	//〔Database/受管〕无在途概念恒可判；未观测载体传零值事件恒非候选）。
	type wlObs struct {
		wid string
		ev  capability.WorkloadEvent
	}
	expected := map[workloadOwner]uint64{}
	e.expect.mu.Lock()
	maps.Copy(expected, e.expect.expected)
	e.expect.mu.Unlock()

	var candidates []wlObs
	snap := e.obs.snapshot()
	for wid, owner := range snap.workloadApp {
		if drift.SteadyStopCandidate(expected[owner], owner.domain == ownerApp && inFlight[owner.id], snap.observations[wid]) {
			candidates = append(candidates, wlObs{wid: wid, ev: snap.observations[wid]})
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
// （签名含 spec 指纹去抖；同一失配不重复发，回归即清），并把六类收敛信号
// 圈定返回（ADR-0082 快通道：spec 失配/gen 偏离/停摆/缺席/多余/重放各自
// 独立签名键——事件面与收敛面在此分叉：事件去抖照旧，收敛信号交
// convergeStep 走签名生命周期）。判定核在 domain/delivery/drift
// （AdjudicateScan/MissingIDs/ExtraIDs，ADR-0087 决策 7 下沉——账本采用
// 规则、停摆/gen/spec 三面裁决与签名字面的单源）；本函数保留装配（取
// 期望账本 + 观测 + 基线锚）与执刑（memo 签名锁内 compare-and-set、事件
// 发射、收敛信号注册）。obsMu.RLock 只护缓存读——事件落账在锁外、经调用
// 链 ctx（关停可取消；N0.1 P2-10）。
//
// 镜像逐字比对口径的实证记录随判定核住在 domain/delivery/drift
// （mismatch 注释，2026-10-01 双腿实证，N0.1 P1-2）。
func (e *Engine) compareSpecs(ctx context.Context, appID string, obs []capability.WorkloadObservation, d *deployment.Deployment) convergeSignals {
	signals := convergeSignals{}
	// 非成功终态的重放标记（裁决表行 10）：取消/失败的部署从未成为期望态，
	// 下一拍以基线重放回卷（值携带部署 ID → 第二次取消 = 新签名，可再
	// 触发）。标记由 transition.transit 单前门在终态迁移时落下。
	if depID, ok := e.converge.replayMarked(appID); ok {
		signals[convergeSignalReplay.key("")] = "replay|" + depID
	}
	// 缺席/多余检测（ADR-0082 §1 行2/行8，同源同投影）：期望集 = 基线
	// Revision 拍内重投影，对照观测集。投影失败（如材料解析瞬断）跳过本拍：
	// 漏报不误报（方向安全），下拍重投影。
	if d != nil {
		ws, err := e.expectedWorkloads(ctx, d)
		if err != nil {
			e.log.Error("drift scan: project expected set", "app", appID, "err", err)
		} else {
			if missing := drift.MissingIDs(ws, obs); len(missing) > 0 {
				signals[convergeSignalAbsent.key("")] = "absent|" + strings.Join(missing, ",")
			}
			if extra := drift.ExtraIDs(ws, obs); len(extra) > 0 {
				signals[convergeSignalExtra.key("")] = "extra|" + strings.Join(extra, ",")
			}
		}
	}

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
	baselineID := ""
	if d != nil {
		baselineID = d.ID
	}
	for _, o := range obs {
		// 装配（锁内取事实）：期望账本三面 + 逐载体 gen 锚 → 域裁决。
		want, managed := e.obs.ensuredSpecOf(o.WorkloadID)
		writer, writerKnown := e.obs.ensuredByOf(o.WorkloadID)
		g, hasGen := e.obs.ensuredGenOf(o.WorkloadID)
		v := drift.AdjudicateScan(drift.ScanInput{
			Obs: o, Managed: managed, Want: want,
			LedgerWriter: writer, WriterKnown: writerKnown, BaselineID: baselineID,
			OwnerGen: expected, EnsuredGen: g, HasEnsuredGen: hasGen,
		})
		if v.Skip {
			continue // 非平台管辖 / 账本未采用（ADR-0082 §6）：零动作，漏报不误报
		}
		// 停摆面（workload.rollout_stalled，runbook 记录·二十七）：域裁决
		// 给签名，锁内 compare-and-set 去抖（同停摆不重复发，恢复即清）。
		// ADR-0082：停摆在终态后是收敛信号（重放途经 resumeStalledRollout）；
		// 部署窗内由收敛的驱动权复查闸门让位 L1 超时回滚既有语义。
		if v.RolloutClear {
			e.drift.mu.Lock()
			delete(e.drift.rolloutSig, o.WorkloadID)
			e.drift.mu.Unlock()
		}
		if v.RolloutSig != "" {
			signals[convergeSignalRollout.key(o.WorkloadID)] = v.RolloutSig
			e.drift.mu.Lock()
			if e.drift.rolloutSig[o.WorkloadID] != v.RolloutSig {
				e.drift.rolloutSig[o.WorkloadID] = v.RolloutSig
				rollout = append(rollout, pendingRollout{wid: o.WorkloadID, gen: uint64(o.Generation), detail: o.RolloutDetail})
			}
			e.drift.mu.Unlock()
		}
		// gen 面让位 spec 对照（ADR-0082 §1 行3：独立签名键 + 收敛信号；
		// 事件面仍归 Watch 流 detectDrift 专管，此处不重复发）。
		if v.GenSig != "" {
			signals[convergeSignalGen.key(o.WorkloadID)] = v.GenSig
			continue
		}
		if v.SpecClear {
			e.drift.mu.Lock()
			delete(e.drift.sig, o.WorkloadID)
			e.drift.mu.Unlock()
			continue
		}
		signals[convergeSignalSpec.key(o.WorkloadID)] = v.SpecSig
		e.drift.mu.Lock()
		if e.drift.sig[o.WorkloadID] == v.SpecSig {
			e.drift.mu.Unlock()
			continue
		}
		e.drift.sig[o.WorkloadID] = v.SpecSig
		e.drift.mu.Unlock()
		pending = append(pending, pendingDrift{
			wid:    o.WorkloadID,
			anchor: v.Anchor,
			ev: capability.WorkloadEvent{
				WorkloadID: o.WorkloadID, Generation: o.Generation,
				State:   capability.WorkloadDegraded,
				Message: "spec drift: " + v.SpecMismatch,
			},
		})
	}

	// 本扫描按 App 圈定（appID 在手）：资源轴 Team 一次解析，两类事件共用。
	team := e.eventTeamOfApp(ctx, e.repos, appID)
	for _, p := range pending {
		if err := e.emitObservation(ctx, team, eventWorkloadDrift, "workload", p.wid,
			driftEventPayloadJSON(p.ev, appOwner(appID), p.anchor)); err != nil {
			e.log.Error("drift scan: event", "workload", p.wid, "err", err)
		}
	}
	for _, p := range rollout {
		if err := e.emitObservation(ctx, team, eventWorkloadRolloutStall, "workload", p.wid,
			rolloutStalledEventPayloadJSON(p.wid, appID, p.gen, p.detail)); err != nil {
			e.log.Error("drift scan: rollout event", "workload", p.wid, "err", err)
		}
	}
	return signals
}

// emitSteadyStateStopped 发稳态 workload.stopped（去抖：签名 = stopped|<gen>
// 字面，域 StoppedSig 单源——wid 是 memo 键、state 恒为 stopped 均不入签；
// 观测回非 stopped 态〔running/degraded/failed/completed 等〕清签名）。
func (e *Engine) emitSteadyStateStopped(ctx context.Context, wid string, ev capability.WorkloadEvent) {
	sig := drift.StoppedSig(uint64(ev.Generation))
	e.drift.stoppedMu.Lock()
	if e.drift.stoppedSig[wid] == sig {
		e.drift.stoppedMu.Unlock()
		return
	}
	e.drift.stoppedSig[wid] = sig
	e.drift.stoppedMu.Unlock()
	// app_id 仅 App 域载体携带（typed owner 行为批）：内部路由键不再漏进
	// 用户可见载荷——数据库/受管载体的 stopped 事件不带伪 app_id。资源轴
	// Team 按归属域解析（P2 批二补齐，ADR-0066 A 化挂账收口）：app/
	// database 域各有解析面；managed/browse 是平台域——无 owner Team
	// （平台级事件，终态口径）。
	appID, team := "", ""
	if owner, ok := e.obs.ownerOf(wid); ok {
		if owner.domain == ownerApp {
			appID = owner.id
		}
		team = e.ownerTeam(ctx, owner)
	}
	if err := e.emitObservation(ctx, team, eventWorkloadStopped, "workload", wid,
		stoppedEventPayloadJSON(wid, appID, ev)); err != nil {
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
	e.driftLoop.Run(ctx, e.opts.Ring.DriftScanInterval, func(ctx context.Context) {
		e.driftScan(ctx)
	})
}
