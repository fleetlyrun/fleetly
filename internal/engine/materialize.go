package engine

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// materialize 把一条 Revision 的期望态物化到 Runtime：装料（loadSpec/
// appTeam/buildDigests）→ 投影（Project）→ 材料（resolveMaterials）→
// 卷钉住（pinVolumes + applyVolumePinning）→ Ensure → 记账（recordEnsured）。
//
// 三处消费面（release / rollback / 基线重放）的唯一序列真源：此前三份
// 手写拷贝已在 pinVolumes 上分叉过一次（rollback 与重放漏钉——今日良性
// 仅因成功部署引用的卷必然已钉，且该论证跨三文件无处记录）；收口后任何
// 步序增删只可能出现在本文件，三路差异在一个 diff 里显形。性质测试
// TestMaterializeThreePathsIdentical 钉死三路下发一致。
//
// 幂等：同 Generation 重放安全（Ensure 幂等；pinVolumes 只钉未钉卷——
// 成功基线引用的卷必然已钉，重放路径空转，不触发 DescribeCluster）。
//
// isolate（ADR-0013 附录 A.3/A.4）：跨 Project 引用未 approved 时剥离
// 而非失败——撤销隔离收敛与基线重放用（隔离是收敛不变式：重启/失败后
// 由漂移扫描拍与重放持续重申）。部署链（release/rollback/prepare）恒
// strict。
func (e *Engine) materialize(ctx context.Context, d *deployment.Deployment, revision string, gen uint64, isolate bool) error {
	// 维护互斥读半边（ADR-0046）：Ensure 族与网络重建的串行化锚。锁获取
	// 在带界 ctx 派生之前——锁等待是排队语义，不占步预算。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()

	// 全序列带界（boundedStep，Options.ManagedStepTimeout 的实证背景）：
	// Ensure 与其上游 DescribeCluster（卷钉住）都跑在部署单写者环上，无界
	// hang 即卡死部署收敛。收口在序列唯一真源处（本函数头），release/
	// rollback/基线重放三消费面统一覆盖，后增消费面不可遗漏。
	ctx, cancel := e.boundedStep(ctx)
	defer cancel()

	spec, err := e.loadSpec(ctx, revision)
	if err != nil {
		return fmt.Errorf("load revision spec: %w", err)
	}
	team, appRow, err := e.appTeam(ctx, d.AppID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	peers, err := e.resolvePeerRefs(ctx, e.db.Runner(), spec.GetApp().GetProject(), spec, isolate)
	if err != nil {
		return fmt.Errorf("resolve cross-project network peers: %w", err)
	}
	// 产物按被物化的 revision 解析（BG 切回重放 from_revision 起走本参数
	// ——此前钉 d.ToRevision 是回放路径的存量错源，回放 from_build spec
	// 会拿目标 revision 的产物映射）。
	digests, err := e.buildDigests(ctx, d.AppID, revision)
	if err != nil {
		return fmt.Errorf("resolve build digests: %w", err)
	}
	egress := e.egressNetworkMap(ctx, spec.GetApp().GetProject())
	ws, ns, err := Project(spec, team, appRow.Name, digests, peers, egress)
	if err != nil {
		return fmt.Errorf("project spec: %w", err)
	}
	// 部署代标记（ADR-0048：全员逐载体 gen 锚 + blue-green 进程的代次化
	// ID/命名位——rolling spec 全员 unscoped，逐字节零漂移）。release/
	// rollback/基线重放/收口共用（stampDeploymentGenerations 单源）。
	stampDeploymentGenerations(ws, spec, gen)
	materials, err := e.resolveMaterials(ctx, spec, spec.GetApp().GetProject())
	if err != nil {
		return fmt.Errorf("resolve materials: %w", err)
	}
	if err := e.pinVolumes(ctx, ws, spec.GetApp().GetProject()); err != nil {
		return fmt.Errorf("pin volumes: %w", err)
	}
	if err := e.applyVolumePinning(ctx, ws, spec.GetApp().GetProject()); err != nil {
		return fmt.Errorf("merge volume pinning: %w", err)
	}
	if err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(gen), materials); err != nil {
		return fmt.Errorf("runtime ensure: %w", err)
	}
	e.recordEnsured(d, gen, ws)
	return nil
}
