package apitest_test

// TemplatesService e2e（F3.3，ADR-0050）：目录读面（内嵌源）、实例化
// create-or-reuse 全链（App/secret 铸造/渲染→既有 Deploy 链/Route）、
// 幂等重跑（不旋转密码/不重建资源）、供值执法、目录刷新 fail-closed
// （坏清单不换好快照）、事件/审计锚。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func templatesFixture(t *testing.T) (*apitest.Harness, context.Context, string) {
	t.Helper()
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "templates"})
	require.NoError(t, err)
	return h, ctx, p.GetProject().GetId()
}

func TestTemplatesCatalogRead(t *testing.T) {
	h, ctx, _ := templatesFixture(t)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)

	list, err := tpl.ListTemplates(ctx, &deliveryv1.ListTemplatesRequest{})
	require.NoError(t, err)
	assert.Equal(t, "builtin", list.GetSource())
	names := map[string]bool{}
	for _, e := range list.GetTemplates() {
		names[e.GetName()] = true
		assert.NotEmpty(t, e.GetVersion())
		assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, e.GetDigest())
	}
	assert.True(t, names["nginx"], "builtin catalog carries nginx")
	assert.True(t, names["grafana"], "builtin catalog carries grafana")

	got, err := tpl.GetTemplate(ctx, &deliveryv1.GetTemplateRequest{Name: "grafana"})
	require.NoError(t, err)
	assert.Equal(t, "grafana", got.GetTemplate().GetName())
	assert.Contains(t, got.GetBody(), "x-fleetly-template:")
	assert.Contains(t, got.GetBody(), "GF_SECURITY_ADMIN_PASSWORD__FILE")
	varTypes := map[string]string{}
	for _, v := range got.GetTemplate().GetVariables() {
		varTypes[v.GetName()] = v.GetType()
	}
	assert.Equal(t, map[string]string{"admin_password": "secret", "host": "domain"}, varTypes)

	_, err = tpl.GetTemplate(ctx, &deliveryv1.GetTemplateRequest{Name: "nope"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Contains(t, err.Error(), "not in the catalog")
}

func TestInstantiateTemplateNginx(t *testing.T) {
	h, ctx, projectID := templatesFixture(t)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	routes := proxyv1.NewRoutesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	res, err := tpl.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "nginx", AppName: "site",
		Values: map[string]string{"host": "site.127.0.0.1.sslip.io"},
	})
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", res.GetTemplateVersion())
	assert.NotEmpty(t, res.GetDeploymentId())

	// 资源报告：app/deployment/route 全 created。
	kinds := map[string]bool{}
	reused := map[string]bool{}
	for _, r := range res.GetResources() {
		kinds[r.GetKind()] = true
		reused[r.GetKind()] = r.GetReused()
	}
	assert.True(t, kinds["app"] && !reused["app"])
	assert.True(t, kinds["deployment"])
	assert.True(t, kinds["route"] && !reused["route"])

	// Route 落地指向 web:80。
	routeList, err := routes.ListRoutes(ctx, &proxyv1.ListRoutesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, routeList.GetRoutes(), 1)
	assert.Equal(t, "site.127.0.0.1.sslip.io", routeList.GetRoutes()[0].GetHost())
	assert.Equal(t, "web", routeList.GetRoutes()[0].GetProcess())
	assert.Equal(t, int32(80), routeList.GetRoutes()[0].GetPort())

	// 渲染链走到收敛环：进程携带声明端口 + default 网（F3.5 两半边经模板
	// 面同执法）。
	require.Eventually(t, func() bool { return len(h.Runtime.Calls()) > 0 }, 2e9, 1e7)
	w := h.Runtime.Calls()[0].Spec["web"]
	require.Len(t, w.Ports, 1)
	assert.Equal(t, int32(80), w.Ports[0].Port)
	assert.Contains(t, w.Networks, "default")

	// 首部署推到终态（per-App 部署串行——重跑的部署排在队头后面；报活
	// → observing → 观察窗过钟 → succeeded，portdecl 同款推进序）。
	driveSucceeded := func(depID string) {
		t.Helper()
		var gen uint64
		require.Eventually(t, func() bool {
			got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
			return err == nil && (got.GetDeployment().GetState() == "releasing" || got.GetDeployment().GetState() == "observing")
		}, 3e9, 1e7)
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		require.NoError(t, err)
		gen = got.GetDeployment().GetGeneration()
		h.Runtime.ReportRunning(res.GetAppId()+"-web", capability.Generation(gen))
		require.Eventually(t, func() bool {
			got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
			return err == nil && got.GetDeployment().GetState() == "observing"
		}, 3e9, 1e7)
		h.Clock.Advance(61 * time.Second)
		require.Eventually(t, func() bool {
			got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
			return err == nil && got.GetDeployment().GetState() == "succeeded"
		}, 3e9, 1e7)
	}
	driveSucceeded(res.GetDeploymentId())

	// 幂等重跑：同请求 → app/route reused、新部署（latest-wins 重部署）。
	res2, err := tpl.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "nginx", AppName: "site",
		Values: map[string]string{"host": "site.127.0.0.1.sslip.io"},
	})
	require.NoError(t, err)
	assert.Equal(t, res.GetAppId(), res2.GetAppId(), "same app reused by name")
	for _, r := range res2.GetResources() {
		switch r.GetKind() {
		case "app", "route":
			assert.True(t, r.GetReused(), "%s reused on re-run", r.GetKind())
		case "deployment":
			assert.False(t, r.GetReused(), "a new deployment is submitted on re-run")
		}
	}
	driveSucceeded(res2.GetDeploymentId())

	// 应用同名复用不重复建行。
	appList, err := apps.ListApps(ctx, &structurev1.ListAppsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, appList.GetApps(), 1)
}

