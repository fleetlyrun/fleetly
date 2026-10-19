package engine

// 跨 Project 网络引用的投影翻译与撤销隔离（F1.8，ADR-0013 附录 A）。
//
// 解析真源：spec 的 project:<id>/<name> 引用 → 目标网络行 + peer 声明行
// 批准态 + 目标 Project 行团队（Team 轴接实联动）。两种模式（A.3）：
// strict（部署链）未批准即 ErrCrossProjectRefNotApproved；isolate（隔离
// 收敛）未批准即剥离。撤销隔离（A.4）：断存量——受影响 App 以 isolate
// 重投影重 Ensure（swarm service update 全量替换即断存量连接），隔离
// 生效时点 = 该次 Ensure 完成；漂移扫描拍持续复核（收敛不变式）。
// posture=detect 下剥离是运行时写、不执行：重放被门限拦住时落 Warn，
// 恢复 all 后下一拍剥离（ADR-0082 §5 第 4 条）。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
)

// resolvePeerRefs 解析 spec 的全部跨 Project 引用（consumerProjectID 是
// 挂靠方项目——peer 声明的归属锚）。strict（isolate=false）：任一引用
// 未 approved → ErrCrossProjectRefNotApproved（fail-closed）；isolate：
// 未 approved 的引用缺席于结果（剥离）。存储错误恒上抛（isolate 也不吞
// ——读失败剥离等于假隔离）。
func (e *Engine) resolvePeerRefs(ctx context.Context, s transact.Stores, consumerProjectID string, spec *specv1.AppSpec, isolate bool) (PeerRefs, error) {
	out := PeerRefs{Refs: map[string]capability.NetworkRef{}, Isolate: isolate}
	resolve := func(net string) error {
		if !specir.IsCrossProjectRef(net) {
			return nil
		}
		if _, done := out.Refs[net]; done {
			return nil // 同引用多进程共享一次解析
		}
		ref, approved, err := e.resolveOnePeerRef(ctx, s, consumerProjectID, net)
		if err != nil {
			return err
		}
		if approved {
			out.Refs[net] = ref
		} else if !isolate {
			return fmt.Errorf("%w: %s (receiving project has not approved this attachment; declare and approve the peer, or remove the reference)",
				ErrCrossProjectRefNotApproved, net)
		}
		return nil
	}
	for _, p := range spec.GetProcesses() {
		for _, net := range p.GetNetworks() {
			if err := resolve(net); err != nil {
				return PeerRefs{}, err
			}
		}
	}
	// preDeployJobs 的挂靠网同域（ADR-0030 决策 7）：受理预检与本解析
	// 一并覆盖 job 引用（部署链恒 strict）。
	for _, j := range spec.GetPreDeployJobs() {
		for _, net := range j.GetProcess().GetNetworks() {
			if err := resolve(net); err != nil {
				return PeerRefs{}, err
			}
		}
	}
	return out, nil
}

// resolveOnePeerRef 解析单条跨 Project 引用：目标 Project 行（团队轴）→
// 目标网络行 → peer 声明批准态。approved=false 表"引用当前不可投影"
// （目标不存在/已删、网络无行、未声明/待批/已撤销）——存储错误另行上抛。
func (e *Engine) resolveOnePeerRef(ctx context.Context, s transact.Stores, consumerProjectID, ref string) (capability.NetworkRef, bool, error) {
	projectID, name, ok := specir.CrossProjectRefParts(ref)
	if !ok {
		return capability.NetworkRef{}, false,
			fmt.Errorf("malformed cross-project reference %q (expected project:<project-id>/<network-name>)", ref)
	}
	target, err := s.Projects.Get(ctx, projectID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return capability.NetworkRef{}, false, nil
		}
		return capability.NetworkRef{}, false, fmt.Errorf("resolve target project %s: %w", projectID, err)
	}
	if target.Deleted() {
		return capability.NetworkRef{}, false, nil
	}
	net, err := s.Networks.GetByName(ctx, projectID, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return capability.NetworkRef{}, false, nil
		}
		return capability.NetworkRef{}, false, fmt.Errorf("resolve network %s in project %s: %w", name, projectID, err)
	}
	peer, err := s.NetworkPeers.FindActive(ctx, net.ID, consumerProjectID)
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
func (e *Engine) CheckPeerRefs(ctx context.Context, s transact.Stores, consumerProjectID, revisionID string) error {
	rev, err := s.Revisions.Get(ctx, revisionID)
	if err != nil {
		return err
	}
	spec, err := unmarshalSpec(rev.Spec)
	if err != nil {
		return err
	}
	_, err = e.resolvePeerRefs(ctx, s, consumerProjectID, spec, false)
	return err
}

