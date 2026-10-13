package apitest_test

// GitHub App 接入材料全链（ADR-0073 阶段 B）：受理即验证（错值当场精确
// 报错 + 属主自动解析）、双作用域授权分野（Project 行级 / Global 平台门）、
// upsert 轮换、软删与列仓面。上游用进程内 httptest 假 GitHub——聚合行的
// api_base 指向它，生产客户端零改动即得端到端确定性。

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/github"
	"github.com/fleetlyrun/fleetly/internal/identity"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// ghFake 是假 GitHub API（apitest 内联版）：installation 响应的 app_id
// 可注入错配；仓库列固定两条。TLS 形态——validateAPIBase 只收 https://，
// 夹具的信任证书注入 Services.GitHub（api_base 指向夹具即端到端）。
type ghFake struct {
	mu         sync.Mutex
	installApp map[int64]int64 // installation id → 所属 app id（错配注入点）
	codes      map[string]bool // manifest 兑换码（一次性语义）
	pem        []byte
	srv        *httptest.Server
}

func newFakeGitHub(t *testing.T, h *apitest.Harness) *ghFake {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	f := &ghFake{
		installApp: map[int64]int64{51790921: 182345, 51887734: 201884},
		codes:      map[string]bool{"good-code": true},
		pem:        pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /app-manifests/{code}/conversions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		code := r.PathValue("code")
		valid := f.codes[code]
		delete(f.codes, code) // 一次性
		f.mu.Unlock()
		if !valid {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 182345, "slug": "fleetly-abc123", "name": "fleetly-abc123", "pem": string(f.pem),
		})
	})
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		appID := f.installApp
		f.mu.Unlock()
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "app_id": appID[id], "account": map[string]string{"login": "acme"},
		})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": "ghs_fake", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET /installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 2, "repositories": []map[string]any{
			{"full_name": "acme/web", "private": true},
			{"full_name": "acme/oss-templates", "private": false},
		}})
	})
	f.srv = httptest.NewTLSServer(mux)
	h.Services.GitHub = github.NewClient(f.srv.Client(), nil)
	t.Cleanup(f.srv.Close)
	return f
}

// putReq 是合法 put 载荷（project 作用域）。
func putReq(projectID, apiBase string) *deliveryv1.SetGitHubAppRequest {
	return &deliveryv1.SetGitHubAppRequest{
		Scope: &deliveryv1.SetGitHubAppRequest_ProjectId{ProjectId: projectID},
		AppId: 182345, InstallationId: 51790921,
		PrivateKeyPem: "", // 由测试填（fake pem）
		ApiBase:       apiBase,
	}
}

