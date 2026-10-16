package apitest

// NodesService.ListNodeContainers 的 API 面测试：protovalidate 锚点校验、
// 哨兵错误映射（未知节点 → E_NOT_FOUND）、归属行如实透传与诚实空。

import (
	"testing"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodesListContainers(t *testing.T) {
	h := New(t)
	ctx := h.Ctx()
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	// 空锚点在服务面即拒（protovalidate，与运维三动词同一文案）。
	_, err := nodes.ListNodeContainers(ctx, &runtimev1.ListNodeContainersRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node_id")

	// 平台节点 ID 对不上任何载体节点 → E_NOT_FOUND（假底座键缺席）。
	_, err = nodes.ListNodeContainers(ctx,
		&runtimev1.ListNodeContainersRequest{NodeId: "01JMISSING0000000000000000"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// 归属回显：布置行集后如实透传（四域投影契约的字段面；行序真源在
	// provider 侧——假面不重排）。
	h.Runtime.SetNodeContainers("node-1", []capability.NodeContainerObservation{
		{Instance: "task-b", WorkloadID: "web-1", Domain: capability.NodeDomainApp,
			Team: "acme", Project: "shop", AppID: "web", Process: "api", Image: "nginx:1.27"},
		{Instance: "task-a", WorkloadID: "db1", Domain: capability.NodeDomainDatabase, Image: "pg:16"},
	})
	resp, err := nodes.ListNodeContainers(ctx, &runtimev1.ListNodeContainersRequest{NodeId: "node-1"})
	require.NoError(t, err)
	require.Len(t, resp.GetContainers(), 2)
	first, second := resp.GetContainers()[0], resp.GetContainers()[1]
	assert.Equal(t, "task-b", first.GetInstance())
	assert.Equal(t, "web-1", first.GetWorkloadId())
	assert.Equal(t, capability.NodeDomainApp, first.GetDomain())
	assert.Equal(t, "acme", first.GetTeam())
	assert.Equal(t, "shop", first.GetProjectId())
	assert.Equal(t, "web", first.GetAppId())
	assert.Equal(t, "api", first.GetProcess())
	assert.Equal(t, "nginx:1.27", first.GetImage())
	assert.Equal(t, capability.NodeDomainDatabase, second.GetDomain())
	assert.Empty(t, second.GetAppId(), "non-app domains carry no app attribution")

	// 节点在场无在跑容器 → 诚实空（空集非错误）。
	h.Runtime.SetNodeContainers("node-1", nil)
	resp, err = nodes.ListNodeContainers(ctx, &runtimev1.ListNodeContainersRequest{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetContainers())
}
