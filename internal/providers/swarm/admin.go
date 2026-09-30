package swarm

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
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
func (p *Provider) findNodeByPlatformID(ctx context.Context, nodeID string) (*swarm.Node, error) {
	res, err := p.cli.NodeList(ctx, client.NodeListOptions{
		Filters: client.Filters{}.Add("label", labelNodeID+"="+nodeID),
	})
	if err != nil {
		return nil, fmt.Errorf("swarm node %s: list: %w", nodeID, err)
	}
	if len(res.Items) == 0 {
		return nil, fmt.Errorf("swarm node %s: no swarm node carries this platform node id", nodeID)
	}
	return &res.Items[0], nil
}
