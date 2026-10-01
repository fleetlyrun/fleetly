package schedule_test

import (
	"context"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// mustParseTime 是测试内固定字面量的便捷面（statetest.MustParse 同款）。
func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("bad fixture time: " + err.Error())
	}
	return t
}

func newSchedule(id, name string) *schedule.Schedule {
	return &schedule.Schedule{
		ID: id, ProjectID: "01JD0PRJ000000000000000000", Name: name,
		State: schedule.StateActive, CronExpr: "30 9 * * *", Timezone: "UTC",
		Spec:       []byte(`{"schemaVersion":1}`),
		NextFireAt: "2026-01-01T09:30:00Z",
	}
}

func TestScheduleCRUDAndNameUnique(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := schedule.New(clock)

	s := newSchedule("01JD0SCH00000000000000000", "nightly-migrate")
	require.NoError(t, repo.Create(ctx, db.Runner(), s))

	got, err := repo.Get(ctx, db.Runner(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, schedule.StateActive, got.State)
	assert.Equal(t, "2026-01-01T09:30:00Z", got.NextFireAt)
	assert.Equal(t, "2026-01-01T00:00:00Z", got.CreatedAt)

	// 活跃名唯一；同项目同名 → ErrAlreadyExists。
	err = repo.Create(ctx, db.Runner(), newSchedule("01JD0SCH00000000000000001", "nightly-migrate"))
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// tombstone 释放名位。
	require.NoError(t, repo.Transit(ctx, db.Runner(), s.ID, schedule.StateActive, schedule.StateDeleted))
	require.NoError(t, repo.Create(ctx, db.Runner(), newSchedule("01JD0SCH00000000000000002", "nightly-migrate")))

	// deleted 行照常可读（终态事实可见）；终态不再迁移。
	got, err = repo.Get(ctx, db.Runner(), s.ID)
	require.NoError(t, err)
	assert.True(t, got.State.Terminal())
	err = repo.Transit(ctx, db.Runner(), s.ID, schedule.StateActive, schedule.StateDeleted)
	assert.ErrorIs(t, err, state.ErrConflict)
}

func TestScheduleListDrivingAndCursor(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := schedule.New(clock)

	ids := []string{"01JD0SCH00000000000000000", "01JD0SCH00000000000000001", "01JD0SCH00000000000000002"}
	for i, id := range ids {
		s := newSchedule(id, "")
		if i == 2 {
			s.State = schedule.StateDeleted
		}
		require.NoError(t, repo.Create(ctx, db.Runner(), s))
	}

	driving, err := repo.ListDriving(ctx, db.Runner())
	require.NoError(t, err)
	assert.Len(t, driving, 2) // tombstone 不拾取

	// 新→旧 + after 游标（列表含 tombstone——终态事实可见，与 tasks 同款）。
	page, err := repo.ListByProject(ctx, db.Runner(), "01JD0PRJ000000000000000000", "", 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, ids[2], page[0].ID)
	page, err = repo.ListByProject(ctx, db.Runner(), "01JD0PRJ000000000000000000", page[0].ID, 10)
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, ids[1], page[0].ID)
	assert.Equal(t, ids[0], page[1].ID)
}

func TestScheduleFireCAS(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := schedule.New(clock)

	s := newSchedule("01JD0SCH00000000000000000", "migrate")
	require.NoError(t, repo.Create(ctx, db.Runner(), s))

	// from 不匹配（双发/陈读防御）→ ErrConflict。
	err := repo.Fire(ctx, db.Runner(), s.ID, "2026-01-02T09:30:00Z", "2026-01-03T09:30:00Z", "01JD0TSK00000000000000000")
	assert.ErrorIs(t, err, state.ErrConflict)

	// from 匹配：next_fire_at 与 last_task_id 同拍推进。
	require.NoError(t, repo.Fire(ctx, db.Runner(), s.ID,
		"2026-01-01T09:30:00Z", "2026-01-02T09:30:00Z", "01JD0TSK00000000000000000"))
	got, err := repo.Get(ctx, db.Runner(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T09:30:00Z", got.NextFireAt)
	assert.Equal(t, "01JD0TSK00000000000000000", got.LastTaskID)

	// 手动拍记账：last_task_id 推进、next_fire_at 不动。
	require.NoError(t, repo.RecordFire(ctx, db.Runner(), s.ID, "01JD0TSK00000000000000001"))
	got, err = repo.Get(ctx, db.Runner(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, "2026-01-02T09:30:00Z", got.NextFireAt)
	assert.Equal(t, "01JD0TSK00000000000000001", got.LastTaskID)

	// tombstone 后 Fire/RecordFire 均拒。
	require.NoError(t, repo.Transit(ctx, db.Runner(), s.ID, schedule.StateActive, schedule.StateDeleted))
	assert.ErrorIs(t, repo.Fire(ctx, db.Runner(), s.ID,
		"2026-01-02T09:30:00Z", "2026-01-03T09:30:00Z", "x"), state.ErrConflict)
	assert.ErrorIs(t, repo.RecordFire(ctx, db.Runner(), s.ID, "x"), state.ErrConflict)

	// 不存在的行 → ErrNotFound。
	assert.ErrorIs(t, repo.Fire(ctx, db.Runner(), "01JD0SCH000000000000099", "", "", ""), state.ErrNotFound)
}

func TestParseCronTimezoneAndDST(t *testing.T) {
	// 基本拍点：UTC 墙钟。
	sched, err := schedule.ParseCron("30 9 * * *", "UTC")
	require.NoError(t, err)
	next := sched.Next(mustParseTime("2026-01-01T00:00:00Z"))
	assert.Equal(t, "2026-01-01T09:30:00Z", next.UTC().Format(time.RFC3339))

	// 时区墙钟：东京 12:00 = UTC 03:00（IANA 名随行解释）。
	sched, err = schedule.ParseCron("0 12 * * *", "Asia/Tokyo")
	require.NoError(t, err)
	next = sched.Next(mustParseTime("2026-01-01T00:00:00Z"))
	assert.Equal(t, "2026-01-01T03:00:00Z", next.UTC().Format(time.RFC3339))

	// 春跳（America/New_York 2026-03-08 02:00→03:00，2:30 不存在）：
	// 按墙钟解释，缺口内的拍点当日不触发（Vixie cron 同款），次日 2:30
	// EDT（06:30Z）恢复——不静默归一到 3:30，也不跳周。
	sched, err = schedule.ParseCron("30 2 * * *", "America/New_York")
	require.NoError(t, err)
	next = sched.Next(mustParseTime("2026-03-07T12:00:00Z"))
	assert.Equal(t, "2026-03-09T06:30:00Z", next.UTC().Format(time.RFC3339))

	// 非法输入：字段数错 / 5 字段域外 / 坏时区名 / 描述符拒绝。
	_, err = schedule.ParseCron("30 9 * *", "UTC")
	assert.Error(t, err)
	_, err = schedule.ParseCron("99 9 * * *", "UTC")
	assert.Error(t, err)
	_, err = schedule.ParseCron("30 9 * * *", "Mars/Olympus")
	assert.Error(t, err)
	_, err = schedule.ParseCron("@daily", "UTC")
	assert.Error(t, err)
	_, err = schedule.ParseCron("", "")
	assert.Error(t, err)

	// 空 timezone 按 UTC（创建面归一为显式值的防御半边）。
	sched, err = schedule.ParseCron("0 0 * * *", "")
	require.NoError(t, err)
	var _ cron.Schedule = sched // 接口面钉住（robfig Schedule 是 Next 的契约）
}
