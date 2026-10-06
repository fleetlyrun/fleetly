package engine

// blue-green 编排变体（ADR-0048 决策 1/2）：Runtime 契约不变——双代窗是
// "期望集阶段性包含两代"的 engine 序列，不是 Runtime 子面。本文件是窗口
// 物化、代次标记与服务代推导的单一真源；相位入口在 drive.go（releasing
// 变体路径，状态枚举不变）。

import (
	"context"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// anyBlueGreen 报告 spec 是否含 blue-green 进程（Process 级策略——
// "web 蓝绿 + worker 滚动"的混合形态：窗口只圈 BG 进程的旧代，rolling
// 进程窗内即替换）。
func anyBlueGreen(spec *specv1.AppSpec) bool {
	for _, p := range spec.GetProcesses() {
		if p.GetStrategy() == specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN {
			return true
		}
	}
	return false
}

// genScopedWorkloadID 铸代次化 Workload ID（base + "-g<gen>"——ADR-0048
// 决策 1.4：双代窗两代各自独立载体/缓存键；rolling 名零变化）。公式
// 单源在此导出，散落拼接一旦漂移即静默失配（WorkloadID 同款纪律）。
func genScopedWorkloadID(base string, gen uint64) string {
	return fmt.Sprintf("%s-g%d", base, gen)
}

// stampDeploymentGenerations 把投影后的 Workload 集打上部署代标记：
// 全员 Generation = gen（载体 gen 标签/观测记录的逐载体锚）；blue-green
// 进程追加 GenerationScoped + 代次化 ID。rolling spec 全员 unscoped——
// 逐字节零漂移（存量零变化锚）。materialize/窗口/收口共用本真源，三路
// 载体身份不可能分叉。
func stampDeploymentGenerations(ws []capability.Workload, spec *specv1.AppSpec, gen uint64) {
	if gen == 0 {
		return
	}
	scoped := map[string]bool{}
	for _, p := range spec.GetProcesses() {
		if p.GetStrategy() == specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN {
			scoped[p.GetName()] = true
		}
	}
	for i := range ws {
		ws[i].Generation = gen
		if scoped[ws[i].Process] {
			ws[i].GenerationScoped = true
			ws[i].ID = genScopedWorkloadID(ws[i].ID, gen)
			// 代次名 {proc}.g{gen}（ADR-0048 决策 1.4）：只服务调试与跨进程
			// 引用的调试面——stable 名（裸名/全名）是主引用面；双代窗内
			// stable 别名 swarm DNS 轮询双代（方案①诚实边界），代次名可
			// 精确命中指定代。
			ws[i].Addressing = append(ws[i].Addressing, capability.Address{
				Name: fmt.Sprintf("%s.g%d", ws[i].Process, gen),
			})
		}
	}
}

// baselineGeneration 解析双代窗的旧代锚 gen：行上 FromGeneration（受理位
// 从最近 succeeded 行取）优先；列加入前的存量行回退 LatestSucceeded 实取
// （迁移零回填——首个 blue-green 部署起自然落列）；仍取不到（0）= 无基线
// 代，窗口退化为单代（首次部署形态）。
func (e *Engine) baselineGeneration(ctx context.Context, d *deployment.Deployment) uint64 {
	if d.FromGeneration > 0 {
		return d.FromGeneration
	}
	if d.FromRevision == "" {
		return 0
	}
	base, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), d.AppID)
	if err != nil || base.ToRevision != d.FromRevision {
		return 0
	}
	return base.Generation
}