func TestInstantiateTemplateSecretChain(t *testing.T) {
	h, ctx, projectID := templatesFixture(t)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)

	_, err := tpl.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "grafana", AppName: "dash",
		Values: map[string]string{"host": "dash.127.0.0.1.sslip.io"},
	})
	require.NoError(t, err)

	// secret 铸造：template:<app>:<var> 落库、值零回显（ListSecrets 指纹面
	// 是唯一读回通道——Secret 无 Get 面）。
	secretFP := func() string {
		list, lerr := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
		require.NoError(t, lerr)
		for _, s := range list.GetSecrets() {
			if s.GetName() == "template:dash:admin_password" {
				return s.GetFingerprint()
			}
		}
		return ""
	}
	fp := secretFP()
	require.NotEmpty(t, fp, "generated secret must exist")

	// 渲染产物经收敛环：secret_refs 由 engine 解析为材料文件（Ensure 的
	// Materials 值面——FakeRuntime 断言注入链），env 吃到路径（引用面）。
	require.Eventually(t, func() bool { return len(h.Runtime.Calls()) > 0 }, 2e9, 1e7)
	call := h.Runtime.Calls()[0]
	val, ok := call.Materials.SecretFiles["template:dash:admin_password"]
	require.True(t, ok, "secret material file must be injected (secret_refs chain)")
	assert.Regexp(t, `^[0-9a-f]{48}$`, string(val), "minted password shape (hex 48)")
	assert.Equal(t, "/run/secrets/template:dash:admin_password", call.Spec["grafana"].Env["GF_SECURITY_ADMIN_PASSWORD__FILE"])

	// 幂等重跑：secret reused（不旋转——指纹不变即证）。
	_, err = tpl.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
		ProjectId: projectID, Template: "grafana", AppName: "dash",
		Values: map[string]string{"host": "dash.127.0.0.1.sslip.io"},
	})
	require.NoError(t, err)
	assert.Equal(t, fp, secretFP(), "re-run must not rotate the generated password")

	// 供值执法：secret 型携值拒、未知键拒、缺必填拒。
	for _, tc := range []struct {
		name   string
		values map[string]string
		code   codes.Code
		want   string
	}{
		{"secret supplied", map[string]string{"host": "h.example.org", "admin_password": "hunter2"}, codes.InvalidArgument, "platform-generated"},
		{"unknown key", map[string]string{"host": "h.example.org", "extra": "x"}, codes.InvalidArgument, "unknown variable"},
		{"missing required host", map[string]string{}, codes.InvalidArgument, "is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tpl.InstantiateTemplate(ctx, &deliveryv1.InstantiateTemplateRequest{
				ProjectId: projectID, Template: "grafana", AppName: "dash",
				Values: tc.values,
			})
			require.Error(t, err)
			assert.Equal(t, tc.code, status.Code(err))
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	// template.instantiated 事件落账（app 聚合；outbox 直查——events list
	// 是 after_seq 游标语义，见 exec_test 同款）。
	evts, err := h.Services.OutboxEvents.ListAfter(context.Background(), h.DB.Runner(), 0, 50)
	require.NoError(t, err)
	seen := false
	for _, ev := range evts {
		if ev.Name != "template.instantiated" {
			continue
		}
		seen = true
		assert.Equal(t, "app", ev.Aggregate)
		assert.Contains(t, string(ev.Payload), `"template":"grafana"`)
		assert.Contains(t, string(ev.Payload), `"version":"1.0.0"`)
	}
	assert.True(t, seen, "template.instantiated event must exist")
}

