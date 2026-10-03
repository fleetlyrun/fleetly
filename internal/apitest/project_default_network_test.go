package apitest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestProjectBirthCreatesDefaultNetwork（F-C，2026-10-03 staging 实证）：
// 项目出生同事务建 default 网络行——compose 引用 networks:[default] 与受管
// Edge 挂靠真源（networks 表，activeProjectNetworks）保持同源，杜绝
// "swarm 侧 overlay 半物化 + 表无行 + traefik 永不挂靠 → 路由 502" 的
// 静默窗口。重复显式建同名网络仍被唯一约束拒绝（出生建行不开后门）。
func TestProjectBirthCreatesDefaultNetwork(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)

	list, err := networks.ListNetworks(ctx, &structurev1.ListNetworksRequest{ProjectId: proj.GetProject().GetId()})
	require.NoError(t, err)
	require.Len(t, list.GetNetworks(), 1, "project birth must seed exactly the default network row")
	assert.Equal(t, "default", list.GetNetworks()[0].GetName())
	assert.False(t, list.GetNetworks()[0].GetEgressNone())

	// 显式重复创建同名网络仍被拒（唯一约束不开口子）。
	_, err = networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{
		ProjectId: proj.GetProject().GetId(), Name: "default",
	})
	require.Error(t, err, "duplicate default network must be rejected by the project+name unique index")
}
