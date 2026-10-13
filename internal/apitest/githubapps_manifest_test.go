package apitest_test

// Manifest 自动建流三步全链（ADR-0073 决策 8）：start（签名 state + POST
// 表单材料）→ consume（一次性 code 换凭据暂存，安装页 URL）→ complete
// （installation_id 落行 + nonce 单用）。假 GitHub 的 conversions 端点
// 单次有效；PEM 全程不出现在任何读面。

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGitHubAppManifestFlow(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	ghApps := deliveryv1.NewGitHubAppsServiceClient(h.Conn)

	const base = "https://console.dev.fleetly.run"
	fake := newFakeGitHub(t, h)
	h.Services.GitHubManifestBase = base
	h.Services.GitHubManifestAPIBase = fake.srv.URL
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "manifest"})
	require.NoError(t, err)
	projectID := p.GetProject().GetId()

	// start：POST 表单材料（action_url + manifest JSON + 签名态）成对返回。
	start, err := ghApps.StartManifestFlow(ctx, &deliveryv1.StartManifestFlowRequest{
		Scope: &deliveryv1.StartManifestFlowRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, start.GetState())

	// 提交契约（GitHub 文档口径）：浏览器 form POST 到 apps/new，字段名
	// manifest、值为 JSON 字符串；state 查询参数 = 本流签名态（GitHub 原
	// 样回显 redirect_url）。GET 跳转带 query 会被静默忽略（真机两连咬）。
	parsed, perr := url.Parse(start.GetActionUrl())
	require.NoError(t, perr)
	assert.Equal(t, "github.com", parsed.Hostname())
	assert.Equal(t, "/settings/apps/new", parsed.Path, "form action target")
	assert.Equal(t, start.GetState(), parsed.Query().Get("state"), "signed state doubles as GitHub's CSRF state echo")
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(start.GetManifest()), &manifest), "manifest is the plain JSON string form field value")
	assert.Equal(t, base+"/github-apps/connect", manifest["redirect_url"], "callback lands on the console connect route")
	assert.Equal(t, "read", manifest["default_permissions"].(map[string]any)["contents"])
	assert.Equal(t, false, manifest["hook_attributes"].(map[string]any)["active"], "push trigger stays on the flthook channel")
	assert.Equal(t, true, manifest["public"], "public app = installable on any account incl. GitHub organizations (user-owned private apps are personal-only)")

	// consume：一次性 code 换凭据（假 GitHub 的 conversions 单次有效），
	// 安装页 URL 返回、行未落。
	consumed, err := ghApps.ConsumeManifestCode(ctx, &deliveryv1.ConsumeManifestCodeRequest{
		Code: "good-code", State: start.GetState(),
	})
	require.NoError(t, err)
	assert.Contains(t, consumed.GetInstallUrl(), "/installations/new")
	assert.Equal(t, "fleetly-abc123", consumed.GetAppName())
	list, err := ghApps.ListGitHubApps(ctx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	assert.Empty(t, list.GetApps(), "no row lands before installation")

	// code 一次性：已耗 code 再 consume = 流内幂等重放（页面刷新安全）。
	replayed, err := ghApps.ConsumeManifestCode(ctx, &deliveryv1.ConsumeManifestCodeRequest{
		Code: "good-code", State: start.GetState(),
	})
	require.NoError(t, err)
	assert.Equal(t, consumed.GetInstallUrl(), replayed.GetInstallUrl())
	assert.Equal(t, consumed.GetAppName(), replayed.GetAppName())

	// 无在册流的坏 code = 上游 404 → E_GITHUB_UPSTREAM。
	start2, err := ghApps.StartManifestFlow(ctx, &deliveryv1.StartManifestFlowRequest{
		Scope: &deliveryv1.StartManifestFlowRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	_, err = ghApps.ConsumeManifestCode(ctx, &deliveryv1.ConsumeManifestCodeRequest{
		Code: "never-issued", State: start2.GetState(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))

	// complete：installation_id 落行（受理即验证解析属主），nonce 单用。
	done, err := ghApps.CompleteManifestFlow(ctx, &deliveryv1.CompleteManifestFlowRequest{
		State: start.GetState(), InstallationId: 51790921, ProjectId: projectID,
	})
	require.NoError(t, err)
	assert.Equal(t, "acme", done.GetApp().GetAccountLogin())
	assert.False(t, strings.Contains(done.GetApp().String(), "PRIVATE KEY"), "key never echoes")

	// nonce 已消费：同 state 再 complete = 精确拒绝。
	_, err = ghApps.CompleteManifestFlow(ctx, &deliveryv1.CompleteManifestFlowRequest{
		State: start.GetState(), InstallationId: 51790921, ProjectId: projectID,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "restart the flow")

	// 落行可见。
	list, err = ghApps.ListGitHubApps(ctx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	require.Len(t, list.GetApps(), 1)
	assert.Equal(t, "acme", list.GetApps()[0].GetAccountLogin())
}

func TestGitHubAppManifestGuards(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	ghApps := deliveryv1.NewGitHubAppsServiceClient(h.Conn)

	// 公网基址未配置（apitest 夹具不注入）→ 精确失败，手动表单兜底。
	_, err := ghApps.StartManifestFlow(ctx, &deliveryv1.StartManifestFlowRequest{
		Scope: &deliveryv1.StartManifestFlowRequest_Global{Global: true},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "console URL")

	// 畸形 state（签名不符/伪造）→ 精确拒绝。
	_, err = ghApps.CompleteManifestFlow(ctx, &deliveryv1.CompleteManifestFlowRequest{
		State: "forged.sig", InstallationId: 51790921,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}
