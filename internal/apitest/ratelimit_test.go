package apitest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestCreateRateLimitEnforcement（ADR-0017 附录 A.2，F1.9）：拦截器链接线
// 证据——bootstrap Token 的创建型动词在固定窗预算（缺省 120）耗尽后收
// E_RATE_LIMITED；读面不受限。计数从 harness 建立即开始（含本测试自身的
// 前置创建），故采用"灌到拒"的圈界形态（< 130 圈）而非精确计数断言。
func TestCreateRateLimitEnforcement(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "ratelimit"})
	require.NoError(t, err)
	pid := proj.GetProject().GetId()

	var lastErr error
	for i := 0; i < 130; i++ {
		_, lastErr = volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{
			ProjectId: pid, Name: fmt.Sprintf("v%03d", i),
		})
		if lastErr != nil {
			break
		}
	}
	require.Error(t, lastErr, "the default 120-per-window budget must exhaust inside the loop bound")
	require.Contains(t, lastErr.Error(), "E_RATE_LIMITED")

	// 限的是创建型动词：读面照常（含被限的同一 Token）。
	_, err = volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: pid})
	require.NoError(t, err)
}
