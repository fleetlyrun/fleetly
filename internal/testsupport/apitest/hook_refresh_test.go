package apitest_test

// RefreshGitHook 拉取通道（hook 材料摄取核的 pull 孪生：控制面拉取部署，
// 不依赖 GitHub webhook 配置——App Templates 第三方模板仓的前提原语）+
// hook 行随 App 删除级联（孤儿 hook 真机咬点：配置面对已删 App 继续成功、
// 接收面却按活跃行拒绝的两面分裂）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGitHookRefreshPullChannel(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "hookpull"})
	require.NoError(t, err)
	a, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := a.GetApp().GetId()

	// 未配 hook 即拉取 = 精确 404（hook 行缺失）。
	_, err = hooks.RefreshGitHook(ctx, &deliveryv1.RefreshGitHookRequest{AppId: appID})
	require.Equal(t, codes.NotFound, status.Code(err))

	_, err = hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)

	// 显式 commit 拉取（测试锚——commit 非空即跳过 ls-remote 现解析，不触
	// 网；HEAD 解析面在 engine 单测以本地夹具覆盖）：部署创建 + commit 回
	// 显 + 行真实在场。
	const commit = "1111111111111111111111111111111111111111"
	first, err := hooks.RefreshGitHook(ctx, &deliveryv1.RefreshGitHookRequest{AppId: appID, Commit: commit})
	require.NoError(t, err)
	require.NotEmpty(t, first.GetDeploymentId())
	require.Equal(t, commit, first.GetCommit())
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, list.GetDeployments(), 1)
	require.Equal(t, first.GetDeploymentId(), list.GetDeployments()[0].GetId())

	// 同 commit 二刷：admission deduplicated 回同一部署引用（与 push 同链
	// 的 CommitSHA 去重）。
	second, err := hooks.RefreshGitHook(ctx, &deliveryv1.RefreshGitHookRequest{AppId: appID, Commit: commit})
	require.NoError(t, err)
	require.Equal(t, first.GetDeploymentId(), second.GetDeploymentId())
	assert.Equal(t, "deduplicated", second.GetAdmission().GetOutcome())
}

func TestGitHookCascadeOnAppDelete(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)

	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "hookgone"})
	require.NoError(t, err)
	a, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := a.GetApp().GetId()

	_, err = hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.NoError(t, err)

	// 级联 + 活跃行执法：配置面三动词对已删 App 即刻 404（真机咬点——
	// 孤儿 hook 时代 rotate 在已删 App 上成功、webhook 却 401 的两面分裂）。
	_, err = hooks.GetGitHook(ctx, &deliveryv1.GetGitHookRequest{AppId: appID})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = hooks.RotateHookToken(ctx, &deliveryv1.RotateHookTokenRequest{AppId: appID})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = hooks.RefreshGitHook(ctx, &deliveryv1.RefreshGitHookRequest{AppId: appID})
	require.Equal(t, codes.NotFound, status.Code(err))
}
