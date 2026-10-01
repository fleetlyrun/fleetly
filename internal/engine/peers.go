package engine

// 跨 Project 网络引用的投影翻译与撤销隔离（F1.8，ADR-0013 附录 A）。
//
// 解析真源：spec 的 project:<id>/<name> 引用 → 目标网络行 + peer 声明行
// 批准态 + 目标 Project 行团队（Team 轴接实联动）。两种模式（A.3）：
// strict（部署链）未批准即 ErrCrossProjectRefNotApproved；isolate（隔离
// 收敛）未批准即剥离。撤销隔离（A.4）：断存量——受影响 App 以 isolate
// 重投影重 Ensure（swarm service update 全量替换即断存量连接），隔离
// 生效时点 = 该次 Ensure 完成；漂移扫描拍持续复核（收敛不变式）。

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	specir "github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
)

// resolvePeerRefs 解析 spec 的全部跨 Project 引用（consumerProjectID 是
// 挂靠方项目——peer 声明的归属锚）。strict（isolate=false）：任一引用
// 未 approved → ErrCrossProjectRefNotApproved（fail-closed）；isolate：
// 未 approved 的引用缺席于结果（剥离）。存储错误恒上抛（isolate 也不吞
// ——读失败剥离等于假隔离）。
func (e *Engine) resolvePeerRefs(ctx context.Context, run state.Runner, consumerProjectID string, spec *specv1.AppSpec, isolate bool) (PeerRefs, error) {
	out := PeerRefs{Refs: map[string]capability.NetworkRef{}, Isolate: isolate}
	for _, p := range spec.GetProcesses() {
		for _, net := range p.GetNetworks() {
			if !specir.IsCrossProjectRef(net) {
				continue
			}
			if _, done := out.Refs[net]; done {
				continue // 同引用多进程共享一次解析
			}
			ref, approved, err := e.resolveOnePeerRef(ctx, run, consumerProjectID, net)
			if err != nil {
				return PeerRefs{}, err
			}
			if approved {
				out.Refs[net] = ref
			} else if !isolate {
				return PeerRefs{}, fmt.Errorf("%w: %s (receiving project has not approved this attachment; declare and approve the peer, or remove the reference)",
					ErrCrossProjectRefNotApproved, net)
			}
		}
	}
	return out, nil
}

// resolveOnePeerRef 解析单条跨 Project 引用：目标 Project 行（团队轴）→
// 目标网络行 → peer 声明批准态。approved=false 表"引用当前不可投影"
// （目标不存在/已删、网络无行、未声明/待批/已撤销）——存储错误另行上抛。
func (e *Engine) resolveOnePeerRef(ctx context.Context, run state.Runner, consumerProjectID, ref string) (capability.NetworkRef, bool, error) {
	projectID, name, ok := specir.CrossProjectRefParts(ref)
	if !ok {
		return capability.NetworkRef{}, false,
			fmt.Errorf("malformed cross-project reference %q (expected project:<project-id>/<network-name>)", ref)
	}
	target, err := e.projects.Get(ctx, run, projectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return capability.NetworkRef{}, false, nil
		}
		return capability.NetworkRef{}, false, fmt.Errorf("resolve target project %s: %w", projectID, err)
	}
	if target.Deleted() {
		return capability.NetworkRef{}, false, nil
	}
	net, err := e.networks.GetByName(ctx, run, projectID, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return capability.NetworkRef{}, false, nil
		}
		return capability.NetworkRef{}, false, fmt.Errorf("resolve network %s in project %s: %w", name, projectID, err)
	}
	peer, err := e.peerDecls.FindActive(ctx, run, net.ID, consumerProjectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return capability.NetworkRef{}, false, nil
		}
		return capability.NetworkRef{}, false, fmt.Errorf("resolve peer declaration for network %s: %w", net.ID, err)
	}
	if peer.State != networkpeer.StateApproved {
		return capability.NetworkRef{}, false, nil
	}
	return capability.NetworkRef{
		Namespace: capability.NamespaceRef{Team: target.TeamID, Project: projectID},
		Name:      name,
	}, true, nil
}

// CheckPeerRefs 是受理面的 strict 预检（Deploy admission，ADR-0013 附录
// A.3 双层拒绝的第一道）：spec 引用任一未 approved 的跨 Project 网络即
// 拒绝入队。Revision 读取经调用方事务 Runner（所见即受理终局）。
func (e *Engine) CheckPeerRefs(ctx context.Context, run state.Runner, consumerProjectID, revisionID string) error {
	rev, err := e.revisions.Get(ctx, run, revisionID)
	if err != nil {
		return err
	}
	spec, err := unmarshalSpec(rev.Spec)
	if err != nil {
		return err
	}
	_, err = e.resolvePeerRefs(ctx, run, consumerProjectID, spec, false)
	return err
}

