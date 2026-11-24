package swarm

// 节点锚定回归（2026-11-24）：未锚定载体不进集群视图。锚定扫描与视图
// 列举是两次调用，其间入场的节点以无标签形态在场——空平台 ID 进观测表
// 会落一行主键为空串的非法行，同载体的正规行随后被它抢先应答载体反查
//（本机 dind 实录：worker 刚加入即落幽灵行，31 秒后被标离场但行保留，
// 该节点的中继附着自此恒被拒）。

import (
	"context"
	"fmt"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDescribeClusterSkipsUnanchoredNodes：锚定扫描没看到、视图列举看到
// 的无标签节点不进视图；已锚定节点照常在视图里，铸造闭包不被调用。
func TestDescribeClusterSkipsUnanchoredNodes(t *testing.T) {
	d := newFakeDaemon()
	d.nodes = []swarm.Node{
		{ID: "carrier-1", Meta: swarm.Meta{Version: swarm.Version{Index: 3}},
			Spec: swarm.NodeSpec{Role: swarm.NodeRoleManager, Annotations: swarm.Annotations{Labels: map[string]string{labelNodeID: "node-1"}}}},
		{ID: "carrier-fresh", Meta: swarm.Meta{Version: swarm.Version{Index: 1}},
			Spec: swarm.NodeSpec{Role: swarm.NodeRoleWorker}},
	}
	p := &Provider{cli: d.newClient(t)}
	// 两次调用之间节点入场的形态：锚定扫描的列举（p.nodes 缝）漏看它，
	// 视图列举（cli.NodeList）看到它。
	p.nodeList = func(context.Context) (client.NodeListResult, error) {
		return client.NodeListResult{Items: []swarm.Node{d.nodes[0]}}, nil
	}
	var minted int
	p.SetNodeIDMinter(func(context.Context) (string, error) {
		minted++
		return fmt.Sprintf("node-%d", minted), nil
	})

	view, err := p.DescribeCluster(context.Background())
	require.NoError(t, err)
	require.Len(t, view.Nodes, 1, "a carrier without an anchoring label is not a platform node yet")
	assert.Equal(t, "node-1", view.Nodes[0].NodeID)
	assert.Equal(t, "carrier-1", view.Nodes[0].CarrierID)
	assert.Zero(t, minted, "the anchoring pass did not see the fresh carrier, so nothing is minted here")
	assert.Equal(t, 0, d.count("POST /nodes/carrier-fresh/update"))
}
