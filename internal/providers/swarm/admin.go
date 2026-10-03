package swarm

import (
	"context"
	"fmt"
	"strings"

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
//
// 版本竞态有界重试（B14-3，mintNodeID 先例）：他方并发推进节点版本（同拍
// 锚定对账 DescribeCluster / 排空动词撞节点状态心跳）时 NodeUpdate 拒绝
// （"object was updated by another process"）——单次失败让 Drain/Cordon
// 语义整体落空且无自愈；重取节点再试（重试即正确：被拒的更新未生效），
// 重试耗尽仍败则原样上抛，调用方如实记录可重试。
func (p *Provider) setNodeAvailability(ctx context.Context, nodeID string, avail swarm.NodeAvailability) error {
	node, err := p.findNodeByPlatformID(ctx, nodeID)
	if err != nil {
		return err
	}
	if err := retryNodeVersionRace(ctx,
		func(ctx context.Context, n swarm.Node) error {
			// 每次重试都从传入节点的 spec 重建：NodeUpdate 全量替换
			// Spec，落到旧 spec 会踩掉他方并发改动。
			spec := n.Spec
			spec.Availability = avail
			_, err := p.cli.NodeUpdate(ctx, n.ID, client.NodeUpdateOptions{
				Version: n.Version,
				Spec:    spec,
			})
			return err
		},
		p.reloadNode,
		node,
	); err != nil {
		return fmt.Errorf("swarm node %s availability %s: %w", nodeID, avail, err)
	}
	return nil
}

// reloadNode 重取节点（版本竞态重试的版本刷新步：写前必须落到新鲜版本与
// spec）。
func (p *Provider) reloadNode(ctx context.Context, nodeID string) (swarm.Node, error) {
	inspect, err := p.cli.NodeInspect(ctx, nodeID, client.NodeInspectOptions{})
	if err != nil {
		return swarm.Node{}, err
	}
	return inspect.Node, nil
}

// retryNodeVersionRace 对节点更新做版本竞态有界重试：update 失败且判定为
// 版本竞态时 reload 重取节点再试，至多 3 次（mintNodeID 同款节律——重取
// 版本自带天然间隔，不加额外退避）；非竞态错误与重试耗尽原样上抛。
// update/reload 以函数值注入，重试形态不依赖真 daemon 即可 hermetic 钉测。
func retryNodeVersionRace(ctx context.Context, update func(context.Context, swarm.Node) error, reload func(context.Context, string) (swarm.Node, error), node *swarm.Node) error {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		err := update(ctx, *node)
		if err == nil {
			return nil
		}
		if !isNodeVersionRace(err) || attempt >= maxAttempts {
			return err
		}
		fresh, ierr := reload(ctx, node.ID)
		if ierr != nil {
			return fmt.Errorf("re-inspect node %s: %w", node.ID, ierr)
		}
		node = &fresh
	}
}

// isNodeVersionRace 识别 swarmkit 节点版本竞态：跨 API 边界无类型化哨兵，
// 按稳定文案匹配（isUpdateOutOfSequence 同款先例）——控制面对版本落后的
// 对象更新按路径返回 "update out of sequence"（ErrSequenceConflict）或并
// 发改写文案 "object was updated by another process"，两者同族（读到的版
// 本已被他方推进）。
func isNodeVersionRace(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "update out of sequence") ||
		strings.Contains(msg, "updated by another process")
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
