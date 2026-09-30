package swarm

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Drain 实现 RuntimeAdmin 子面：节点排空（Availability=drain，存量按
// swarm 语义重调度；卷钉住的 Workload 因节点约束不被迁走——场景 5）。
func (p *Provider) Drain(ctx context.Context, nodeID string) error {
	return p.setNodeAvailability(ctx, nodeID, swarm.NodeAvailabilityDrain)
}

// Cordon 实现 RuntimeAdmin 子面：封锁节点（拒绝新调度，存量不动）。
func (p *Provider) Cordon(ctx context.Context, nodeID string) error {
	return p.setNodeAvailability(ctx, nodeID, swarm.NodeAvailabilityPause)
}

// Uncordon 解除封锁。
func (p *Provider) Uncordon(ctx context.Context, nodeID string) error {
	return p.setNodeAvailability(ctx, nodeID, swarm.NodeAvailabilityActive)
}

// setNodeAvailability 按平台节点 ID 定位载体节点并更新可用性。
func (p *Provider) setNodeAvailability(ctx context.Context, nodeID string, avail swarm.NodeAvailability) error {
	node, err := p.findNodeByPlatformID(ctx, nodeID)
	if err != nil {
		return err
	}
	spec := node.Spec
	spec.Availability = avail
	if _, err := p.cli.NodeUpdate(ctx, node.ID, client.NodeUpdateOptions{
		Version: node.Version,
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("swarm node %s availability %s: %w", nodeID, avail, err)
	}
	return nil
}

// findNodeByPlatformID 以平台节点 ID（label 锚）定位载体节点。
//
// 真机实证（staging 2026-09-30，docker 29.8）：node 列表的 label 服务端
// 过滤形态（label=k=v / node.labels.k=v）对含点号的键**恒不命中**且不
// 报错——不与服务过滤方言较劲，全量列出后内存精确匹配（集群节点数
// 量级小，全列可接受）。
func (p *Provider) findNodeByPlatformID(ctx context.Context, nodeID string) (*swarm.Node, error) {
	res, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return nil, fmt.Errorf("swarm node %s: list: %w", nodeID, err)
	}
	for i := range res.Items {
		if res.Items[i].Spec.Labels[labelNodeID] == nodeID {
			return &res.Items[i], nil
		}
	}
	return nil, fmt.Errorf("swarm node %s: no swarm node carries this platform node id: %w", nodeID, capability.ErrNodeNotFound)
}
