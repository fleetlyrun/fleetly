package apitest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestDeploySpecFileFullChain（F3.5 第四源）：裸 AppSpec 全字段 intake——
// 多进程/端口/网络/探针直接声明；AppRef 权威覆写（文件内 app 声明不信任，
// 恒以 DeployRequest.app_id 行为准）；schema_version 缺省当前版。归一化
// 产物与三形态同喉（共享变量合成 + 内容寻址复用随 freezeRevision）。
func TestDeploySpecFileFullChain(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "spec-file"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "fullspec"})
	require.NoError(t, err)

	// 文件内 app ref 故意错写（覆写语义的断言面）；schema_version 缺席。
	body := `{
		"app": {"id": "NOT_THE_TARGET", "project": "ALSO_WRONG"},
		"source": {"image": {"ref": "nginx:1.27"}},
		"processes": [
			{
				"name": "web",
				"image": "nginx:1.27",
				"ports": [{"port": 8080, "protocol": "PROTOCOL_H2C"}],
				"networks": ["default"],
				"replicas": 2,
				"healthcheck": {"tcp_port": 8080}
			},
			{"name": "worker", "image": "busybox:1.37", "command": ["sleep", "3600"]}
		]
	}`
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: app.GetApp().GetId(), SpecFile: body,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return len(h.Runtime.Calls()) > 0
	}, 2e9, 1e7)
	calls := h.Runtime.Calls()
	// AppRef 覆写：期望集落在目标 App 的 ns（而非文件内声明的假 id）。
	assert.Equal(t, app.GetApp().GetId(), calls[0].NS.App)
	web := calls[0].Spec["web"]
	require.Len(t, web.Ports, 1)
	assert.Equal(t, int32(8080), web.Ports[0].Port)
	assert.Equal(t, capability.ProtocolH2C, web.Ports[0].Protocol)
	assert.Equal(t, int64(2), web.Replicas)
	assert.Equal(t, []string{"default"}, web.Networks)
	worker := calls[0].Spec["worker"]
	require.NotNil(t, worker)
	assert.Equal(t, []string{"sleep", "3600"}, worker.Command)
	// schema_version 缺省当前版（冻结体经 Revision 复用链不回读——以部署
	// 受理成功 + 期望集形态为断言面）。

	h.Runtime.ReportRunning(app.GetApp().GetId()+"-web", capability.Generation(dep.GetDeployment().GetGeneration()))
	h.Runtime.ReportRunning(app.GetApp().GetId()+"-worker", capability.Generation(dep.GetDeployment().GetGeneration()))
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 3e9, 1e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 3e9, 1e7)
}

// TestDeploySpecFileValidation：fail-closed 解码（未知字段拒）、schema
// 跳代拒、缺源拒、四源互斥、单进程旗标互斥。
func TestDeploySpecFileValidation(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "spec-file-val"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	cases := []struct {
		name string
		req  *deliveryv1.DeployRequest
		want string
	}{
		{
			"unknown field rejected",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}],"canary":true}`},
			"invalid AppSpec JSON",
		},
		{
			"schema version jump rejected",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"schema_version":99,"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}]}`},
			"unsupported schema version",
		},
		{
			"missing source rejected",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"processes":[{"name":"web","image":"nginx:1.27"}]}`},
			"one of git, image or upload is required",
		},
		{
			"no processes rejected",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[]}`},
			"at least one process is required",
		},
		{
			"four-way source mutex",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}]}`, Image: "nginx:1.27"},
			"mutually exclusive",
		},
		{
			"single-process flags mutex",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}]}`, Port: 8080},
			"spec_file carries the full process declaration",
		},
		{
			"env flag mutex",
			&deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}]}`, Env: map[string]string{"A": "b"}},
			"spec_file carries the full process declaration",
		},
	}
	for _, tc := range cases {
		_, err := deployments.Deploy(ctx, tc.req)
		require.Error(t, err, tc.name)
		assert.Contains(t, err.Error(), tc.want, tc.name)
	}
}

// TestDeploySpecFileUploadSource：spec_file 的 Source.upload 与 upload_id
// 形态同一条归属执法（project 级材料纪律不因 intake 形态放松）——跨项目
// 引用拒绝；同项目引用全链到 succeeded（含 build 链：spec_file 是 API 面
// 声明 Build/from_build 的首个 intake 通道）。
func TestDeploySpecFileUploadSource(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projA, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "sf-upload-a"})
	require.NoError(t, err)
	projB, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "sf-upload-b"})
	require.NoError(t, err)
	appB, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projB.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	up, err := uploadTar(ctx, builds, projA.GetProject().GetId(), buildFixtureTar(), 8)
	require.NoError(t, err)

	// 跨项目引用（文件内声明）拒绝。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appB.GetApp().GetId(),
		SpecFile: `{"source":{"upload":{"id":"` + up.GetId() + `"}},"build":{"builder":"dockerfile","dockerfile":"Dockerfile"},` +
			`"processes":[{"name":"web","from_build":"web"}]}`,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to project")

	// 同项目引用：app 建在 A 项目，全链到 succeeded（FakeBuilder 构建链）。
	appA, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: projA.GetProject().GetId(), Name: "builder"})
	require.NoError(t, err)
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appA.GetApp().GetId(),
		SpecFile: `{"source":{"upload":{"id":"` + up.GetId() + `"}},"build":{"builder":"dockerfile","dockerfile":"Dockerfile"},` +
			`"processes":[{"name":"web","from_build":"web"}]}`,
	})
	require.NoError(t, err)

	succeeded := false
	for i := 0; i < 30 && !succeeded; i++ {
		h.Drive(ctx)
		h.Runtime.ReportRunning(appA.GetApp().GetId()+"-web", 1)
		h.Clock.Advance(120 * time.Second)
		h.Drive(ctx)
		got, gerr := deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		if gerr == nil && got.GetDeployment().GetState() == "succeeded" {
			succeeded = true
		}
	}
	require.True(t, succeeded, "spec_file upload+build deploy must converge")
	assert.NotEmpty(t, h.Builder.Calls(), "the build chain must run for a from_build spec")
}
