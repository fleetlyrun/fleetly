package engine

import (
	"context"
	"fmt"

	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// step 是收敛循环的单次推进（单写者；N0 全局一遍，单节点规模足够）：
// 拾取全部待驱动行 → 按 App 分组 → 组内驱动至多一条（在途优先，无在途
// 则最新 queued——latest-wins 已在 admission 合并，残余并发窗口在此收口）。
func (e *Engine) step(ctx context.Context) {
	driving, err := e.deployments.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("engine step: list driving", "err", err)
		return
	}
	byApp := map[string][]deployment.Deployment{}
	var order []string
	for _, d := range driving {
		if _, seen := byApp[d.AppID]; !seen {
			order = append(order, d.AppID)
		}
		byApp[d.AppID] = append(byApp[d.AppID], d)
	}
	for _, appID := range order {
		group := byApp[appID]
		var inFlight *deployment.Deployment
		for i := range group {
			if group[i].State != deployment.StateQueued {
				inFlight = &group[i]
				break // ListDriving 按 id 升序 → 首个非 queued 即最老在途
			}
		}
		if inFlight != nil {
			e.drive(ctx, inFlight)
			continue
		}
		latest := &group[len(group)-1]
		for _, older := range group[:len(group)-1] {
			if _, err := e.transitAndReload(ctx, &older,
				[]deployment.State{deployment.StateQueued}, deployment.StateSuperseded,
				func(m *deployment.Deployment) { m.SupersededBy = latest.ID }); err != nil {
				e.log.Error("engine step: latest-wins merge", "deployment", older.ID, "err", err)
			}
		}
		e.drive(ctx, latest)
	}
}

// drive 驱动单条 Deployment 至下一个需外部事件（观测/到期/构建）的状态。
// 同步可达的迁移在本调用内链式完成；ctx 取消即返回（优雅退出：当前
// Ensure 完成后不再推进）。
func (e *Engine) drive(ctx context.Context, d *deployment.Deployment) {
	for d != nil && ctx.Err() == nil {
		next, err := e.driveOnce(ctx, d)
		if err != nil {
			e.log.Error("engine drive", "deployment", d.ID, "state", d.State, "err", err)
			return
		}
		d = next
	}
}

