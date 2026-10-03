package swarm

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestTaskEventStateTerminalMapping（ADR-0025 决策 2）：一次性终态不再被
// degraded/pending 吞并——complete=completed、failed/rejected=failed、
// shutdown=stopped。
func TestTaskEventStateTerminalMapping(t *testing.T) {
	cases := map[swarm.TaskState]capability.WorkloadState{
		swarm.TaskStateRunning:  capability.WorkloadRunning,
		swarm.TaskStateComplete: capability.WorkloadCompleted,
		swarm.TaskStateFailed:   capability.WorkloadFailed,
		swarm.TaskStateRejected: capability.WorkloadFailed,
		swarm.TaskStateShutdown: capability.WorkloadStopped,
		swarm.TaskStateNew:      capability.WorkloadPending,
		swarm.TaskStateStarting: capability.WorkloadPending,
	}
	for state, want := range cases {
		assert.Equal(t, want, taskEventState(state), "swarm task state %s", state)
	}
}

// TestTerminalTaskState：终态判定集（pollTasks 的 Task 域豁免面）。
func TestTerminalTaskState(t *testing.T) {
	for _, s := range []swarm.TaskState{
		swarm.TaskStateComplete, swarm.TaskStateFailed,
		swarm.TaskStateShutdown, swarm.TaskStateRejected,
	} {
		assert.True(t, terminalTaskState(s), "swarm task state %s", s)
	}
	for _, s := range []swarm.TaskState{
		swarm.TaskStateNew, swarm.TaskStateRunning, swarm.TaskStateStarting,
		swarm.TaskStatePreparing, swarm.TaskStatePending,
	} {
		assert.False(t, terminalTaskState(s), "swarm task state %s", s)
	}
}

// 终态任务无 ContainerStatus 的回归面（安全批 P0）：rejected/shutdown 等
// 未建容器的终态，moby 类型 TaskStatus.ContainerStatus 是 nil 指针——
// 直接解引用曾击穿 Watch goroutine（fleetlyd 崩溃循环）。修复后必须不
// panic 且照常产出终态事件（rejected/failed=failed、shutdown=stopped 的
// 既有语义不动），ExitCode 保持 nil（下游显示 unknown 的既有语义）。
func TestTaskEventTerminalWithoutContainerStatus(t *testing.T) {
	managedTaskLabels := map[string]string{
		labelManaged:    "true",
		labelTask:       "01JD0TASK0000000000000000",
		labelWorkload:   "01JD0RUN0000000000000000",
		labelGeneration: "3",
		labelNodeID:     "01JD0NODE00000000000000000",
	}
	for _, tc := range []struct {
		state swarm.TaskState
		want  capability.WorkloadState
	}{
		{swarm.TaskStateRejected, capability.WorkloadFailed},
		{swarm.TaskStateFailed, capability.WorkloadFailed},
		{swarm.TaskStateShutdown, capability.WorkloadStopped},
	} {
		task := swarm.Task{
			ID:           "task-1",
			DesiredState: swarm.TaskStateShutdown, // 排空/替换后的终态形态
			Status: swarm.TaskStatus{
				State:           tc.state,
				Err:             "no suitable node",
				ContainerStatus: nil, // 未建容器（本回归面的核心形态）
			},
		}
		ev, ok := taskEvent(task, managedTaskLabels)
		require.True(t, ok, "a terminal task-domain task must stay visible (state %s)", tc.state)
		assert.Equal(t, tc.want, ev.State, "state %s: terminal mapping must keep existing semantics", tc.state)
		assert.Nil(t, ev.ExitCode, "state %s: no container means no exit code (unknown semantics)", tc.state)
		assert.Equal(t, "01JD0RUN0000000000000000", ev.WorkloadID)
		assert.Equal(t, "no suitable node", ev.Reason)
	}

	// 对照组：带 ContainerStatus 的终态仍携带退出码（complete=0 形态）。
	withContainer := swarm.Task{
		ID:           "task-2",
		DesiredState: swarm.TaskStateRunning,
		Status: swarm.TaskStatus{
			State:           swarm.TaskStateComplete,
			ContainerStatus: &swarm.ContainerStatus{ExitCode: 0},
		},
	}
	ev, ok := taskEvent(withContainer, managedTaskLabels)
	require.True(t, ok)
	require.NotNil(t, ev.ExitCode)
	assert.Equal(t, 0, *ev.ExitCode)
}
