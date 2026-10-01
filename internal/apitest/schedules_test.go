package apitest_test

// Schedule API 全链（F1.7，ADR-0018）：创建（受理位 + cron 校验 + 首拍
// 铸点 + schedule.created）、到期拍全链（铸 Task → 补足 Run → 收口）、
// 手动触发与重叠拒绝、幂等键重放、删除幂等。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestScheduleAPILifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projectID := taskFixtureProject(t, h, ctx, "sched")

	sc := automationv1.NewSchedulesServiceClient(h.Conn)

	// 创建：时区归一（空 → UTC）+ 首拍铸点（fake now 2026-01-01T00:00:00Z，
	// "30 9 * * *" → 当日 09:30Z）。
	created, err := sc.CreateSchedule(ctx, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Name: "nightly-backup",
		Cron: "30 9 * * *", Image: "busybox:1.37", Command: []string{"/backup"},
		TtlSeconds: 3600, NetworkGroup: "backup",
	})
	require.NoError(t, err)
	s := created.GetSchedule()
	scheduleID := s.GetId()
	assert.Equal(t, "active", s.GetState())
	assert.Equal(t, "UTC", s.GetTimezone())
	assert.Equal(t, "30 9 * * *", s.GetCron())
	assert.Equal(t, "2026-01-01T09:30:00Z", s.GetNextFireAt())
	assert.Equal(t, "busybox:1.37", s.GetImage())
	assert.Empty(t, s.GetLastTaskId())

	// 显式时区：东京 12:00 = UTC 03:00 次日起算（首拍大于当前时刻）。
	tzCreated, err := sc.CreateSchedule(ctx, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Name: "tokyo-noon",
		Cron: "0 12 * * *", Timezone: "Asia/Tokyo", Image: "busybox:1.37",
	})
	require.NoError(t, err)
	assert.Equal(t, "Asia/Tokyo", tzCreated.GetSchedule().GetTimezone())
	assert.Equal(t, "2026-01-01T03:00:00Z", tzCreated.GetSchedule().GetNextFireAt())

	// cron/时区校验面。
	_, err = sc.CreateSchedule(ctx, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Cron: "99 9 * * *", Image: "busybox:1.37",
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "out-of-range cron field must be rejected")
	_, err = sc.CreateSchedule(ctx, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Cron: "30 9 * * *", Timezone: "Mars/Olympus", Image: "busybox:1.37",
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "bad IANA name must be rejected")

	// 幂等键重放：同键同体 → 同响应。
	ctxKeyed := sdk.WithIdempotencyKey(ctx, "sched-once-1")
	first, err := sc.CreateSchedule(ctxKeyed, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Name: "idem", Cron: "0 0 * * *", Image: "busybox:1.37",
	})
	require.NoError(t, err)
	replay, err := sc.CreateSchedule(ctxKeyed, &automationv1.CreateScheduleRequest{
		ProjectId: projectID, Name: "idem", Cron: "0 0 * * *", Image: "busybox:1.37",
	})
	require.NoError(t, err)
	assert.Equal(t, first.GetSchedule().GetId(), replay.GetSchedule().GetId())

	// 到期拍全链：推过 09:30 → Drive → 铸 Task → 补足 Run。
	h.Clock.Advance(10 * time.Hour)
	h.Drive(ctx)

	got, err := sc.GetSchedule(ctx, &automationv1.GetScheduleRequest{Id: scheduleID})
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T09:30:00Z", got.GetSchedule().GetNextFireAt())
	assert.NotEmpty(t, got.GetSchedule().GetLastTaskId())

	rc := automationv1.NewRunsServiceClient(h.Conn)
	runs, err := rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: got.GetSchedule().GetLastTaskId()})
	require.NoError(t, err)
	require.Len(t, runs.GetRuns(), 1, "fired task replenishes exactly one run")
	assert.Contains(t, runs.GetRuns()[0].GetDnsName(), "run-")

	// Ensure 走 Task 域 ns（F1.5 机制复用的 API 面证据）。
	ensures := h.Runtime.Calls()
	var firedEnsure *apitest.EnsureCall
	for i := range ensures {
		if ensures[i].NS.Task == got.GetSchedule().GetLastTaskId() {
			firedEnsure = &ensures[i]
		}
	}
	require.NotNil(t, firedEnsure, "fired task must be ensured in the task domain")
	assert.Equal(t, capability.RestartNever, firedEnsure.Spec["run"].Restart)

	// 手动触发：在途 Run → 诚实拒绝（E_CONFLICT → FailedPrecondition）。
	_, err = sc.TriggerSchedule(ctx, &automationv1.TriggerScheduleRequest{Id: scheduleID})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// Run 收口 → 手动触发成功；cron 节奏不动、last_task_id 换新拍。
	h.Runtime.ReportCompleted(runs.GetRuns()[0].GetId(), capability.Generation(1))
	h.Drive(ctx)
	before := got.GetSchedule().GetNextFireAt()
	prevTask := got.GetSchedule().GetLastTaskId()
	trig, err := sc.TriggerSchedule(ctx, &automationv1.TriggerScheduleRequest{Id: scheduleID})
	require.NoError(t, err)
	assert.Equal(t, before, trig.GetSchedule().GetNextFireAt(), "manual fire must not shift the cadence")
	assert.NotEqual(t, prevTask, trig.GetSchedule().GetLastTaskId(), "manual fire spawns a fresh task")

	// 新拍的 Task 进驱动链。
	h.Drive(ctx)
	afterRuns, err := rc.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: trig.GetSchedule().GetLastTaskId()})
	require.NoError(t, err)
	assert.Len(t, afterRuns.GetRuns(), 1)

	// 列表（含 after 游标）+ 删除幂等 + tombstone 行可读。
	listed, err := sc.ListSchedules(ctx, &automationv1.ListSchedulesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, listed.GetSchedules(), 3)
	paged, err := sc.ListSchedules(ctx, &automationv1.ListSchedulesRequest{
		ProjectId: projectID, AfterScheduleId: listed.GetSchedules()[0].GetId(),
	})
	require.NoError(t, err)
	assert.Len(t, paged.GetSchedules(), 2)
	_, err = sc.DeleteSchedule(ctx, &automationv1.DeleteScheduleRequest{Id: scheduleID})
	require.NoError(t, err)
	_, err = sc.DeleteSchedule(ctx, &automationv1.DeleteScheduleRequest{Id: scheduleID})
	require.NoError(t, err)
	got, err = sc.GetSchedule(ctx, &automationv1.GetScheduleRequest{Id: scheduleID})
	require.NoError(t, err)
	assert.Equal(t, "deleted", got.GetSchedule().GetState())
}