// driveOnce 执行一次状态迁移，返回刷新后的行（nil = 到达等待点/终态）。
func (e *Engine) driveOnce(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	switch d.State {
	case deployment.StateQueued:
		return e.transitAndReload(ctx, d, []deployment.State{deployment.StateQueued}, deployment.StatePreparing, nil)

	case deployment.StatePreparing:
		return e.prepare(ctx, d)

	case deployment.StateBuilding:
		return e.driveBuilding(ctx, d)

	case deployment.StateReleasing:
		return e.release(ctx, d)

	case deployment.StateObserving:
		return e.observe(ctx, d)

	case deployment.StateFailed:
		return e.startRollback(ctx, d)

	case deployment.StateRollingBack:
		return e.rollback(ctx, d)

	default:
		// 终态行撞进 driving 集（列举与驱动的并发竞态：行在 list 与 drive
		// 之间被并发步骤收进终态）不是错误——停驱即可，下一拍列举自然
		// 剔除。staging 实证 2026-10-03：succeeded 行单次 error 噪音。
		// failed 有独立分支（回滚）；真正的非法值仍报错（状态机拼写出
		// 的新值宁可吵不可哑）。
		switch d.State {
		case deployment.StateSucceeded, deployment.StateSuperseded, deployment.StateCancelled:
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected driving state %s", d.State)
	}
}

// prepare：Spec 归一化校验 + 投影 + 材料装配（F0.17 材料解析随材料批次
// 接入，当前为空集）。镜像直投 → releasing；需构建 → building（build
// engine 拾取）。firstBootJobs 的结构校验在此先行（受理面 ValidateApp
// 同规则；此处是部署链内防御 + 更早失败：坏 job 不等构建完才死）——
// 执行在 releasing 态前半（firstboot.go，ADR-0030）。
func (e *Engine) prepare(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	spec, err := e.loadSpec(ctx, d.ToRevision)
	if err != nil {
		return e.failDeployment(ctx, d, "load revision spec: "+err.Error())
	}
	for i, j := range spec.GetFirstBootJobs() {
		if verr := specir.ValidateJob(fmt.Sprintf("app.first_boot_jobs[%d]", i), j); verr != nil {
			return e.failDeployment(ctx, d, "first boot job: "+verr.Error())
		}
	}
	team, _, err := e.appTeam(ctx, d.AppID)
	if err != nil {
		return e.failDeployment(ctx, d, "resolve app: "+err.Error())
	}
	// 受管仓库前置门（ADR-0019 附录 B.5①）：Build 声明存在的部署需要
	// 推送目标——无 Registry Provider 时在此精确失败，不进 building 走
	// 到一半才死（本机导入退化形态已裁决不做：裸 image ID 引用真机
	// swarm 不可拉取）。
	if spec.GetBuild() != nil && e.registry == nil {
		return e.failDeployment(ctx, d,
			"no registry provider wired; build-source deployments require the managed registry (set FLEETLY_REGISTRY_ADDR on the control plane)")
	}
	// 投影预检（strict）：from_build 无产物在有 Build 声明时合法（building
	// 态产出；releasing 前再次投影校验）；无 Build 声明的 from_build 是
	// 永久错误，此处精确失败。跨 Project 引用未 approved 同样 fail-closed
	//（ADR-0013 附录 A.3——受理面已拒一次，此处是部署链内的第二道）。
	digests, derr := e.buildDigests(ctx, d)
	if derr == nil && (digests != nil || spec.GetBuild() == nil) {
		peers, perr := e.resolvePeerRefs(ctx, e.db.Runner(), spec.GetApp().GetProject(), spec, false)
		if perr == nil {
			_, _, perr = Project(spec, team, digests, peers)
		}
		derr = perr
	}
	if derr != nil {
		return e.failDeployment(ctx, d, "project spec: "+derr.Error())
	}
	if spec.GetBuild() != nil {
		// Build 声明存在 → 构建链（building 态由 driveBuilding 驱动）。
		return e.transitAndReload(ctx, d, []deployment.State{deployment.StatePreparing}, deployment.StateBuilding, nil)
	}
	return e.transitAndReload(ctx, d, []deployment.State{deployment.StatePreparing}, deployment.StateReleasing, nil)
}

// release：jobs 子相位（firstBootJobs，ADR-0030——串行执行部署期 init
// job，失败即回滚；旧 Revision 载体原样在服）→ 物化（materialize 单序列）
// 下发 Runtime（唯一写动词 Ensure，同 Generation 重放安全）；L1 健康门 =
// 全部 Workload 观测到 running（gen 匹配）。
//
// L1 等待期（deadline 已设）历史每拍幂等重 Ensure：重启后观测缓存为空，
// 重放触发 Provider 的 update 事件恢复观测流（场景 1 重放语义）。N1 C17
// 起等待期物化走签名短路：行锚签名（revision+gen+相位——Revision 冻结体
// 不可变，行锚即内容锚）未变且未到强制重放节拍 → 跳过重物化（loadSpec 后
// 半的 peer 解析/Secret 解密/卷钉住/Ensure 全套）。语义保持三锚：重启备忘
// 丢失 → 首拍全量物化（观测流恢复路径不变，只是从每拍降到首拍）；签名外
// 输入漂移（Secret 轮换、peer 批准、卷钉住、环外载体变更）由
// ReconcileReplayInterval 强制重放兜底（滞后有界，终态同）；Ensure 失败
// 检测窗从每拍放宽到节拍（Task 域 ensureTaskWorkloads 先例同口径）。
// observe_deadline 在两子相位复用（job 等待 / L1），消歧真源 = first_boot
// 游标。
func (e *Engine) release(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	spec, err := e.loadSpec(ctx, d.ToRevision)
	if err != nil {
		return e.failDeployment(ctx, d, "load revision spec: "+err.Error())
	}
	jobsDone, err := e.driveFirstBootJobs(ctx, d, spec)
	if err != nil {
		return e.failDeployment(ctx, d, err.Error())
	}
	if !jobsDone {
		return nil, nil // 等 job 终态（tick 再进）
	}
	if e.releaseMaterializeFresh(d) {
		// 签名短路（C17）：等待期重物化跳过，观测门/超时门照常判定。
	} else if err := e.materialize(ctx, d, d.ToRevision, d.Generation, false); err != nil {
		return e.failDeployment(ctx, d, err.Error())
	} else {
		e.rememberReleaseMaterialize(d)
	}

	deadline := parseDeadline(d.ObserveDeadline)
	if deadline == nil {
		l1 := state.FormatTime(e.clock.Now().Add(e.opts.ReleaseTimeout))
		// 原地迁移（state 不变）：只落 L1 截止，四件一拍照走（无事件）。
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateReleasing}, deployment.StateReleasing,
			func(m *deployment.Deployment) { m.ObserveDeadline = l1 })
	}
	if e.releaseReady(d) {
		e.ensureForget(e.delivery.release, d.ID) // 离开 releasing：物化备忘随相位作废
		window := state.FormatTime(e.clock.Now().Add(e.opts.ObserveWindow))
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateReleasing}, deployment.StateObserving,
			func(m *deployment.Deployment) { m.ObserveDeadline = window })
	}
	if e.clock.Now().After(*deadline) {
		return e.failDeployment(ctx, d, "health gate L1 timed out waiting for workloads to become ready")
	}
	return nil, nil // 等观测或到期（tick 再进；等待期物化签名短路）
}

