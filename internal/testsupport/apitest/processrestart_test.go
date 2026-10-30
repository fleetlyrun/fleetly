package apitest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// TestRestartProcessVerb（ADR-0069）：进程重启 API 全链——已知进程强制重排
// （RuntimeRestart 子面收到 app 域 ns + 投影公式 Workload ID）；未部署与
// 未知进程 E_NOT_FOUND（引擎先校验冻结体——k3s 驱逐对零命中静默，防假
// 成功的唯一防线）；冻结期 E_CHANGE_FROZEN 拒绝（变更族执法）。
func TestRestartProcessVerb(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projectID := h.CreateProject(t, "restart").GetId()
	appID := h.CreateApp(t, projectID, "svc").GetId()

	// 未部署（无冻结体）：同归 E_NOT_FOUND。
	_, err := deployments.RestartProcess(ctx, &deliveryv1.RestartProcessRequest{AppId: appID, ProcessName: "web"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")

	// 部署 web+worker 双进程（Revision 冻结同步完成，部署异步推进）。
	body := `{"app":{"id":"X","project":"Y"},"source":{"image":{"ref":"nginx:1.27"}},"processes":[` +
		`{"name":"web","image":"nginx:1.27","replicas":1},` +
		`{"name":"worker","image":"busybox:1.37","command":["sleep","3600"]}]}`
	_, err = deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: body})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(h.Runtime.Calls()) > 0 }, 2e9, 1e7)

	// 已知进程：真滚动（app 域 ns + 投影 Workload ID；零 lineage——无新
	// Ensure 调用、无新 Deployment）。
	deploysBefore := len(h.Runtime.Calls())
	_, err = deployments.RestartProcess(ctx, &deliveryv1.RestartProcessRequest{AppId: appID, ProcessName: "worker"})
	require.NoError(t, err)
	restarts := h.Runtime.RestartCalls()
	require.Len(t, restarts, 1)
	assert.Equal(t, appID+"-worker", restarts[0].WorkloadID)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: projectID, App: appID}, restarts[0].NS)
	assert.Len(t, h.Runtime.Calls(), deploysBefore, "restart must not trigger a re-Ensure (zero lineage)")

	// 未知进程（typo）：精确拒绝而非静默成功。
	_, err = deployments.RestartProcess(ctx, &deliveryv1.RestartProcessRequest{AppId: appID, ProcessName: "wrker"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
}

// TestRestartProcessFrozen（ADR-0069 冻结归族）：RestartProcess 入
// FrozenVerbs 封禁面——冻结期拒绝，与 Deploy/Rollback 同映射。
func TestRestartProcessFrozen(t *testing.T) {
	h := apitest.New(t)
	owner := h.Ctx()
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)

	team, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "frozen-restart"})
	require.NoError(t, err)
	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{
		Name: "frozen-restart", TeamId: team.GetTeam().GetId(),
	})
	require.NoError(t, err)
	// 冻结守卫经归属解析图（App 行 → Project → Team）定位封禁面——app 行
	// 须真实在场。
	app := h.CreateApp(t, proj.GetProject().GetId(), "web")
	_, err = gov.SetChangeFreeze(owner, &systemv1.SetChangeFreezeRequest{TeamId: team.GetTeam().GetId(), Reason: "migration window"})
	require.NoError(t, err)

	_, err = deliveryv1.NewDeploymentsServiceClient(h.Conn).RestartProcess(owner,
		&deliveryv1.RestartProcessRequest{AppId: app.GetId(), ProcessName: "web"})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")
}