// IsolateNetworkPeer 是撤销后的即时隔离（A.4）：对挂靠方项目下、当前
// 成功基线引用目标网络的 App 逐个 isolate 重收敛（在途部署的 App 跳过
// ——驱动器拥有 Ensure 权，其投影将 fail-closed 失败至终态）。撤销行
// 已由调用方落账；本方法失败不回滚撤销（受理面已生效），错误上抛由
// 调用方记录（漂移扫描拍兜底重试）。
func (e *Engine) IsolateNetworkPeer(ctx context.Context, networkID, peerProjectID string) error {
	net, err := e.networks.GetByID(ctx, e.db.Runner(), networkID)
	if err != nil {
		return fmt.Errorf("isolate peer: load network %s: %w", networkID, err)
	}
	target := crossProjectRefString(net.ProjectID, net.Name)
	apps, err := e.apps.ListByProject(ctx, e.db.Runner(), peerProjectID)
	if err != nil {
		return fmt.Errorf("isolate peer: list apps of project %s: %w", peerProjectID, err)
	}
	for i := range apps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), apps[i].ID)
		if err != nil {
			continue // 无成功基线：无可剥离的存量附件
		}
		spec, err := e.loadSpec(ctx, d.ToRevision)
		if err != nil {
			continue
		}
		if !specReferencesNetwork(spec, target) {
			continue
		}
		e.replayAppBaseline(ctx, &apps[i], d) // materialize 落点即 isolate 模式
	}
	return nil
}

// enforcePeerIsolation 是漂移扫描拍的隔离不变式（A.4 自愈兜底）：缓存中
// 带跨域附件的 App 复核每条附件的批准态，失配（已撤销/网络已删）即
// isolate 重收敛。附件快照在锁内取、批准态复核在锁外做（锁内无 DB 面）；
// 复核读失败不剥离（假隔离比迟隔离糟，下一拍重试）。
func (e *Engine) enforcePeerIsolation(ctx context.Context) {
	appRefs := map[string][]capability.NetworkRef{}
	e.obsMu.RLock()
	for wid, ensured := range e.ensuredSpec {
		if len(ensured.NetworkRefs) == 0 {
			continue
		}
		appID := e.workloadApp[wid]
		if appID == "" {
			continue
		}
		appRefs[appID] = append(appRefs[appID], ensured.NetworkRefs...)
	}
	e.obsMu.RUnlock()
	if len(appRefs) == 0 {
		return
	}

	var stale []*app.App
	staleDepl := map[string]*deployment.Deployment{}
	for appID, refs := range appRefs {
		if ctx.Err() != nil {
			return
		}
		bad := false
		a, err := e.apps.Get(ctx, e.db.Runner(), appID)
		if err != nil {
			continue
		}
		for _, ref := range refs {
			ok, err := e.peerRefApproved(ctx, a.ProjectID, ref)
			if err != nil {
				e.log.Error("peer isolation: check approval", "app", appID, "err", err)
				ok = true
			}
			if !ok {
				bad = true
				break
			}
		}
		if !bad {
			continue
		}
		d, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), appID)
		if err != nil {
			continue // 无基线却带附件：缓存残留，收敛面无从下手，跳过
		}
		stale = append(stale, a)
		staleDepl[appID] = d
	}
	for _, a := range stale {
		e.replayAppBaseline(ctx, a, staleDepl[a.ID])
	}
}

// peerRefApproved 复核一条已下发跨域附件的批准态（隔离扫描的单元判定）。
func (e *Engine) peerRefApproved(ctx context.Context, consumerProjectID string, ref capability.NetworkRef) (bool, error) {
	net, err := e.networks.GetByName(ctx, e.db.Runner(), ref.Namespace.Project, ref.Name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	peer, err := e.peerDecls.FindActive(ctx, e.db.Runner(), net.ID, consumerProjectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return peer.State == networkpeer.StateApproved, nil
}

// crossProjectRefString 合成 spec 引用串（隔离扫描的匹配锚）。
func crossProjectRefString(projectID, networkName string) string {
	return "project:" + projectID + "/" + networkName
}

// specReferencesNetwork 报告 spec 是否引用目标跨域网络。
func specReferencesNetwork(spec *specv1.AppSpec, target string) bool {
	for _, p := range spec.GetProcesses() {
		for _, net := range p.GetNetworks() {
			if net == target {
				return true
			}
		}
	}
	return false
}