// releaseEnsureSignature 是 releasing 等待期物化的行锚签名（C17）：
// revision（冻结体不可变 → 行锚即内容锚）+ gen + 相位。签名未变 + 未到
// ReconcileReplayInterval → materialize 可跳过。
func releaseEnsureSignature(d *deployment.Deployment) string {
	return d.ToRevision + "\x00" + fmt.Sprint(d.Generation) + "\x00" + string(d.State)
}

// releaseMaterializeFresh 报告该 Deployment 的等待期物化可短路（C17）。
func (e *Engine) releaseMaterializeFresh(d *deployment.Deployment) bool {
	_, fresh := e.ensureFresh(e.delivery.release, d.ID, releaseEnsureSignature(d), e.clock.Now())
	return fresh
}

// rememberReleaseMaterialize 落等待期物化备忘（仅 materialize 成功后）。
func (e *Engine) rememberReleaseMaterialize(d *deployment.Deployment) {
	e.ensureRemember(e.delivery.release, d.ID, ensureMemo{
		sig: releaseEnsureSignature(d), gen: d.Generation, at: e.clock.Now(),
	})
}

// observe：L3 观察窗到期 → succeeded；L2 看门狗 = 当前 Generation 观测
// 劣化（stopped）→ 失败回滚。
func (e *Engine) observe(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	if bad := e.watchdogBite(d); bad != "" {
		return e.failDeployment(ctx, d, "health gate L2 watchdog: "+bad)
	}
	deadline := parseDeadline(d.ObserveDeadline)
	if deadline == nil {
		// 观察窗缺失（崩溃窗口/半途行）：补设观察窗——与 release 的 nil L1
		// 截止补设对称（原地迁移，state 不变无事件）。不补设则 nil 被读作
		// "窗内"，该行永久滞留 observing，无外部事件可收敛。
		window := state.FormatTime(e.clock.Now().Add(e.opts.ObserveWindow))
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateObserving}, deployment.StateObserving,
			func(m *deployment.Deployment) { m.ObserveDeadline = window })
	}
	if e.clock.Now().Before(*deadline) {
		return nil, nil // 观察窗内（tick 再进）
	}
	return e.transitAndReload(ctx, d,
		[]deployment.State{deployment.StateObserving}, deployment.StateSucceeded, nil)
}

