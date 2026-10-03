package engine

// runsByTask 视图测试（taskview.go，2026-10-03 架构评审候选 2）：方向锚
// 由构造保证（与入口查询方向无关）、双占用规则、排空补停触发锚。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// TestNewRunsByTaskEnforcesDirection：序由构造保证——输入顺序任意
// （ListDriving 批量 / ListByTaskStates 重列两入口同构造），恒为新→旧
// （ULID 字典序降序）。方向倒置即停老保新（P1#7 实证缺陷）。
func TestNewRunsByTaskEnforcesDirection(t *testing.T) {
	shuffled := []run.Run{
		{ID: "01JD0RUN0000000000000000A2", TaskID: "T1", State: run.StatePending},
		{ID: "01JD0RUN0000000000000000Z9", TaskID: "T1", State: run.StateRunning}, // 最新
		{ID: "01JD0RUN0000000000000000A1", TaskID: "T1", State: run.StateStopping},
	}
	v := newRunsByTask(shuffled)
	require.Len(t, v, 3)
	assert.Equal(t, "01JD0RUN0000000000000000Z9", v[0].ID, "head is newest (id DESC)")
	assert.Equal(t, "01JD0RUN0000000000000000A2", v[1].ID)
	assert.Equal(t, "01JD0RUN0000000000000000A1", v[2].ID)
	assert.Empty(t, newRunsByTask(nil))
}

// TestRunsByTaskOccupancyDualRule：双占用规则唯一判定点——resident 按
// 活槽位（stopping 不占位）；one-shot 按全部驱动行（stopping 也占位——
// 唯一 Run 停止收口中即补第二 Run = job 多跑一次）。
func TestRunsByTaskOccupancyDualRule(t *testing.T) {
	rows := []run.Run{
		{ID: "R1", TaskID: "T1", State: run.StatePending},
		{ID: "R2", TaskID: "T1", State: run.StateRunning},
		{ID: "R3", TaskID: "T1", State: run.StateStopping, StopReason: "stopped_by_user"},
		{ID: "R4", TaskID: "T1", State: run.StateStopping, StopReason: ""},
	}
	v := newRunsByTask(rows)
	assert.Equal(t, 2, v.liveCount())
	assert.Equal(t, 2, v.occupiedFor(task.FormResident), "resident: stopping rows do not occupy")
	assert.Equal(t, 4, v.occupiedFor(task.FormOneShot), "one-shot: every driving row occupies, stopping included")

	stoppingOnly := newRunsByTask([]run.Run{{ID: "R3", TaskID: "T1", State: run.StateStopping}})
	assert.Equal(t, 0, stoppingOnly.liveCount())
	assert.Equal(t, 0, stoppingOnly.occupiedFor(task.FormResident))
	assert.Equal(t, 1, stoppingOnly.occupiedFor(task.FormOneShot))
}

// TestRunsByTaskDrainAnchorReason：排空补停触发锚——任一 stopping 行
// 携带的起因即锚（行事实真源，重启后仍可判）；无 stopping 行或起因
// 全空 = 宽限排空未发起过，补停不触发。
func TestRunsByTaskDrainAnchorReason(t *testing.T) {
	anchored := newRunsByTask([]run.Run{
		{ID: "R1", TaskID: "T1", State: run.StateRunning},
		{ID: "R2", TaskID: "T1", State: run.StateStopping, StopReason: "stopped_by_user"},
	})
	assert.Equal(t, "stopped_by_user", anchored.drainAnchorReason())

	emptyReason := newRunsByTask([]run.Run{
		{ID: "R1", TaskID: "T1", State: run.StateStopping, StopReason: ""},
	})
	assert.Empty(t, emptyReason.drainAnchorReason())

	activeOnly := newRunsByTask([]run.Run{
		{ID: "R1", TaskID: "T1", State: run.StateRunning},
	})
	assert.Empty(t, activeOnly.drainAnchorReason())
}
