package fleetlygrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/state/app"
)

// A3 回归（N0 修复批）：logs --process 曾是死旗标（CLI 丢字段 + API 不消
// 费）。钉死请求 → LogQuery 翻译契约：process 经投影公式合成 Workload ID、
// since/until 时间窗解析、坏值精确拒绝。
func TestLogQueryFromRequest(t *testing.T) {
	appRow := &app.App{ID: "01JD0APP000000000000000000", ProjectID: "01JD0PROJ00000000000000000"}

	t.Run("process composes workload id via the projection formula", func(t *testing.T) {
		q, err := logQueryFromRequest(appRow, &telemetryv1.StreamLogsRequest{
			AppId: appRow.ID, Process: "web", TailLines: 100, Follow: true,
		})
		require.NoError(t, err)
		assert.Equal(t, appRow.ID+"-web", q.WorkloadID, "process must filter by the projected workload ID")
		assert.Equal(t, int64(100), q.TailLines)
		assert.True(t, q.Follow)
		assert.Equal(t, appRow.ProjectID, q.Namespace.Project)
	})

	t.Run("no process leaves workload filter open", func(t *testing.T) {
		q, err := logQueryFromRequest(appRow, &telemetryv1.StreamLogsRequest{AppId: appRow.ID})
		require.NoError(t, err)
		assert.Empty(t, q.WorkloadID)
	})

	t.Run("since and until parse as RFC3339", func(t *testing.T) {
		q, err := logQueryFromRequest(appRow, &telemetryv1.StreamLogsRequest{
			AppId: appRow.ID,
			Since: "2026-10-01T00:00:00Z",
			Until: "2026-10-01T01:00:00+02:00",
		})
		require.NoError(t, err)
		assert.Equal(t, "2026-10-01T00:00:00Z", q.Since.Format("2006-01-02T15:04:05Z07:00"))
		assert.Equal(t, "2026-10-01T01:00:00+02:00", q.Until.Format("2006-01-02T15:04:05Z07:00"))
	})

	t.Run("malformed window is an explicit invalid argument", func(t *testing.T) {
		_, err := logQueryFromRequest(appRow, &telemetryv1.StreamLogsRequest{Since: "yesterday"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
		assert.Contains(t, err.Error(), "since")

		_, err = logQueryFromRequest(appRow, &telemetryv1.StreamLogsRequest{Until: "2026-13-99"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "until")
	})
}
