package swarm

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"

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
