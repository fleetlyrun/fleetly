package apitest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
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
			ProjectId: pid, Name: "c" + twoDigits(i), Content: "v",
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

func twoDigits(i int) string {
	if i < 10 {
		return "0" + string(rune('0'+i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
