package apitest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// TestChangeFreezeLifecycle（F1.9，ADR-0017 附录 A.3）：per-Team 冻结 →
// 封禁面拒绝（E_CHANGE_FROZEN / FailedPrecondition，reason 可见）→ 其他
// Team 与停止族豁免 → lift 恢复；freeze.set/lifted 事件入流。webhook push
// 同受冻结（无 Token 的部署触发面不得漏拦）。
func TestChangeFreezeLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	tasksAPI := automationv1.NewTasksServiceClient(h.Conn)
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)

	teamA, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "alpha"})
	require.NoError(t, err)
	teamB, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "beta"})
	require.NoError(t, err)
	projA, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "alpha-main", TeamId: teamA.GetTeam().GetId()})
	require.NoError(t, err)
	projB, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "beta-main", TeamId: teamB.GetTeam().GetId()})
	require.NoError(t, err)

	// 冻结前置事实：A 域 App + Task + Git hook。
	appA, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projA.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	taskA, err := tasksAPI.CreateTask(owner, &automationv1.CreateTaskRequest{
		ProjectId: projA.GetProject().GetId(), Image: "busybox:1.37",
	})
	require.NoError(t, err)
	set, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appA.GetApp().GetId(), Repo: "https://github.com/acme/alpha.git", Branch: "main",
	})
	require.NoError(t, err)
	hookSecret := set.GetSecret()

	// 冻结 Team A（reason 是拒绝信封的可见锚）。
	const reason = "database migration"
	frozen, err := gov.SetChangeFreeze(owner, &systemv1.SetChangeFreezeRequest{TeamId: teamA.GetTeam().GetId(), Reason: reason})
	require.NoError(t, err)
	freezeID := frozen.GetFreeze().GetId()

	// 封禁面：CreateApp / CreateTask / Deploy 拒（FailedPrecondition 族——
	// E_CONFLICT 同映射面，测试断言按 apperr 信封码走）。
	_, err = apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projA.GetProject().GetId(), Name: "worker"})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")
	assert.Contains(t, err.Error(), reason)

	_, err = tasksAPI.CreateTask(owner, &automationv1.CreateTaskRequest{ProjectId: projA.GetProject().GetId(), Image: "busybox:1.37"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")

	_, err = deliveryv1.NewDeploymentsServiceClient(h.Conn).Deploy(owner, &deliveryv1.DeployRequest{
		AppId: appA.GetApp().GetId(), Image: "nginx:1.27",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")

	// webhook push（PUBLIC 无 Token 的部署触发面）同拦。
	_, err = hooks.ReceiveWebhook(context.Background(), &deliveryv1.ReceiveWebhookRequest{
		Token: hookSecret, Event: "push", Delivery: "d-frozen",
		Signature: signPayload(hookSecret, pushBody("refs/heads/main", "f1", "fix", "web/x")),
		Payload:   []byte(pushBody("refs/heads/main", "f1", "fix", "web/x")),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")

	// 豁免面：停止族在冻结期可用；其他 Team 不受影响。
	_, err = tasksAPI.StopTask(owner, &automationv1.StopTaskRequest{Id: taskA.GetTask().GetId()})
	require.NoError(t, err)
	_, err = apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projB.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	// 同 scope 重复 Set → E_ALREADY_EXISTS（先 lift）。
	_, err = gov.SetChangeFreeze(owner, &systemv1.SetChangeFreezeRequest{TeamId: teamA.GetTeam().GetId(), Reason: "again"})
	require.Error(t, err)
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// lift → 封禁面恢复；幂等 lift 再成功。
	_, err = gov.LiftChangeFreeze(owner, &systemv1.LiftChangeFreezeRequest{Id: freezeID})
	require.NoError(t, err)
	_, err = gov.LiftChangeFreeze(owner, &systemv1.LiftChangeFreezeRequest{Id: freezeID})
	require.NoError(t, err)
	_, err = apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projA.GetProject().GetId(), Name: "worker"})
	require.NoError(t, err)

	// 历史行可回读（lifted_at 非空）。
	list, err := gov.ListChangeFreezes(owner, &systemv1.ListChangeFreezesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetFreezes(), 1)
	assert.NotEmpty(t, list.GetFreezes()[0].GetLiftedAt(), "historical row stays readable")

	// 事件三链面：freeze.set / freeze.lifted 入流（aggregate=freeze）。
	events := peerEventNames(t, owner, h, "freeze", freezeID)
	assert.Equal(t, []string{"freeze.set", "freeze.lifted"}, events)
}
