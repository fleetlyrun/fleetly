package apitest_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// R02-M4：删除收口预检走驱动权谓词（含 failed 待自动回滚窗，与 converger
// 同口径）——API 无锁预检与 TeardownApp 锁内预检都不得对"仍会被驱动器
// 回滚重放"的 App 放行（否则回滚可在收口后重建载体，形成无主孤儿）。
// 回滚窗关闭（rollback_attempted=1）后删除正常。
func TestDeleteAppRefusesFailedPendingRollbackWindow(t *testing.T) {
	h := apitest.NewManual(t) // 手动驱动：planted 行不会被真环拾取成 rolling-back
	ctx := h.Ctx()
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projectID := h.CreateProject(t, "rollback-window").GetId()
	appID := h.CreateApp(t, projectID, "web").GetId()

	// 冻结一条真 Revision（部署后取消，行留终态；Revision 行可作基线锚）。
	dep, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	revID := dep.GetDeployment().GetToRevision()
	require.NotEmpty(t, revID)
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: dep.GetDeployment().GetId()})
	require.NoError(t, err)

	// failed 待自动回滚行（from_revision 非空 + rollback_attempted=0）。
	planted := &deployment.Deployment{
		ID: "01JD0FAILEDROLLBACK000000", AppID: appID,
		FromRevision: revID, ToRevision: revID,
		State: deployment.StateFailed, Generation: 2, FromGeneration: 1,
	}
	require.NoError(t, h.DB.Tx(ctx, func(run state.Runner) error {
		return deploymentrepo.New(h.DB.Clock(), run).Create(ctx, planted)
	}))

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.Error(t, err)
	require.Equal(t, "E_CONFLICT", appErrCode(t, err),
		"the failed pending-rollback window is driving; the delete must be refused")
	require.Empty(t, h.Runtime.Removed(), "the refused delete must be side-effect free")
	_, err = apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	require.NoError(t, err, "the app stays operable after the refusal")

	// 回滚窗关闭（已尝试回滚 = 终态失败）→ 删除放行。
	require.NoError(t, h.DB.Tx(ctx, func(run state.Runner) error {
		return deploymentrepo.New(h.DB.Clock(), run).Transit(ctx, planted.ID,
			[]deployment.State{deployment.StateFailed}, deployment.StateFailed,
			func(m *deployment.Deployment) { m.RollbackAttempted = true })
	}))
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID})
	require.NoError(t, err)
	require.NotEmpty(t, h.Runtime.Removed(), "after the rollback window closes the delete tears down carriers")
}
