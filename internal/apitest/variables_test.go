package apitest_test

// 两级变量合成全链（F2.9，ADR-0043 验收锚）：SharedVariable 落库 → Deploy
// 归一化期合成（Project 层在下、App 层 env 覆盖）→ Revision 冻结最终生效集；
// 改共享变量不触发重部署（既有 Revision 不变）但响应提示受影响 App；
// 删除同理。App 级直传（DeployRequest.env）与 compose environment 两形态
// 的覆盖各有锚。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// latestSpecEnv 读 App 最新 Revision 冻结体的单进程 env（冻结即最终生效集）。
func latestSpecEnv(t *testing.T, h *apitest.Harness, ctx context.Context, appID, process string) map[string]string {
	t.Helper()
	rev, err := h.Services.Revisions.Latest(ctx, h.DB.Runner(), appID)
	require.NoError(t, err)
	s := &specv1.AppSpec{}
	require.NoError(t, (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(rev.Spec, s))
	for _, p := range s.GetProcesses() {
		if p.GetName() == process {
			return p.GetEnv()
		}
	}
	t.Fatalf("process %q not found in latest revision", process)
	return nil
}

// 验收锚 1/2：合成进冻结体（共享层在下、App 层 env 覆盖同键）；同状态
// 重复部署逐字节相等（内容寻址复用——不冻结 R2）。
func TestSharedVariableMergeAtNormalization(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	vars := structurev1.NewSharedVariablesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "varshop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "DATABASE_URL", Value: "postgres://shared",
	})
	require.NoError(t, err)
	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "LOG_LEVEL", Value: "debug",
	})
	require.NoError(t, err)

	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27",
		Env: map[string]string{"LOG_LEVEL": "warn", "EXTRA_FLAG": "app-only"},
	})
	require.NoError(t, err)

	env := latestSpecEnv(t, h, ctx, appID, "web")
	assert.Equal(t, map[string]string{
		"DATABASE_URL": "postgres://shared", // 共享层注入
		"LOG_LEVEL":    "warn",              // App 层覆盖
		"EXTRA_FLAG":   "app-only",          // App 层独有
	}, env)

	// 同源同变量状态重复部署：内容寻址复用（仍只有 R1，无新冻结）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27",
		Env: map[string]string{"LOG_LEVEL": "warn", "EXTRA_FLAG": "app-only"},
	})
	require.NoError(t, err)
	list, err := h.Services.Revisions.ListByApp(ctx, h.DB.Runner(), appID, 0, 10)
	require.NoError(t, err)
	require.Len(t, list, 1, "identical source+variable state must reuse the frozen revision")
}

// compose 形态的 App 层覆盖（environment 声明面）。
func TestSharedVariableComposeOverride(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	vars := structurev1.NewSharedVariablesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "varshop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "LOG_LEVEL", Value: "debug",
	})
	require.NoError(t, err)

	compose := "services:\n  web:\n    image: nginx:1.27\n    environment:\n      LOG_LEVEL: warn\n"
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, ComposeYaml: compose})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"LOG_LEVEL": "warn"}, latestSpecEnv(t, h, ctx, appID, "web"))

	// compose 形态携带 env 直传即拒（compose 自带声明面）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, ComposeYaml: compose, Env: map[string]string{"X": "y"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "compose declares variables")
}

// 验收锚 4：改共享变量不触发重部署（Revision 不变、无新部署行），响应
// 提示受影响 App；重部署后取新值（合成在归一化期，旧 Revision 不动）。
func TestSharedVariableChangeNoRedeployButAffectedHint(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	vars := structurev1.NewSharedVariablesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "varshop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	other, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "other"})
	require.NoError(t, err)

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "DATABASE_URL", Value: "postgres://v1",
	})
	require.NoError(t, err)

	// 首次 put 的受影响提示：项目内尚无任何 Revision——空集。
	put, err := vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "LOG_LEVEL", Value: "debug",
	})
	require.NoError(t, err)
	assert.Empty(t, put.GetAffectedApps())

	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	depCount := func() int {
		list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		require.NoError(t, err)
		return len(list.GetDeployments())
	}
	before := depCount()

	// 改共享变量：app（冻结 env 带旧值）受影响；other（无 Revision）不在
	// 提示集——提示是近似口径，但不部署的 App 不打扰。
	put, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: proj.GetProject().GetId(), Name: "DATABASE_URL", Value: "postgres://v2",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{appID}, put.GetAffectedApps())

	// 未重部署：行为不变（Revision 集不变、无新部署行、冻结值仍旧值）。
	list, err := h.Services.Revisions.ListByApp(ctx, h.DB.Runner(), appID, 0, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "postgres://v1", latestSpecEnv(t, h, ctx, appID, "web")["DATABASE_URL"])
	assert.Equal(t, before, depCount(), "changing a shared variable must not trigger a redeploy")

	// 重部署取新值（合成发生在归一化期）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.26"})
	require.NoError(t, err)
	assert.Equal(t, "postgres://v2", latestSpecEnv(t, h, ctx, appID, "web")["DATABASE_URL"])
	_ = other // other 全程无部署（受影响口径的负锚）
}