// blueGreenWindowMaterialize 物化双代窗（releasing 前半的变体路径）：
// 单次 Ensure 期望集 = BG 进程的旧代成员 ∪ 全部新代成员——"期望集不含
// 即移除"的域内收敛语义天然不移除任何一方（ADR-0048 决策 1.2）。三锚：
//   - 旧代成员按 from_revision 重投影（确定性——缓存冷热无关）+ 基线 gen
//     标记 + 逐载体基线材料（旧代 secret 引用/凭证不被新代材料改写——
//     零扰动锚）；
//   - 新代成员按 to_revision 投影 + 本部署 gen 标记（调用 gen = from_gen，
//     新代显式覆写自身 gen——旧代标签不被调用 gen 翻新）；
//   - rolling 进程（to-spec 非 BG）只进新代——窗内即替换，联合集不含其
//     旧代（同名载体不两立）。
func (e *Engine) blueGreenWindowMaterialize(ctx context.Context, d *deployment.Deployment, toSpec *specv1.AppSpec, team, appName string) error {
	// 维护互斥读半边 + 全序列带界（materialize 同款纪律：Ensure 族与网络
	// 重建串行化；hang 不吃部署环）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	ctx, cancel := e.boundedStep(ctx)
	defer cancel()

	fromGen := e.baselineGeneration(ctx, d)
	if fromGen == 0 {
		fromGen = d.Generation // 无基线（首次部署）：退化为单代窗
	}

	peers, err := e.resolvePeerRefs(ctx, e.db.Runner(), toSpec.GetApp().GetProject(), toSpec, false)
	if err != nil {
		return fmt.Errorf("resolve cross-project network peers: %w", err)
	}

	var fromWS []capability.Workload
	if d.FromRevision != "" && fromGen != d.Generation {
		fromSpec, err := e.loadSpec(ctx, d.FromRevision)
		if err != nil {
			return fmt.Errorf("load baseline spec: %w", err)
		}
		fromDigests, err := e.buildDigests(ctx, d.AppID, d.FromRevision)
		if err != nil {
			return fmt.Errorf("resolve baseline build digests: %w", err)
		}
		fromPeers, err := e.resolvePeerRefs(ctx, e.db.Runner(), fromSpec.GetApp().GetProject(), fromSpec, false)
		if err != nil {
			return fmt.Errorf("resolve baseline cross-project network peers: %w", err)
		}
		fromWS, _, err = Project(fromSpec, team, appName, fromDigests, fromPeers)
		if err != nil {
			return fmt.Errorf("project baseline spec: %w", err)
		}
		stampDeploymentGenerations(fromWS, fromSpec, fromGen)
		// 基线材料：解析失败如实上抛（Q-8 同款——静默换材料面会让旧代
		// spec 漂移或丢 secret 引用，比部署失败更糟）。
		fromMaterials, err := e.resolveMaterials(ctx, fromSpec, fromSpec.GetApp().GetProject())
		if err != nil {
			return fmt.Errorf("resolve baseline materials: %w", err)
		}
		for i := range fromWS {
			fromWS[i].Materials = &fromMaterials
		}
	}

	digests, err := e.buildDigests(ctx, d.AppID, d.ToRevision)
	if err != nil {
		return fmt.Errorf("resolve build digests: %w", err)
	}
	newWS, ns, err := Project(toSpec, team, appName, digests, peers)
	if err != nil {
		return fmt.Errorf("project spec: %w", err)
	}
	stampDeploymentGenerations(newWS, toSpec, d.Generation)

	// 联合集：新代在前（Ensure 序即创建序——新代先落地，旧代成员随后
	// no-op 复核）；只圈 to-spec BG 进程的旧代。
	union := newWS
	if len(fromWS) > 0 {
		bg := map[string]bool{}
		for _, p := range toSpec.GetProcesses() {
			if p.GetStrategy() == specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN {
				bg[p.GetName()] = true
			}
		}
		for _, w := range fromWS {
			if bg[w.Process] {
				union = append(union, w)
			}
		}
	}

	materials, err := e.resolveMaterials(ctx, toSpec, toSpec.GetApp().GetProject())
	if err != nil {
		return fmt.Errorf("resolve materials: %w", err)
	}
	if err := e.pinVolumes(ctx, union, toSpec.GetApp().GetProject()); err != nil {
		return fmt.Errorf("pin volumes: %w", err)
	}
	if err := e.applyVolumePinning(ctx, union, toSpec.GetApp().GetProject()); err != nil {
		return fmt.Errorf("merge volume pinning: %w", err)
	}
	if err := e.runtime.Ensure(ctx, ns, union, capability.Generation(fromGen), materials); err != nil {
		return fmt.Errorf("runtime ensure: %w", err)
	}
	// 记账走 recordEnsured 单源（逐载体锚分组：旧代成员 from_gen、新代
	// 成员本部署 gen——L1 门 releaseReadyGen 按 gen 过滤天然只圈新代；
	// App 级 expect 锚 = from_gen = 服务侧代）。
	e.recordEnsured(d, fromGen, union)
	return nil
}

// failBlueGreen 收口新代 L1 失败（ADR-0048 决策 1.2"回滚零重建"）：
// Ensure 期望集 = 仅基线（新代载体被移除，旧代从未被触碰——无 Replay：
// rollback_attempted 落位，failed 即终态）。基线收口 Ensure 失败不上终态
// ——下一拍重试（终态先行会把新代残留留给无人收口的在途态）。
func (e *Engine) failBlueGreen(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error) {
	fromGen := e.baselineGeneration(ctx, d)
	if d.FromRevision != "" && fromGen > 0 {
		if err := e.materialize(ctx, d, d.FromRevision, fromGen, false); err != nil {
			return nil, err
		}
	}
	e.ensureForget(e.delivery.release, d.ID)
	return e.transitAndReload(ctx, d,
		deployment.ActiveStatesNoQueued(), deployment.StateFailed,
		func(m *deployment.Deployment) {
			m.Error, m.ObserveDeadline = reason, ""
			// 基线在服 = 回滚已由构造兑现（切流从未发生）；标记已尝试，
			// startRollback 不再铸 Replay（验收锚：无 Replay 发生）。
			if m.FromRevision != "" {
				m.RollbackAttempted = true
			}
		})
}

// servingGenerations 推导每 App 的在服代（ADR-0048 决策 1.2 切换步的
// 状态化：releasing→observing 迁移即切换——观测中的部署已把流量交给
// 本代；其余在途（含 failed 待回滚/rolling-back）服务代 = 基线代——
// 切回。部署行是唯一真源：重启/抢占/回放的翻转自洽，无内存第二真源。
// 无在途的 App 不进表（稳态单代，scoped 过滤退化全通过——stale 期望
// 条目无对应活载体，Addresses 不产端点）。ListDriving 按 id 升序：首见
// 即最老在途（无在途则最老 queued，其 FromRevision 同基线，值等价）。
func (e *Engine) servingGenerations(ctx context.Context) map[string]uint64 {
	driving, err := e.deployments.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("route publish: list driving for serving generations", "err", err)
		return nil
	}
	out := map[string]uint64{}
	for i := range driving {
		d := &driving[i]
		if _, seen := out[d.AppID]; seen {
			continue
		}
		gen := d.FromGeneration
		if d.State == deployment.StateObserving {
			gen = d.Generation
		}
		out[d.AppID] = gen
	}
	return out
}
