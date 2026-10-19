package apitest_test

// 复合 handler 内层动作 × scope（R08-M3，S14-c6）：TemplatesService.
// InstantiateTemplate 与 HooksService.RefreshGitHook 在进程内直调其他服务
// 方法/触发部署链——拦截器只在 RPC 边界按外层注解执法一次，内层动词的
// 能力面必须在 handler 里显式复核（与内层 RPC 的 api_key_scope 同源）。
// 负例：自定义角色仅外层 scope → 403 且零副作用；正例：携带全部内层
// scope 的自定义角色与平台 owner 均可用。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// mintScopedRoleToken 在 default Team 造一个自定义角色并直铸挂该角色的
// Token（最小权限夹具；明文只经返回的 ctx 存活）。
func mintScopedRoleToken(t *testing.T, h *apitest.Harness, name string, scopes ...string) context.Context {
	t.Helper()
	created, err := identityv1.NewRolesServiceClient(h.Conn).CreateRole(h.Ctx(), &identityv1.CreateRoleRequest{
		Name: name, Scopes: scopes,
	})
	require.NoError(t, err)
	secret := mintTeamToken(t, h, name+"-token", identity.DefaultTeamID, created.GetRole().GetId())
	return sdk.WithToken(context.Background(), secret)
}

// catalogWithTemplate 把一条自定义模板体写进目录快照（模板面夹具：覆盖
// 内嵌目录没有的声明族，如 x-fleetly-databases）。
func catalogWithTemplate(t *testing.T, h *apitest.Harness, name, body string) {
	t.Helper()
	entry := apptemplate.Entry{Name: name, Version: "1.0.0", Digest: apptemplate.DigestOf(body), Body: body}
	manifest, err := json.Marshal(apptemplate.Manifest{Version: 1, Templates: []apptemplate.Entry{entry}})
	require.NoError(t, err)
	require.NoError(t, h.Services.Catalog.Put(context.Background(), &catalog.Snapshot{
		Digest: apptemplate.AggregateDigest([]apptemplate.Entry{entry}), Body: string(manifest),
	}))
}

// assertTemplateInstantiationSideEffectFree 断言一次被拒的实例化零副作用：
// app/secret/route/database 行与 template.instantiated 事件均未落。
func assertTemplateInstantiationSideEffectFree(t *testing.T, h *apitest.Harness, projectID string) {
	t.Helper()
	owner := h.Ctx()
	apps, err := structurev1.NewAppsServiceClient(h.Conn).ListApps(owner, &structurev1.ListAppsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, apps.GetApps(), "denied instantiation must not create apps")
	secrets, err := structurev1.NewSecretsServiceClient(h.Conn).ListSecrets(owner, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, secrets.GetSecrets(), "denied instantiation must not mint secrets")
	routes, err := proxyv1.NewRoutesServiceClient(h.Conn).ListRoutes(owner, &proxyv1.ListRoutesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, routes.GetRoutes(), "denied instantiation must not create routes")
	dbs, err := structurev1.NewDatabasesServiceClient(h.Conn).ListDatabases(owner, &structurev1.ListDatabasesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, dbs.GetDatabases(), "denied instantiation must not create databases")
	evts, err := h.Services.OutboxEvents.ListAfter(context.Background(), 0, 100, "")
	require.NoError(t, err)
	for _, ev := range evts {
		assert.NotEqual(t, "template.instantiated", ev.Name, "denied instantiation must not emit its summary event")
	}
}

// TestInstantiateTemplateRequiresInnerScopes：只有外围 templates:write 的
// 自定义角色不得借实例化获得 apps/secrets/networks/databases/routes/
// deployments 的写能力——403 且零副作用。
func TestInstantiateTemplateRequiresInnerScopes(t *testing.T) {
	h := apitest.NewManual(t)
	projectID := h.CreateProject(t, "tplscope").GetId()
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)
	tplCtx := mintScopedRoleToken(t, h, "template-viewer", "templates:write")

	_, err := tpl.InstantiateTemplate(tplCtx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "grafana", AppName: "dash",
		Values: map[string]string{"host": "dash.example.org"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "E_FORBIDDEN")
	assert.Contains(t, err.Error(), "apps:write", "the first missing inner capability must be named")
	assertTemplateInstantiationSideEffectFree(t, h, projectID)
}