func TestInstantiateTemplateValidation(t *testing.T) {
	h, ctx, projectID := templatesFixture(t)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)

	for _, tc := range []struct {
		name string
		req  *deliveryv1.InstantiateTemplateRequest
		code codes.Code
		want string
	}{
		{"empty fields", &deliveryv1.InstantiateTemplateRequest{ProjectId: projectID}, codes.InvalidArgument, "must not be empty"},
		{"unknown template", &deliveryv1.InstantiateTemplateRequest{
			ProjectId: projectID, Template: "ghost", AppName: "g",
			Values: map[string]string{"host": "g.example.org"},
		}, codes.NotFound, "not in the catalog"},
		// host 元字符在 Route 步骤被拒（ValidateRouteHost 白名单——顺序
		// 编排中部署已受理，失败即停带精确错误是诚实面；重跑收敛）。
		{"route host metacharacters", &deliveryv1.InstantiateTemplateRequest{
			ProjectId: projectID, Template: "nginx", AppName: "metachar",
			Values: map[string]string{"host": "bad`host"},
		}, codes.InvalidArgument, "host"},
		{"unknown project", &deliveryv1.InstantiateTemplateRequest{
			ProjectId: "01JZZZNOTFOUND", Template: "nginx", AppName: "g",
			Values: map[string]string{"host": "g.example.org"},
		}, codes.NotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tpl.InstantiateTemplate(ctx, tc.req)
			require.Error(t, err)
			assert.Equal(t, tc.code, status.Code(err))
			if tc.want != "" {
				assert.Contains(t, err.Error(), tc.want)
			}
		})
	}
}

func TestRefreshTemplatesFailClosed(t *testing.T) {
	h, ctx, _ := templatesFixture(t)
	tpl := deliveryv1.NewTemplatesServiceClient(h.Conn)

	// 未配置 URL：精确拒绝（内嵌目录即全部）。
	_, err := tpl.RefreshTemplates(ctx, &deliveryv1.RefreshTemplatesRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "server.templates_catalog_url is empty")

	builtin, err := apptemplate.Builtin()
	require.NoError(t, err)
	// 单条目录清单（nginx 原文）——刷新成功形态。
	nginx := builtin[0]
	for _, e := range builtin {
		if e.Name == "nginx" {
			nginx = e
		}
	}
	manifest := map[string]any{
		"version": 1,
		"templates": []map[string]string{
			{"name": nginx.Name, "version": nginx.Version, "digest": nginx.Digest, "body": nginx.Body},
		},
	}
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/catalog.json", r.URL.Path)
		_ = json.NewEncoder(w).Encode(manifest)
	}))
	defer good.Close()
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := map[string]any{
			"version": 1,
			"templates": []map[string]string{
				{"name": nginx.Name, "version": nginx.Version, "digest": "sha256:" + strings.Repeat("0", 64), "body": nginx.Body},
			},
		}
		_ = json.NewEncoder(w).Encode(m)
	}))
	defer tampered.Close()

	h.Services.TemplatesCatalogURL = good.URL
	refreshed, err := tpl.RefreshTemplates(ctx, &deliveryv1.RefreshTemplatesRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), refreshed.GetTemplateCount())
	assert.NotEqual(t, refreshed.GetPreviousDigest(), refreshed.GetDigest())

	list, err := tpl.ListTemplates(ctx, &deliveryv1.ListTemplatesRequest{})
	require.NoError(t, err)
	assert.Equal(t, "refreshed", list.GetSource())
	require.Len(t, list.GetTemplates(), 1)
	assert.Equal(t, "nginx", list.GetTemplates()[0].GetName())

	// fail-closed：digest 不符的清单整次拒绝，快照不变（仍服务 refreshed
	// 的单条目录）。
	h.Services.TemplatesCatalogURL = tampered.URL
	_, err = tpl.RefreshTemplates(ctx, &deliveryv1.RefreshTemplatesRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Contains(t, err.Error(), "digest mismatch")
	list2, err := tpl.ListTemplates(ctx, &deliveryv1.ListTemplatesRequest{})
	require.NoError(t, err)
	assert.Equal(t, "refreshed", list2.GetSource())
	assert.Len(t, list2.GetTemplates(), 1)

	// templates.refreshed 事件落账（digest 前后对照）。
	evts, err := h.Services.OutboxEvents.ListAfter(context.Background(), h.DB.Runner(), 0, 50)
	require.NoError(t, err)
	seen := false
	for _, ev := range evts {
		if ev.Name != "templates.refreshed" {
			continue
		}
		seen = true
		assert.Contains(t, string(ev.Payload), `"count":1`)
		assert.Contains(t, string(ev.Payload), refreshed.GetDigest())
	}
	assert.True(t, seen, "templates.refreshed event must exist")
}