// IsolateNetworkPeer 是撤销后的隔离重收敛执行体（A.4）：对挂靠方项目下、
// 当前成功基线引用目标网络的 App 逐个 isolate 重收敛（在途部署的 App 跳过
// ——驱动器拥有 Ensure 权，其投影将 fail-closed 失败至终态）。撤销行已由
// 调用方落账；本方法失败不回滚撤销（受理面已生效），错误上抛由调用方记录
// （漂移扫描拍兜底重试）。
//
// 调用面（R16-M4）：**不是请求路径**——撤销 handler 只调 RequestPeerIsolation
// 标记 + Kick，剥离由下一拍漂移扫描在本方法上完成（请求不等待 N×30s 的
// 同步 replay）。测试可直调以钉同步语义。
//
// posture=detect 下剥离不执行（零运行时写，ADR-0082 §5 第 4 条）：隔离
// 重放被门限拦住时落一条 Warn——撤销仍已落账，detect 的承诺就是不动；
// 恢复 all 后由漂移扫描拍的隔离不变式在下一拍剥离。
func (e *Engine) IsolateNetworkPeer(ctx context.Context, networkID, peerProjectID string) error {
	net, err := e.repos.Networks.GetByID(ctx, networkID)
	if err != nil {
		return fmt.Errorf("isolate peer: load network %s: %w", networkID, err)
	}
	target := crossProjectRefString(net.ProjectID, net.Name)
	apps, err := e.repos.Apps.ListByProject(ctx, peerProjectID)
	if err != nil {
		return fmt.Errorf("isolate peer: list apps of project %s: %w", peerProjectID, err)
	}
	for i := range apps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := e.repos.Deployments.LatestSucceeded(ctx, apps[i].ID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 无成功基线：无可剥离的存量附件
			}
			// 存储故障不吞（B13）：吞掉 = 剥离静默跳过 = 假隔离——按包内
			// 原则恒上抛（调用方记录，漂移扫描拍兜底重试；撤销本身不回滚，
			// 受理面已生效）。NotFound 是唯一合法的"无基线可剥离"形态。
			return fmt.Errorf("isolate peer: load succeeded baseline of app %s: %w", apps[i].ID, err)
		}
		spec, err := e.loadSpec(ctx, d.ToRevision)
		if err != nil {
			continue
		}
		if !specReferencesNetwork(spec, target) {
			continue
		}
		if out := e.replayAppBaseline(ctx, &apps[i], d); out == convergeOutcomePostureDetect {
			e.warnPeerIsolationSkipped(apps[i].ID, net.Name)
		}
	}
	return nil
}

// peerIsolationRequest 是一条撤销后的待剥离标记（网络 + 挂靠方项目）。
type peerIsolationRequest struct {
	networkID     string
	peerProjectID string
}

// RequestPeerIsolation 标记一次撤销后的待剥离并唤醒漂移扫描环（R16-M4 的
// 异步语义）：撤销落账即返回，剥离由下一拍漂移扫描完成（IsolateNetworkPeer）。
// 标记驻内存：崩溃丢失时，漂移拍的隔离不变式（crossProjectRefs 复核批准态）
// 仍会在重启基线重放后兜底剥离——标记是及时性优化，不是正确性依赖。
func (e *Engine) RequestPeerIsolation(networkID, peerProjectID string) {
	if networkID == "" || peerProjectID == "" {
		return
	}
	e.peerIsolationMu.Lock()
	if e.peerIsolations == nil {
		e.peerIsolations = map[peerIsolationRequest]bool{}
	}
	e.peerIsolations[peerIsolationRequest{networkID: networkID, peerProjectID: peerProjectID}] = true
	e.peerIsolationMu.Unlock()
	if e.driftLoop != nil {
		e.driftLoop.Kick()
	}
}