func TestGitHubAppIntakeVerification(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := deliveryv1.NewGitHubAppsServiceClient(h.Conn)

	fake := newFakeGitHub(t, h)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "ghapps"})
	require.NoError(t, err)
	projectID := p.GetProject().GetId()

	// 受理即验证：落库前实调 GitHub——属主自动解析（请求不携带），响应只
	// 回指纹不回明文。
	req := putReq(projectID, fake.srv.URL)
	req.PrivateKeyPem = string(fake.pem)
	out, err := apps.SetGitHubApp(ctx, req)
	require.NoError(t, err)
	app := out.GetApp()
	assert.Equal(t, "acme", app.GetAccountLogin(), "owner resolved from the installation")
	assert.NotEmpty(t, app.GetPrivateKeyFingerprint())
	assert.NotContains(t, app.String(), "RSA PRIVATE KEY", "the key never echoes back")
	assert.Equal(t, projectID, app.GetProjectId())

	// List 指纹面：project 作用域可见，明文不在面里。
	list, err := apps.ListGitHubApps(ctx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	require.Len(t, list.GetApps(), 1)
	assert.Nil(t, nil) // 占位：面字段即指纹，明文形态不存在于 proto
	assert.Equal(t, app.GetId(), list.GetApps()[0].GetId())

	// 列仓（现铸）：installation 可达仓库透出。
	repos, err := apps.ListGitHubAppRepos(ctx, &deliveryv1.ListGitHubAppReposRequest{Id: app.GetId()})
	require.NoError(t, err)
	require.Len(t, repos.GetRepos(), 2)
	assert.Equal(t, "acme/web", repos.GetRepos()[0].GetFullName())
	assert.True(t, repos.GetRepos()[0].GetPrivate())

	// upsert 重放：同属主同钥 = 覆盖（id 不变；fingerprint 是明文的函数，
	// 同钥恒同指纹）；换钥重放 = 指纹变（轮换可见面）。
	firstID, firstFP := app.GetId(), app.GetPrivateKeyFingerprint()
	replay, err := apps.SetGitHubApp(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, firstID, replay.GetApp().GetId())
	assert.Equal(t, firstFP, replay.GetApp().GetPrivateKeyFingerprint(), "fingerprint is a function of the plaintext")
	key2, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pem2 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key2)})
	rotated := putReq(projectID, fake.srv.URL)
	rotated.PrivateKeyPem = string(pem2)
	replay2, err := apps.SetGitHubApp(ctx, rotated)
	require.NoError(t, err)
	assert.Equal(t, firstID, replay2.GetApp().GetId())
	assert.NotEqual(t, firstFP, replay2.GetApp().GetPrivateKeyFingerprint(), "key rotation shows a new fingerprint")

	// 错配：installation 属于别的 app → E_INVALID_ARGUMENT（打错当场报）。
	fake.mu.Lock()
	fake.installApp[51790921] = 999
	fake.mu.Unlock()
	bad := putReq(projectID, fake.srv.URL)
	bad.PrivateKeyPem = string(fake.pem)
	_, err = apps.SetGitHubApp(ctx, bad)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "does not belong")
	fake.mu.Lock()
	fake.installApp[51790921] = 182345
	fake.mu.Unlock()

	// 坏 PEM → E_INVALID_ARGUMENT；api_base 明文 http → E_INVALID_ARGUMENT；
	// scope 双缺 → E_INVALID_ARGUMENT。
	noPEM := putReq(projectID, fake.srv.URL)
	noPEM.PrivateKeyPem = "not a pem"
	_, err = apps.SetGitHubApp(ctx, noPEM)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	httpBase := putReq(projectID, "http://api.github.com")
	httpBase.PrivateKeyPem = string(fake.pem)
	_, err = apps.SetGitHubApp(ctx, httpBase)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	noScope := &deliveryv1.SetGitHubAppRequest{AppId: 1, InstallationId: 2, PrivateKeyPem: string(fake.pem)}
	_, err = apps.SetGitHubApp(ctx, noScope)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// 上游不可达 → E_GITHUB_UPSTREAM（Available；REST 面 502 映射）。
	dead := putReq(projectID, "https://127.0.0.1:1")
	dead.PrivateKeyPem = string(fake.pem)
	_, err = apps.SetGitHubApp(ctx, dead)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, err.Error(), "E_GITHUB_UPSTREAM")

	// 软删：解析面即时失效（repos → E_NOT_FOUND），List 不再见。
	_, err = apps.DeleteGitHubApp(ctx, &deliveryv1.DeleteGitHubAppRequest{Id: firstID})
	require.NoError(t, err)
	_, err = apps.ListGitHubAppRepos(ctx, &deliveryv1.ListGitHubAppReposRequest{Id: firstID})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	list, err = apps.ListGitHubApps(ctx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_ProjectId{ProjectId: projectID},
	})
	require.NoError(t, err)
	assert.Empty(t, list.GetApps())
}