// TestInstantiateTemplateRequiresDatabaseNetworkScopes：模板声明 databases
// 时内层建 default 网 + 托管库——缺 networks:write / databases:write 的
// 角色在入口预检即拒（零副作用），补齐后可用。
func TestInstantiateTemplateRequiresDatabaseNetworkScopes(t *testing.T) {
	h := apitest.NewManual(t)
	projectID := h.CreateProject(t, "tplscope-db").GetId()
	catalogWithTemplate(t, h, "dbapp", `x-fleetly-template:
  name: dbapp
  version: 1.0.0
  description: nginx fronted by a managed postgres database
  variables:
    - name: host
      type: domain
      required: true
  routes:
    - var: host
      process: web
      port: 80
x-fleetly-databases:
  - name: main
    engine: postgres
services:
  web:
    image: nginx:1.27
    ports: ["80"]
`)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)

	missingNetwork := mintScopedRoleToken(t, h, "template-nodb",
		"templates:write", "apps:write", "databases:write", "deployments:write", "routes:write", "secrets:write")
	_, err := tpl.InstantiateTemplate(missingNetwork, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "dbapp", AppName: "site",
		Values: map[string]string{"host": "dbapp.example.org"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "networks:write")
	assertTemplateInstantiationSideEffectFree(t, h, projectID)

	// 补齐内层写面 → 实例化成功（库 created）。
	full := mintScopedRoleToken(t, h, "template-dbdeployer",
		"templates:write", "apps:write", "networks:write", "databases:write", "deployments:write", "routes:write", "secrets:write")
	res, err := tpl.InstantiateTemplate(full, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "dbapp", AppName: "site",
		Values: map[string]string{"host": "dbapp.example.org"},
	})
	require.NoError(t, err)
	dbs, err := structurev1.NewDatabasesServiceClient(h.Conn).ListDatabases(h.Ctx(), &structurev1.ListDatabasesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, dbs.GetDatabases(), 1)
	assert.Equal(t, "main", dbs.GetDatabases()[0].GetName())
	assert.NotEmpty(t, res.GetDeploymentId())
}

// TestInstantiateTemplateWithAllInnerScopes：携带全部内层写面的自定义角色
// 可用（能力面声明与内层动作并集一致时不再越权）。
func TestInstantiateTemplateWithAllInnerScopes(t *testing.T) {
	h := apitest.NewManual(t)
	projectID := h.CreateProject(t, "tplscope-ok").GetId()
	tplCtx := mintScopedRoleToken(t, h, "template-deployer",
		"templates:write", "apps:write", "networks:write", "secrets:write", "databases:write", "deployments:write", "routes:write")

	res, err := deliveryv1.NewTemplatesServiceClient(h.Conn).InstantiateTemplate(tplCtx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "grafana", AppName: "dash",
		Values: map[string]string{"host": "dash.example.org"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.GetDeploymentId())
	kinds := map[string]bool{}
	for _, r := range res.GetResources() {
		kinds[r.GetKind()] = true
	}
	assert.True(t, kinds["app"] && kinds["secret"] && kinds["deployment"] && kinds["route"])
}

// TestRefreshGitHookRequiresDeploymentsScope：RefreshGitHook 的外层注解是
// apps:write，但内层动作是部署链（构建+替换工作负载）——只有 apps:write
// 的角色 403 且零部署行；补 deployments:write 后可用。
func TestRefreshGitHookRequiresDeploymentsScope(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "hookscope").GetId(), "web").GetId()
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	_, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main",
	})
	require.NoError(t, err)

	const commit = "2222222222222222222222222222222222222222"
	appsOnly := mintScopedRoleToken(t, h, "hook-app-writer", "apps:write")
	_, err = hooks.RefreshGitHook(appsOnly, &deliveryv1.RefreshGitHookRequest{AppId: appID, Commit: commit})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Contains(t, err.Error(), "deployments:write")
	list, err := deployments.ListDeployments(owner, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Empty(t, list.GetDeployments(), "denied refresh must not submit a deployment")

	withDeploy := mintScopedRoleToken(t, h, "hook-deployer", "apps:write", "deployments:write")
	_, err = hooks.RefreshGitHook(withDeploy, &deliveryv1.RefreshGitHookRequest{AppId: appID, Commit: commit})
	require.NoError(t, err)
	list, err = deployments.ListDeployments(owner, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	assert.Len(t, list.GetDeployments(), 1)
}

// TestInstantiateTemplatePlatformOwnerUnchanged：平台 owner（`*`）不受内层
// scope 门影响（owner 的 scope 面天然覆盖并集，无需身份特判）。既有
// TestInstantiateTemplate* 已用 owner 走全链，此处只钉"新增门不回退 owner"。
func TestInstantiateTemplatePlatformOwnerUnchanged(t *testing.T) {
	h := apitest.NewManual(t)
	projectID := h.CreateProject(t, "tplscope-owner").GetId()
	res, err := deliveryv1.NewTemplatesServiceClient(h.Conn).InstantiateTemplate(h.Ctx(), &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "nginx", AppName: "site",
		Values: map[string]string{"host": "owner.example.org"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.GetDeploymentId())
}
