package apitest_test

// ADR-0101 的 API 面契约锚：App 删除级联全清 revisions 而 deployment 审计
// 行保留（审计链 = 时间线，to_revision 软引用悬空为设计内终态）；窗外回滚
// = E_NOT_FOUND + 保留策略 suggestion。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// countRows 是 Harness DB 的直读帮手（级联断言走行数面，避免与 API 读径
// 的 App tombstone 404 纠缠）。
func countRows(t *testing.T, h *apitest.Harness, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, h.DB.Runner().QueryRowContext(ctx, query, args...).Scan(&n))
	return n
}

// ① 聚合终局全清：删除事务内 revisions 全清；deployment 审计行全留
// （ADR-0101 决策 4，翻 ADR-0023「不级联清历史」的 revision 半句）。
func TestDeleteAppClearsRevisions(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	apps := structurev1.NewAppsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projectID := h.CreateProject(t, "retention").GetId()
	appID := h.CreateApp(t, projectID, "web").GetId()

	// 一次部署（产 revision + deployment 行），取消到终态后删除。
	_, err := deployments.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27"})
	require.NoError(t, err)
	list, err := deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.NotEmpty(t, list.GetDeployments())
	_, err = deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: list.GetDeployments()[0].GetId()})
	require.NoError(t, err)

	// 删前基线：revision 与 deployment 行各 ≥1。
	require.NotZero(t, countRows(t, h, ctx,
		`SELECT COUNT(*) FROM revisions WHERE app_id = ?`, appID), "deploy must freeze a revision")
	depCount := countRows(t, h, ctx, `SELECT COUNT(*) FROM deployments WHERE app_id = ?`, appID)
	require.NotZero(t, depCount)

	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Confirmation: yesSkip(), Id: appID})
	require.NoError(t, err)

	assert.Zero(t, countRows(t, h, ctx, `SELECT COUNT(*) FROM revisions WHERE app_id = ?`, appID),
		"revisions are cleared with the app (ADR-0101 decision 4)")
	assert.Equal(t, depCount, countRows(t, h, ctx, `SELECT COUNT(*) FROM deployments WHERE app_id = ?`, appID),
		"deployment audit rows survive app deletion (timeline is the audit chain)")
}

// ② 窗外回滚：目标 revision 已被保留策略清理 → E_NOT_FOUND + suggestion
// （ADR-0101 决策 7；裁剪行为本身由 engine 白盒承载，此处直删模拟）。
func TestRollbackOutsideRetentionWindow(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projectID := h.CreateProject(t, "retention-rollback").GetId()
	appID := h.CreateApp(t, projectID, "web").GetId()

	revID := appID + "-r1"
	_, err := h.DB.Runner().ExecContext(ctx, `
		INSERT INTO revisions (id, app_id, seq, digest, spec, source_text, created_at)
		VALUES (?, ?, 1, 'deadbeef', '{}', '', '2026-11-18T00:00:00Z')`,
		revID, appID)
	require.NoError(t, err)
	// 模拟保留窗清理：行删除后，回滚锚悬空。
	_, err = h.DB.Runner().ExecContext(ctx, `DELETE FROM revisions WHERE id = ?`, revID)
	require.NoError(t, err)

	_, err = deployments.Rollback(ctx, &deliveryv1.RollbackRequest{
		Confirmation: yesSkip(), AppId: appID, ToRevision: revID,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "E_NOT_FOUND")
	// suggestion 走 errcode 信封面（apperrstatus），不在错误串里。
	env, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_NOT_FOUND", env.Code())
	assert.Contains(t, env.Suggestion(), "retention policy",
		"outside-window rollback carries the retention suggestion")
}
