package engine

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
)

// reconcileNodes 节点观测对账（F0.20 观测面）：DescribeCluster 快照 →
// nodes 表 upsert（观测缓存）；快照中消失的节点发 node.left 事件（F0.19
// Watch 流的 node 事件只覆盖加入路径，离开经周期对账检出——检出延迟 =
// 对账节拍）。
//
// node.left 去抖是实例态（e.nodeLeftSeen，C5：不再是包级全局）：节点
// 回归快照即清签名——同一节点再离开时再发（leave/join 循环不失真）；
// 控制面重启后可能重发一条（消费面按 node_id+状态幂等处理，诚实标注）。
//
// S11b 宽限窗（e.nodeAbsentSince + LeaveGrace）：单拍缺行不再等价于离场
// ——编排器快照偶发抖动（manager 短窗失联/快照竞态）会误删个体 relay
// 凭证并断连，把可自愈的观测抖动升级成事故。只有**连续缺失达到
// LeaveGrace**（引擎时钟；默认 30s，约 30 拍）才执行安全清理；窗口内
// 节点回归即清零，不删凭证、不断连、不发事件（离线标记也不落——节点
// 只是暂时没出现在快照里，不是离场）。
//
// H10 §4.1/§4.9：节点离开先清安全材料（个体 relay 凭证行 + 在连中继），
// 清理独立于 node.left 事件发射成功与否——先清材料，事件失败只记日志。
// 事件 continue 不得把清理/下线挂在其后（旧形态会在事件故障时留下可用
// 凭证与 stale 可用行）。
func (e *Engine) reconcileNodes(ctx context.Context) {
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		e.log.Warn("node reconcile: describe cluster", "err", err)
		return
	}
	present := map[string]bool{}
	for _, n := range view.Nodes {
		present[n.NodeID] = true
		// 回归清除：宽限计时与 node.left 去抖签名一并归零（再离开可再发）。
		delete(e.nodeAbsentSince, n.NodeID)
		delete(e.nodeLeftSeen, n.NodeID)
		if err := e.repos.Nodes.Upsert(ctx, &node.Node{
			PlatformID: n.NodeID, CarrierID: n.CarrierID,
			Hostname: n.Hostname, Role: n.Role, Available: n.Available,
		}); err != nil {
			e.log.Error("node reconcile: upsert", "node", n.NodeID, "err", err)
		}
	}
	cached, err := e.repos.Nodes.List(ctx)
	if err != nil {
		e.log.Error("node reconcile: list", "err", err)
		return
	}
	now := e.clock.Now()
	for _, n := range cached {
		if present[n.PlatformID] || e.nodeLeftSeen[n.PlatformID] {
			continue
		}
		// 宽限窗：首拍缺行只起算，不执行清理（节点回归即清零）。
		since, tracked := e.nodeAbsentSince[n.PlatformID]
		if !tracked {
			e.nodeAbsentSince[n.PlatformID] = now
			continue
		}
		if now.Sub(since) < e.opts.Node.LeaveGrace {
			continue
		}
		delete(e.nodeAbsentSince, n.PlatformID)
		e.nodeLeftSeen[n.PlatformID] = true
		// 安全材料先于事实（H10）：凭证删除/断连/审计失败各自容忍，不阻
		// 断后续留痕与观测缓存下线。
		e.revokeDepartedNodeRelay(ctx, n)
		// 平台级事件（node 聚合无 Team 轴）：team 留 '' = 全员可见。
		if err := e.emitObservation(ctx, "", "node.left", "node", n.PlatformID,
			nodeLeftPayloadJSON(n.PlatformID, n.CarrierID)); err != nil {
			e.log.Error("node reconcile: node.left event", "node", n.PlatformID, "err", err)
		}
		// 离开即观测缓存下线（行保留——节点 ID 永不复用，历史可查）。事件
		// 失败仍下线：观测缓存与事件面各自容错（nodes 表非权威）。
		if err := e.repos.Nodes.Upsert(ctx, &node.Node{
			PlatformID: n.PlatformID, CarrierID: n.CarrierID, Hostname: n.Hostname,
			Role: n.Role, Available: false,
		}); err != nil {
			e.log.Error("node reconcile: mark unavailable", "node", n.PlatformID, "err", err)
		}
	}
}

// revokeDepartedNodeRelay 清理已离开节点的中继安全材料（H10 §4.1/§4.9）：
// 删个体凭证行（仅实际删行才落 node.relay_credential_revoked 审计，Detail
// 带 carrier_id 与 reason="node_left"）并断开在连中继。删除失败也断连
// （连接本身已无可服务对象；重连仍受同一凭证行约束，错误如实记日志）。
func (e *Engine) revokeDepartedNodeRelay(ctx context.Context, n node.Node) {
	deleted, err := e.repos.Nodes.DeleteRelayCredential(ctx, n.PlatformID)
	if err != nil {
		e.log.Error("node reconcile: delete relay credential", "node", n.PlatformID, "err", err)
	}
	// 先让凭证失效再断连接（rotate 同序）；无行可删时断连仍是幂等清理。
	e.DropRelay(n.PlatformID)
	if !deleted {
		return
	}
	if err := e.emitLedger(ctx, "node.relay_credential_revoked", "node/"+n.PlatformID,
		auditDetailJSON(map[string]string{"carrier_id": n.CarrierID, "reason": "node_left"})); err != nil {
		e.log.Error("node reconcile: relay credential revoked audit", "node", n.PlatformID, "err", err)
	}
}
