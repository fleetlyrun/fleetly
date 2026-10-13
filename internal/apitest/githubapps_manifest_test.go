package apitest_test

// Manifest 自动建流三步全链（ADR-0073 决策 8）：start（签名 state + 跳转
// URL）→ consume（一次性 code 换凭据暂存，安装页 URL）→ complete
// （installation_id 落行 + nonce 单用）。假 GitHub 的 conversions 端点
// 单次有效；PEM 全程不出现在任何读面。

import (
	"context"
	"encoding/base64"
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

	// start：跳转 URL 与签名态成对；manifest 编码在 URL 查询参数里。
	start, err := ghApps.StartManifestFlow(ctx, &deliveryv1.StartManifestFlowRequest{
		Scope: &deliveryv1.StartManifestFlowRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	require.Contains(t, start.GetRedirectUrl(), "https://github.com/settings/apps/new?state=")
	require.NotEmpty(t, start.GetState())

	// manifest 编码契约（GitHub 文档口径）：标准 Base64 再 URL 编码——解码
	// 回 JSON 并逐字段断言（base64url 会被 GitHub 静默忽略、表单全空）。
	parsed, perr := url.Parse(start.GetRedirectUrl())
	require.NoError(t, perr)
	encoded := parsed.Query().Get("state")
	require.NotEmpty(t, encoded)
	decoded, derr := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, derr, "manifest must be standard base64 (GitHub ignores base64url)")
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(decoded, &manifest))
	assert.Equal(t, base+"/github-apps/connect", manifest["redirect_url"], "callback lands on the console connect route")
	assert.Equal(t, "read", manifest["default_permissions"].(map[string]any)["contents"])
	assert.Equal(t, false, manifest["hook_attributes"].(map[string]any)["active"], "push trigger stays on the flthook channel")

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

	// code 单次有效：同一 code 再 consume = 上游 404 → E_GITHUB_UPSTREAM。
	_, err = ghApps.ConsumeManifestCode(ctx, &deliveryv1.ConsumeManifestCodeRequest{
		Code: "good-code", State: start.GetState(),
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
