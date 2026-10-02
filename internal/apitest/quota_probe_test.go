package apitest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// B2 执法面回归（N0.1 P2-8）：Config 配额——per-Project 100 个上限
// （E_QUOTA_EXCEEDED）、单值 256KiB 上限（E_INVALID_ARGUMENT）、同名 put
// 是新版本不占新位。
func TestConfigQuotaEnforcement(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	configs := structurev1.NewConfigsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "quota"})
	require.NoError(t, err)
	pid := proj.GetProject().GetId()

	// 单值上限：256KiB 过界即拒（精确 256KiB+1）。
	big := strings.Repeat("a", 256*1024)
	_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: pid, Name: "big", Content: big})
	require.NoError(t, err)
	_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: pid, Name: "toobig", Content: big + "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
	require.Contains(t, err.Error(), "per-config limit")

	// 数量上限：1（big）+ 99 个新名 = 100 满；第 101 个新名拒配额。
	for i := 0; i < 99; i++ {
		_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{
			ProjectId: pid, Name: fmt.Sprintf("c%02d", i), Content: "v",
		})
		require.NoError(t, err)
	}
	_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: pid, Name: "overflow", Content: "v"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	require.Contains(t, err.Error(), "limit 100")

	// 同名 put 是新版本：不占新位，满员后仍可写既有名。
	_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: pid, Name: "big", Content: "shrink"})
	require.NoError(t, err)

	// 配额按 Project 计：另一 Project 不受影响。
	proj2, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "quota2"})
	require.NoError(t, err)
	_, err = configs.PutConfig(ctx, &structurev1.PutConfigRequest{ProjectId: proj2.GetProject().GetId(), Name: "free", Content: "v"})
	require.NoError(t, err)
}

// TestTaskAndAppQuotaEnforcement（ADR-0017 附录 A.1，F1.9）：Task 双配额
// （活跃行数 / 并发总量）与 App 配额在受理位事务内执法；per-Task
// desired_concurrency 的 sanity 上限收口为项目总量配额（大单值走配额面
// 而非 E_INVALID_ARGUMENT）。
func TestTaskAndAppQuotaEnforcement(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	tasksAPI := automationv1.NewTasksServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "brakes"})
	require.NoError(t, err)
	pid := proj.GetProject().GetId()

	// 直接灌行到边界前一步：99 条活跃 one-shot×desired 2（active=99、sum=198）。
	tasksRepo := task.New(h.DB.Clock())
	for i := 0; i < 99; i++ {
		row := &task.Task{
			ID: fmt.Sprintf("01JDQTASK0000000000000%03dQ", i), ProjectID: pid,
			Form: task.FormOneShot, State: task.StateActive, Spec: []byte(`{"schema_version":1}`),
			DesiredConcurrency: 2, DNSName: fmt.Sprintf("fleetly-task-%03d", i),
		}
		require.NoError(t, tasksRepo.Create(ctx, h.DB.Runner(), row))
	}

	// 并发总量面：198 + 3 > 200 → 配额拒。
	_, err = tasksAPI.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: pid, Image: "busybox:1.37", DesiredConcurrency: 3,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	require.Contains(t, err.Error(), "would exceed")

	// 收口锚：desired 201 的单请求走配额面（不是 E_INVALID_ARGUMENT sanity）。
	_, err = tasksAPI.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: pid, Image: "busybox:1.37", DesiredConcurrency: 201,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")

	// 数量面：第 100 条收下（sum 199），第 101 条按行数拒。
	made, err := tasksAPI.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: pid, Image: "busybox:1.37", Form: "resident", DesiredConcurrency: 1,
	})
	require.NoError(t, err)
	_, err = tasksAPI.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: pid, Image: "busybox:1.37", DesiredConcurrency: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	require.Contains(t, err.Error(), "active tasks (limit 100)")

	// ScaleTask 增量面：199→200 放行；→201 拒。
	scaled, err := tasksAPI.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: made.GetTask().GetId(), DesiredConcurrency: 2})
	require.NoError(t, err)
	require.EqualValues(t, 2, scaled.GetTask().GetDesiredConcurrency())
	_, err = tasksAPI.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: made.GetTask().GetId(), DesiredConcurrency: 3})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")

	// App 配额：50 活跃行满员后拒第 51。
	appsRepo := app.New(h.DB.Clock())
	for i := 0; i < 50; i++ {
		require.NoError(t, appsRepo.Create(ctx, h.DB.Runner(), &app.App{
			ID: fmt.Sprintf("01JDQAPPS0000000000000%03dX", i), ProjectID: pid,
			Name: fmt.Sprintf("svc-%02d", i),
		}))
	}
	_, err = structurev1.NewAppsServiceClient(h.Conn).CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: pid, Name: "overflow"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	require.Contains(t, err.Error(), "limit 50")

	// 配额按 Project 计：另一 Project 的首单不受影响。
	proj2, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "brakes2"})
	require.NoError(t, err)
	_, err = tasksAPI.CreateTask(ctx, &automationv1.CreateTaskRequest{
		ProjectId: proj2.GetProject().GetId(), Image: "busybox:1.37", DesiredConcurrency: int64(engine.MaxTaskConcurrencyPerProject),
	})
	require.NoError(t, err)
}

// 探针声明执法（B2 + N0.1 P2-2/P2-8）：http/tcp 互斥、compose 形态不收
// 直投探针、tcp 端口域、http 绝对路径。
func TestDeployProbeDeclarationValidation(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := structurev1.NewProjectsServiceClient(h.Conn).CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "probes"})
	require.NoError(t, err)
	app, err := apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	id := app.GetApp().GetId()

	for _, tc := range []struct {
		name string
		req  *deliveryv1.DeployRequest
		want string
	}{
		{"http and tcp are mutually exclusive", &deliveryv1.DeployRequest{AppId: id, Image: "nginx:1.27", HttpProbe: "/healthz", TcpProbe: 8080}, "mutually exclusive"},
		{"http path must be absolute", &deliveryv1.DeployRequest{AppId: id, Image: "nginx:1.27", HttpProbe: "healthz"}, "starting with '/'"},
		{"tcp port range", &deliveryv1.DeployRequest{AppId: id, Image: "nginx:1.27", TcpProbe: 70000}, "port out of range"},
		{"compose rejects direct probes", &deliveryv1.DeployRequest{AppId: id, ComposeYaml: "services:\n  web:\n    image: nginx\n", HttpProbe: "/healthz"}, "compose declares probes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := deployments.Deploy(ctx, tc.req)
			require.Error(t, err)
			require.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
			require.Contains(t, err.Error(), tc.want)
		})
	}

	// 合法形态照常受理（http 单独声明）。
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: id, Image: "nginx:1.27", HttpProbe: "/healthz"})
	require.NoError(t, err)
}
