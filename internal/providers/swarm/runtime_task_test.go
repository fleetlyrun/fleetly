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

// TestTaskEventGenAttributionPrefersTaskLabels（B14-1）：任务观测的
// Generation 归属取任务级 ContainerSpec.Labels（创建时冻结的 spec）——
// 滚动窗口内旧任务不被服务级 labels 的当前值误归因到新 gen。
func TestTaskEventGenAttributionPrefersTaskLabels(t *testing.T) {
	genLabels := func(gen string) map[string]string {
		return map[string]string{
			labelManaged:    "true",
			labelGeneration: gen,
			labelWorkload:   "01JD0WL000000000000000000",
		}
	}
	taskWithLabels := func(labels map[string]string) swarm.Task {
		return swarm.Task{
			ID:           "task-1",
			DesiredState: swarm.TaskStateRunning,
			Status:       swarm.TaskStatus{State: swarm.TaskStateRunning},
			Spec:         swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Labels: labels}},
		}
	}
	serviceLabels := genLabels("2") // 服务级 labels 恒是当前 gen
	// 归属判定走 pollTasks 的同一取值序：taskLabels（任务级优先，服务级兜底）。
	attributed := func(task swarm.Task) capability.WorkloadEvent {
		ev, ok := taskEvent(task, taskLabels(task, serviceLabels))
		require.True(t, ok)
		return ev
	}

	// 旧任务（gen 1）在滚动窗口内被轮询到：归属必须锚定创建时的 gen，
	// 不被服务级 gen=2 吞并。
	ev := attributed(taskWithLabels(genLabels("1")))
	assert.Equal(t, capability.Generation(1), ev.Generation,
		"task-level labels must win: an old task stays attributed to its own generation")

	// 任务级缺失 → 回退服务级（修复前的既有取法，兜底不回退）。
	bare := taskWithLabels(nil)
	bare.Spec = swarm.TaskSpec{} // 无 ContainerSpec（nil 指针不 panic）
	ev = attributed(bare)
	assert.Equal(t, capability.Generation(2), ev.Generation,
		"missing task-level labels must fall back to service labels")
}

// TestTaskLabelsPreference 钉 taskLabels 取值序：任务级非空优先、nil
// ContainerSpec 安全、缺失回退服务级。
func TestTaskLabelsPreference(t *testing.T) {
	taskLevel := map[string]string{labelGeneration: "1"}
	serviceLevel := map[string]string{labelGeneration: "2"}

	got := taskLabels(swarm.Task{Spec: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Labels: taskLevel}}}, serviceLevel)
	assert.Equal(t, taskLevel, got, "task-level labels win when present")

	got = taskLabels(swarm.Task{Spec: swarm.TaskSpec{}}, serviceLevel)
	assert.Equal(t, serviceLevel, got, "nil ContainerSpec falls back to service labels")

	got = taskLabels(swarm.Task{Spec: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{}}}, serviceLevel)
	assert.Equal(t, serviceLevel, got, "empty task-level labels fall back to service labels")
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
