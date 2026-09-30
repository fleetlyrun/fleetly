package engine

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state/node"
)

// reconcileNodes 节点观测对账（F0.20 观测面）：DescribeCluster 快照 →
// nodes 表 upsert（观测缓存）；快照中消失的节点发 node.left 事件（F0.19
// Watch 流的 node 事件只覆盖加入路径，离开经周期对账检出——检出延迟 =
// 对账节拍）。
//
// node.left 去抖是实例态（e.nodeLeftSeen，C5：不再是包级全局）：节点
// 回归快照即清签名——同一节点再离开时再发（leave/join 循环不失真）；
// 控制面重启后可能重发一条（消费面按 node_id+状态幂等处理，诚实标注）。
func (e *Engine) reconcileNodes(ctx context.Context) {
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		e.log.Warn("node reconcile: describe cluster", "err", err)
		return
	}
	present := map[string]bool{}
	for _, n := range view.Nodes {
		present[n.NodeID] = true
		if e.nodeLeftSeen[n.NodeID] {
			delete(e.nodeLeftSeen, n.NodeID) // 回归清除：再离开可再发
		}
		if err := e.nodes.Upsert(ctx, e.db.Runner(), &node.Node{
			PlatformID: n.NodeID, CarrierID: n.CarrierID,
			Hostname: n.Hostname, Role: n.Role, Available: n.Available,
		}); err != nil {
			e.log.Error("node reconcile: upsert", "node", n.NodeID, "err", err)
		}
	}
	cached, err := e.nodes.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("node reconcile: list", "err", err)
		return
	}
	for _, n := range cached {
		if present[n.PlatformID] || e.nodeLeftSeen[n.PlatformID] {
			continue
		}
		e.nodeLeftSeen[n.PlatformID] = true
		if _, err := e.outbox.Append(ctx, e.db.Runner(), "node.left", "node", n.PlatformID,
			nodeLeftPayloadJSON(n.PlatformID, n.CarrierID)); err != nil {
			e.log.Error("node reconcile: node.left event", "node", n.PlatformID, "err", err)
			continue
		}
		// 离开即观测缓存下线（行保留——节点 ID 永不复用，历史可查）。
		if err := e.nodes.Upsert(ctx, e.db.Runner(), &node.Node{
			PlatformID: n.PlatformID, CarrierID: n.CarrierID, Hostname: n.Hostname,
			Role: n.Role, Available: false,
		}); err != nil {
			e.log.Error("node reconcile: mark unavailable", "node", n.PlatformID, "err", err)
		}
	}
}