// startRollback：failed 自动回滚入口（领域模型 §4：failed → (自动)
// rolling-back）。无 from_revision（首次部署）或回滚已尝试 → 终态不动。
func (e *Engine) startRollback(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	if d.FromRevision == "" || d.RollbackAttempted {
		return nil, nil
	}
	return e.transitAndReload(ctx, d,
		[]deployment.State{deployment.StateFailed}, deployment.StateRollingBack, nil)
}

// rollback：Revision Replay——重新下发 from_revision 的 Spec（永不编排器
// 原生回滚，ADR-0005；Ensure 域内收敛天然重建人工删除的载体，场景 2）。
// 物化走 materialize 单序列（与 release/基线重放同源）。
func (e *Engine) rollback(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error) {
	// Replay 用新 Generation 幂等重下发（单调编号；Drift 对照同步刷新）。
	// deadline 未设 = 首轮（gen 未推进）；已设 = 等待期（gen 已在行上）。
	// gen 在物化前取（物化失败不落行，号未持久化，重试同号无损）。
	deadline := parseDeadline(d.ObserveDeadline)
	gen := d.Generation
	if deadline == nil {
		var err error
		gen, err = e.deployments.NextGeneration(ctx, e.db.Runner(), d.AppID)
		if err != nil {
			return e.rollbackFailed(ctx, d, "next generation: "+err.Error())
		}
	}
	if err := e.materialize(ctx, d, d.FromRevision, gen, false); err != nil {
		return e.rollbackFailed(ctx, d, err.Error())
	}
	if e.releaseReadyGen(d, gen) {
		window := state.FormatTime(e.clock.Now().Add(e.opts.ObserveWindow))
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateRollingBack}, deployment.StateObserving,
			func(m *deployment.Deployment) {
				m.Generation = gen
				m.ObserveDeadline = window
				// 终态事实修正：实际运行的是回放目标（原失败目标留在 error 文本）。
				m.ToRevision = m.FromRevision
				m.Error = fmt.Sprintf("deployment failed (%s); rolled back to previous revision", m.Error)
			})
	}
	if deadline == nil {
		l1 := state.FormatTime(e.clock.Now().Add(e.opts.ReleaseTimeout))
		return e.transitAndReload(ctx, d,
			[]deployment.State{deployment.StateRollingBack}, deployment.StateRollingBack,
			func(m *deployment.Deployment) { m.Generation, m.ObserveDeadline = gen, l1 })
	}
	if e.clock.Now().After(*deadline) {
		return e.rollbackFailed(ctx, d, "rollback health gate timed out")
	}
	return nil, nil // 等回放就绪观测（tick 再进）
}

// rollbackFailed：回滚也失败 → failed 终态（rollback_attempted=1，不再
// 自动重试；显式 rollback 命令创建新 Deployment）。
func (e *Engine) rollbackFailed(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error) {
	return e.transitAndReload(ctx, d,
		[]deployment.State{deployment.StateRollingBack}, deployment.StateFailed,
		func(m *deployment.Deployment) {
			m.RollbackAttempted = true
			m.Error = m.Error + "; rollback failed: " + reason
		})
}

// failDeployment：任意阶段失败 → failed（四件一拍；自动回滚由 failed
// 分支接手）。ObserveDeadline 是相位局部截止（job 等待/L1/L3），进 failed
// 即终止该相位——残留会让 rollback 误读为"等待期已设、gen 已推进"
// （ADR-0030：job 等待截止不泄漏进回滚相位）。等待期物化备忘随相位终止
// 作废（C17）。
func (e *Engine) failDeployment(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error) {
	e.ensureForget(e.delivery.release, d.ID)
	return e.transitAndReload(ctx, d,
		deployment.ActiveStatesNoQueued(), deployment.StateFailed,
		func(m *deployment.Deployment) { m.Error, m.ObserveDeadline = reason, "" })
}