// 删除面：受影响提示（键仍在最新冻结 env）、幂等 tombstone 后重复删按
// 404 诚实拒绝（secrets 同款）、重部署后键从冻结体消失。
func TestSharedVariableDelete(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	vars := structurev1.NewSharedVariablesServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "varshop"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()
	projectID := proj.GetProject().GetId()

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: projectID, Name: "DATABASE_URL", Value: "postgres://v1",
	})
	require.NoError(t, err)
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)

	del, err := vars.DeleteSharedVariable(ctx, &structurev1.DeleteSharedVariableRequest{
		ProjectId: projectID, Name: "DATABASE_URL",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{appID}, del.GetAffectedApps())

	// 删除后 List 不再回显；重复删除 404（tombstone 幂等语义与 secrets 同款）。
	list, err := vars.ListSharedVariables(ctx, &structurev1.ListSharedVariablesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, list.GetVariables())
	_, err = vars.DeleteSharedVariable(ctx, &structurev1.DeleteSharedVariableRequest{
		ProjectId: projectID, Name: "DATABASE_URL",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// 既有 Revision 不变（删除同样不重部署）；重部署后键消失。
	assert.Equal(t, "postgres://v1", latestSpecEnv(t, h, ctx, appID, "web")["DATABASE_URL"])
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.26"})
	require.NoError(t, err)
	assert.NotContains(t, latestSpecEnv(t, h, ctx, appID, "web"), "DATABASE_URL")

	// tombstone 后同名复活（upsert 清 deleted_at）。
	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{
		ProjectId: projectID, Name: "DATABASE_URL", Value: "postgres://v3",
	})
	require.NoError(t, err)
	list, err = vars.ListSharedVariables(ctx, &structurev1.ListSharedVariablesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, list.GetVariables(), 1)
	assert.Equal(t, "postgres://v3", list.GetVariables()[0].GetValue())
}

// 受理面：名（env 键形态）与值大小；List 值明文回显 + 分页。
func TestSharedVariableValidationAndList(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	vars := structurev1.NewSharedVariablesServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "varshop"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{ProjectId: projectID, Name: "BAD-KEY", Value: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "variable names must match")

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{ProjectId: projectID, Name: "OK_KEY", Value: string(make([]byte, 16*1024+1))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "per-variable limit")

	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{ProjectId: projectID, Name: "A_KEY", Value: "va"})
	require.NoError(t, err)
	_, err = vars.PutSharedVariable(ctx, &structurev1.PutSharedVariableRequest{ProjectId: projectID, Name: "B_KEY", Value: "vb"})
	require.NoError(t, err)

	list, err := vars.ListSharedVariables(ctx, &structurev1.ListSharedVariablesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, list.GetVariables(), 2)
	// 值明文回显（非敏感契约，ADR-0043 决策 1）+ name 字典序。
	assert.Equal(t, "A_KEY", list.GetVariables()[0].GetName())
	assert.Equal(t, "va", list.GetVariables()[0].GetValue())

	page, err := vars.ListSharedVariables(ctx, &structurev1.ListSharedVariablesRequest{ProjectId: projectID, AfterName: "A_KEY"})
	require.NoError(t, err)
	require.Len(t, page.GetVariables(), 1)
	assert.Equal(t, "B_KEY", page.GetVariables()[0].GetName())
}
