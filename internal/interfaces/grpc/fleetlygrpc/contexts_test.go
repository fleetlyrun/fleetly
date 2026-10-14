package fleetlygrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
)

// A3 回归（N0 修复批）：logs --process 曾是死旗标（CLI 丢字段 + API 不消
// 费）。钉死请求 → LogQuery 翻译契约：process 经投影公式合成 Workload ID、
// since/until 时间窗解析、坏值精确拒绝、Team 轴实取（ADR-0035 前硬编码
// "default"——归一化入参后透传 NamespaceRef.Team）。
func TestLogQueryFromRequest(t *testing.T) {
	appRow := &app.App{ID: "01JD0APP000000000000000000", ProjectID: "01JD0PROJ00000000000000000"}

	t.Run("process composes workload id via the projection formula", func(t *testing.T) {
		q, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{
			AppId: appRow.ID, Process: "web", TailLines: 100, Follow: true,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{appRow.ID + "-web"}, q.WorkloadIDs, "process must filter by the projected workload ID set")
		assert.Equal(t, int64(100), q.TailLines)
		assert.True(t, q.Follow)
		assert.Equal(t, appRow.ProjectID, q.Namespace.Project)
		assert.Equal(t, "acme", q.Namespace.Team)
	})

	t.Run("no process leaves workload filter open", func(t *testing.T) {
		q, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{AppId: appRow.ID})
		require.NoError(t, err)
		assert.Empty(t, q.WorkloadIDs)
	})

	t.Run("processes union with process and dedupe to the workload set", func(t *testing.T) {
		q, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{
			AppId: appRow.ID, Process: "web", Processes: []string{"web", "worker"},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{appRow.ID + "-web", appRow.ID + "-worker"}, q.WorkloadIDs,
			"process and processes union (deduped) into the workload set")
	})

	t.Run("empty processes entry is rejected exactly", func(t *testing.T) {
		_, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{Processes: []string{"web", ""}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
		assert.Contains(t, err.Error(), "processes")
	})

	t.Run("since and until parse as RFC3339", func(t *testing.T) {
		q, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{
			AppId: appRow.ID,
			Since: "2026-10-01T00:00:00Z",
			Until: "2026-10-01T01:00:00+02:00",
		})
		require.NoError(t, err)
		assert.Equal(t, "2026-10-01T00:00:00Z", q.Since.Format("2006-01-02T15:04:05Z07:00"))
		assert.Equal(t, "2026-10-01T01:00:00+02:00", q.Until.Format("2006-01-02T15:04:05Z07:00"))
	})

	t.Run("malformed window is an explicit invalid argument", func(t *testing.T) {
		_, err := logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{Since: "yesterday"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
		assert.Contains(t, err.Error(), "since")

		_, err = logQueryFromRequest("acme", appRow, &telemetryv1.StreamLogsRequest{Until: "2026-13-99"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "until")
	})
}