// drainPeerIsolation 消费待剥离标记（漂移扫描拍单写者）。成功/网络行已删即
// 清标记；存储故障保留标记下拍重试（失败可见：Warn 带 network/project/err）。
// 网络行已删形态由隔离不变式按缓存附件兜底剥离——按 ID 已无从定位，保留
// 待办只会每拍重复同一条错误。
func (e *Engine) drainPeerIsolation(ctx context.Context) {
	e.peerIsolationMu.Lock()
	if len(e.peerIsolations) == 0 {
		e.peerIsolationMu.Unlock()
		return
	}
	pending := make([]peerIsolationRequest, 0, len(e.peerIsolations))
	for req := range e.peerIsolations {
		pending = append(pending, req)
	}
	e.peerIsolationMu.Unlock()
	for _, req := range pending {
		if ctx.Err() != nil {
			return
		}
		err := e.IsolateNetworkPeer(ctx, req.networkID, req.peerProjectID)
		if err == nil {
			e.clearPeerIsolation(req)
			continue
		}
		if errors.Is(err, state.ErrNotFound) {
			e.clearPeerIsolation(req)
			e.log.Warn("peer isolation deferred: network row is gone; the isolation invariant strips the attachment",
				"network", req.networkID, "project", req.peerProjectID)
			continue
		}
		e.log.Warn("peer isolation deferred to the next drift tick",
			"network", req.networkID, "project", req.peerProjectID, "err", err)
	}
}

// clearPeerIsolation 撤销一条待剥离标记（缺失为 no-op）。
func (e *Engine) clearPeerIsolation(req peerIsolationRequest) {
	e.peerIsolationMu.Lock()
	delete(e.peerIsolations, req)
	e.peerIsolationMu.Unlock()
}

// pendingPeerIsolationCount 返回待剥离标记数（测试/诊断面）。
func (e *Engine) pendingPeerIsolationCount() int {
	e.peerIsolationMu.Lock()
	defer e.peerIsolationMu.Unlock()
	return len(e.peerIsolations)
}

// enforcePeerIsolation 是漂移扫描拍的隔离不变式（A.4 自愈兜底）：缓存中
// 带跨域附件的 App 复核每条附件的批准态，失配（已撤销/网络已删）即
// isolate 重收敛。附件快照在锁内取、批准态复核在锁外做（锁内无 DB 面）；
// 复核读失败不剥离（假隔离比迟隔离糟，下一拍重试）。
func (e *Engine) enforcePeerIsolation(ctx context.Context) {
	appRefs := e.obs.crossProjectRefs()
	if len(appRefs) == 0 {
		return
	}

	var stale []*app.App
	staleDepl := map[string]*deployment.Deployment{}
	staleNetwork := map[string]string{}
	for appID, refs := range appRefs {
		if ctx.Err() != nil {
			return
		}
		bad := false
		a, err := e.repos.Apps.Get(ctx, appID)
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
		d, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
		if err != nil {
			continue // 无基线却带附件：缓存残留，收敛面无从下手，跳过
		}
		stale = append(stale, a)
		staleDepl[appID] = d
		staleNetwork[appID] = peerRefNames(refs)
	}
	for _, a := range stale {
		// detect 下剥离是运行时写：重放被门限拦住即 Warn（ADR-0082 §5 第 4
		// 条）。恢复 all 后本函数下一拍剥离（撤销已落账，不变式仍在）。
		if out := e.replayAppBaseline(ctx, a, staleDepl[a.ID]); out == convergeOutcomePostureDetect {
			e.warnPeerIsolationSkipped(a.ID, staleNetwork[a.ID])
		}
	}
}

// warnPeerIsolationSkipped 是 detect 下隔离重放被拦的可发现性面（ADR-0082
// §5 第 4 条）：撤销落账与剥离执行是两件事——detect 承诺不写载体，故剥离
// 延后到姿态恢复 all 的下一拍。Warn 让"该剥离而未剥离"可见。
func (e *Engine) warnPeerIsolationSkipped(appID, network string) {
	e.log.Warn("peer isolation skipped: convergence posture is detect (no runtime writes); lift the posture or freeze-lift to isolate",
		"app", appID, "network", network)
}

// peerRefNames 合成隔离附件的网络名串（Warn 的 network 面）。
func peerRefNames(refs []capability.NetworkRef) string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return strings.Join(names, ",")
}

// peerRefApproved 复核一条已下发跨域附件的批准态（隔离扫描的单元判定）。
func (e *Engine) peerRefApproved(ctx context.Context, consumerProjectID string, ref capability.NetworkRef) (bool, error) {
	net, err := e.repos.Networks.GetByName(ctx, ref.Namespace.Project, ref.Name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	peer, err := e.repos.NetworkPeers.FindActive(ctx, net.ID, consumerProjectID)
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
