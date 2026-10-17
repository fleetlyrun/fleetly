package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// ErrRuntimeNoInspect 是 Runtime 未实现观测子面（RuntimeInspector）的哨兵
// （观测面缺席精确拒绝，不静默空集——ADR-0071 决策 1；engine 不 import
// api 包——依赖方向，见架构 §2，api 层 map 为错误码）。
var ErrRuntimeNoInspect = errors.New("runtime: no workload inspection face")

// projectRevision 装配一次 Revision 投影的全部输入并执行 Project——部署
// 物化（materialize）与只读渲染（RenderWorkloads）的投影前段单源
// （ADR-0071 决策 3：视图即部署管线的只读半边）。装配链只读侧表
// （team/builds/peers/egress），写半边（代戳 stamp/pin/Ensure/记账）归
// 调用方。isolate 语义随调用方：部署链恒 strict（release/rollback），
// 基线重放与渲染恒 isolate（剥离未批准 peer，view 永不因撤销中的引用
// 报错）。产物 digest 按 revisionID 解析（BG 切回重放 from_revision 起走
// 本参数——from_build 的镜像引用按被物化的 Revision 自身解析）。
func (e *Engine) projectRevision(ctx context.Context, appID, revisionID string, isolate bool) (*specv1.AppSpec, []capability.Workload, capability.NamespaceRef, error) {
	spec, err := e.loadSpec(ctx, revisionID)
	if err != nil {
		return nil, nil, capability.NamespaceRef{}, fmt.Errorf("load revision spec: %w", err)
	}
	team, appRow, err := e.appTeam(ctx, appID)
	if err != nil {
		return nil, nil, capability.NamespaceRef{}, fmt.Errorf("resolve app: %w", err)
	}
	peers, err := e.resolvePeerRefs(ctx, e.repos, spec.GetApp().GetProject(), spec, isolate)
	if err != nil {
		return nil, nil, capability.NamespaceRef{}, fmt.Errorf("resolve cross-project network peers: %w", err)
	}
	digests, err := e.buildDigests(ctx, revisionID)
	if err != nil {
		return nil, nil, capability.NamespaceRef{}, fmt.Errorf("resolve build digests: %w", err)
	}
	egress := e.egressNetworkMap(ctx, spec.GetApp().GetProject())
	ws, ns, err := Project(spec, team, appRow.Name, digests, peers, egress)
	if err != nil {
		return nil, nil, capability.NamespaceRef{}, fmt.Errorf("project spec: %w", err)
	}
	return spec, ws, ns, nil
}

// RenderWorkloads 按 Revision 只读重放 Workload 投影（ADR-0071 投影层）：
// seq=0 解析"当前生效"= 最新成功部署的 to_revision（无成功部署 →
// ErrNotFound 包装）。渲染近似语义（ADR-0071 决策 4）：历史 Revision 用
// 当前侧表重放——"现在重放会下发什么"而非考古；当前生效 Revision 与
// 在服实况一致（撤销进行中的收敛态除外）。卷钉住只读并入（已钉卷的
// 节点进 Placement；不 pinVolumes、零写库）；代戳不 stamp（渲染非下发，
// ADR-0064 代戳语义归 Ensure 与观测层）。
func (e *Engine) RenderWorkloads(ctx context.Context, appID string, seq int64) (*revision.Revision, []capability.Workload, error) {
	if appID == "" {
		return nil, nil, fmt.Errorf("app id must not be empty")
	}
	rev, projectID, err := e.resolveRenderRevision(ctx, appID, seq)
	if err != nil {
		return nil, nil, err
	}
	_, ws, _, err := e.projectRevision(ctx, appID, rev.ID, true)
	if err != nil {
		return nil, nil, err
	}
	if err := e.applyVolumePinning(ctx, ws, projectID); err != nil {
		return nil, nil, fmt.Errorf("merge volume pinning: %w", err)
	}
	return rev, ws, nil
}

// resolveRenderRevision 解析渲染寻址的 Revision 与所属 Project：显式 seq
// 直查；seq=0 = 当前生效（LatestSucceeded → to_revision，"最新冻结≠生效"
// 的分野锚——ADR-0071 决策 2）。
func (e *Engine) resolveRenderRevision(ctx context.Context, appID string, seq int64) (*revision.Revision, string, error) {
	var rev *revision.Revision
	if seq > 0 {
		r, err := e.repos.Revisions.GetBySeq(ctx, appID, seq)
		if err != nil {
			return nil, "", err
		}
		rev = r
	} else {
		d, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, "", fmt.Errorf("app %s has no succeeded deployment yet: %w", appID, state.ErrNotFound)
			}
			return nil, "", err
		}
		r, err := e.repos.Revisions.Get(ctx, d.ToRevision)
		if err != nil {
			return nil, "", err
		}
		rev = r
	}
	a, err := e.repos.Apps.Get(ctx, appID)
	if err != nil {
		return nil, "", err
	}
	return rev, a.ProjectID, nil
}

// InspectAppWorkloads 透出载体级在服观测（ADR-0071 观测层）：经
// RuntimeInspector 子面按 App 域观测，观测集按 WorkloadID 归属过滤
// （公式 appID-process；ns.App 是 Provider 侧的过滤锚，本过滤是对
// Provider 过量返回的防御）。Runtime 未实现子面 → 精确拒绝。
func (e *Engine) InspectAppWorkloads(ctx context.Context, appID string) ([]capability.WorkloadObservation, error) {
	if appID == "" {
		return nil, fmt.Errorf("app id must not be empty")
	}
	faces := capability.FacesOf(e.runtime)
	if faces.Inspector == nil {
		return nil, ErrRuntimeNoInspect
	}
	team, a, err := e.appTeam(ctx, appID)
	if err != nil {
		return nil, err
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: appID}
	obs, err := faces.Inspector.InspectWorkloads(ctx, ns)
	if err != nil {
		return nil, err
	}
	prefix := appID + "-"
	out := make([]capability.WorkloadObservation, 0, len(obs))
	for _, o := range obs {
		if strings.HasPrefix(o.WorkloadID, prefix) {
			out = append(out, o)
		}
	}
	return out, nil
}
