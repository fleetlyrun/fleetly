package apitest_test

// 节点观测读面的平台管理员门（R08-M1，S16-c7）：节点无 Team 轴——读面与
// 写动词同门（ListNodes/ListNodeContainers 均 RequirePlatformAdmin），门先于
// 子面协商与节点存在性（403 不带存在性泄露）。member / 非平台 admin 一律
// E_FORBIDDEN；平台 owner 正向可用（列表 + 在跑容器归属透传）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestNodesReadSurfacePlatformAdminOnly(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	nodes := runtimev1.NewNodesServiceClient(h.Conn)

	// ---- 平台 owner 正例：节点列表可读（对账面既有 TestNodesAndEnroll） ----
	list, err := nodes.ListNodes(owner, &runtimev1.ListNodesRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, list.GetNodes())

	// ---- 负例：非平台身份两种（他队 admin / default 队普通 member） ----
	acmeTeamID, otherTeamAdmin := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")
	createUserAs(t, h, "acme-member", acmeTeamID, identity.RoleMemberID, "acme-member-pass")
	memberCtx, _ := loginSession(t, h, "acme-member", "acme-member-pass")
	createUserAs(t, h, "default-member", identity.DefaultTeamID, identity.RoleMemberID, "default-member-pass")
	defaultMemberCtx, defaultWho := loginSession(t, h, "default-member", "default-member-pass")
	require.False(t, defaultWho.GetPlatformAdmin(), "a plain member of the default team is not a platform admin")

	for name, ctx := range map[string]context.Context{
		"other-team admin":                        otherTeamAdmin,
		"other-team member":                       memberCtx,
		"default-team member (read scope holder)": defaultMemberCtx,
	} {
		t.Run(name, func(t *testing.T) {
			// 读面门先于节点存在性与子面协商：任意 node_id 都只能得到 403。
			_, err := nodes.ListNodes(ctx, &runtimev1.ListNodesRequest{})
			wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
			_, err = nodes.ListNodeContainers(ctx, &runtimev1.ListNodeContainersRequest{NodeId: "node-1"})
			wantCode(t, err, codes.PermissionDenied, "E_FORBIDDEN")
		})
	}

	// ---- 平台 owner 容器读面正例：归属行如实透传 ----
	h.Runtime.SetNodeContainers("node-1", []capability.NodeContainerObservation{
		{Instance: "task-b", WorkloadID: "web-1", Domain: capability.NodeDomainApp,
			Team: "acme", Project: "shop", AppID: "web", Process: "api", Image: "nginx:1.27"},
	})
	resp, err := nodes.ListNodeContainers(owner, &runtimev1.ListNodeContainersRequest{NodeId: "node-1"})
	require.NoError(t, err)
	require.Len(t, resp.GetContainers(), 1)
	assert.Equal(t, "acme", resp.GetContainers()[0].GetTeam())
	assert.Equal(t, "web", resp.GetContainers()[0].GetAppId())
}