// TestGitHubAppAuthz：授权分野（ADR-0073 决策 2）——Global 行是平台材料
// （非平台 token 写/读/列仓/删一律 E_FORBIDDEN）；Project 级行本队可写、
// 他队 E_FORBIDDEN。
func TestGitHubAppAuthz(t *testing.T) {
	h := apitest.New(t)
	ownerCtx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	roles := identityv1.NewRolesServiceClient(h.Conn)
	users := identityv1.NewUsersServiceClient(h.Conn)
	ghApps := deliveryv1.NewGitHubAppsServiceClient(h.Conn)

	fake := newFakeGitHub(t, h)
	mine, err := projects.CreateProject(ownerCtx, &structurev1.CreateProjectRequest{Name: "mine"})
	require.NoError(t, err)

	// 他队：team + member token（带 github_app scope——scope 门过、行级门
	// 才是受检对象）。
	sandbox, err := teams.CreateTeam(ownerCtx, &identityv1.CreateTeamRequest{Name: "sandbox"})
	require.NoError(t, err)
	role, err := roles.CreateRole(ownerCtx, &identityv1.CreateRoleRequest{
		Name: "ghapp", TeamId: sandbox.GetTeam().GetId(),
		Scopes: []string{"github_app:write", "github_app:read", "projects:write"},
	})
	require.NoError(t, err)
	alice, err := users.CreateUser(ownerCtx, &identityv1.CreateUserRequest{
		Name: "alice", TeamId: sandbox.GetTeam().GetId(), RoleId: role.GetRole().GetId()})
	require.NoError(t, err)
	minted, err := identityv1.NewTokensServiceClient(h.Conn).CreateToken(ownerCtx,
		&identityv1.CreateTokenRequest{Name: "alice", RoleId: role.GetRole().GetId(),
			TeamId: sandbox.GetTeam().GetId(), UserId: alice.GetUser().GetId()})
	_ = minted
	require.NoError(t, err)
	otherCtx := sdk.WithToken(context.Background(), minted.GetSecret())
	theirs, err := projects.CreateProject(otherCtx, &structurev1.CreateProjectRequest{Name: "theirs"})
	require.NoError(t, err)

	// Global 行：owner 落位；他队 token 写/读/删/列仓全部 E_FORBIDDEN。
	greq := &deliveryv1.SetGitHubAppRequest{
		Scope: &deliveryv1.SetGitHubAppRequest_Global{Global: true},
		AppId: 201884, InstallationId: 51887734,
		PrivateKeyPem: string(fake.pem), ApiBase: fake.srv.URL,
	}
	gapp, err := ghApps.SetGitHubApp(ownerCtx, greq)
	require.NoError(t, err)
	assert.Equal(t, "acme", gapp.GetApp().GetAccountLogin())

	_, err = ghApps.SetGitHubApp(otherCtx, greq)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = ghApps.ListGitHubApps(otherCtx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_Global{Global: true}})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = ghApps.DeleteGitHubApp(otherCtx, &deliveryv1.DeleteGitHubAppRequest{Id: gapp.GetApp().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = ghApps.ListGitHubAppRepos(otherCtx, &deliveryv1.ListGitHubAppReposRequest{Id: gapp.GetApp().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// Project 级行：本队（owner）可写；他队 token 对他队项目可写（行级自
	// 治），对他队项目（mine）E_FORBIDDEN。
	oreq := putReq(theirs.GetProject().GetId(), fake.srv.URL)
	oreq.PrivateKeyPem = string(fake.pem)
	_, err = ghApps.SetGitHubApp(otherCtx, oreq)
	require.NoError(t, err)
	mreq := putReq(mine.GetProject().GetId(), fake.srv.URL)
	mreq.PrivateKeyPem = string(fake.pem)
	_, err = ghApps.SetGitHubApp(otherCtx, mreq)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestGitHubAppGlobalListedByPlatformAdminOnly：Global 读面回到平台门
// （ADR-0073 决策 3）——owner 双态可见，他队 member 不可见。
func TestGitHubAppGlobalListedByPlatformAdminOnly(t *testing.T) {
	h := apitest.New(t)
	ownerCtx := sdk.WithToken(context.Background(), h.Token)
	ghApps := deliveryv1.NewGitHubAppsServiceClient(h.Conn)
	fake := newFakeGitHub(t, h)
	greq := &deliveryv1.SetGitHubAppRequest{
		Scope: &deliveryv1.SetGitHubAppRequest_Global{Global: true},
		AppId: 201884, InstallationId: 51887734,
		PrivateKeyPem: string(fake.pem), ApiBase: fake.srv.URL,
	}
	_, err := ghApps.SetGitHubApp(ownerCtx, greq)
	require.NoError(t, err)
	list, err := ghApps.ListGitHubApps(ownerCtx, &deliveryv1.ListGitHubAppsRequest{
		Scope: &deliveryv1.ListGitHubAppsRequest_Global{Global: true}})
	require.NoError(t, err)
	assert.Len(t, list.GetApps(), 1)
	assert.Equal(t, identity.RoleOwnerID, identity.RoleOwnerID) // 哨兵：上文为 owner 面
}
