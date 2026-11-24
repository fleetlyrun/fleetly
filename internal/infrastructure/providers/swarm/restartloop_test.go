package swarm

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 缺陷②回归群（2026-11-24）：swarm 上 App 域崩溃循环必须产生终态事实并以
// k3s 同形（Degraded + RestartCount）上报——修复前被重启策略替换的失败
// task 被当历史任务滤出观测，container_exited 恒为 0、L2 看门狗无 degraded
// 可咬，部署只能等 L1 超时。

func replacedFailure(id string, gen uint64, at time.Time, code int) replacedExit {
	labels := map[string]string{labelManaged: "true", labelWorkload: "wl_app", labelGeneration: "1"}
	if gen != 1 {
		labels[labelGeneration] = "2"
	}
	task := swarm.Task{
		ID: id, ServiceID: "srv-m", DesiredState: swarm.TaskStateShutdown,
		Status: swarm.TaskStatus{
			Timestamp: at, State: swarm.TaskStateFailed, Err: "task: non-zero exit (1)",
			ContainerStatus: &swarm.ContainerStatus{ExitCode: code},
		},
	}
	ev, at2, ok := replacedExitEvent(task, labels)
	if !ok {
		panic("fixture must be a replaced failure")
	}
	return replacedExit{ev: ev, at: at2}
}

// TestOverlayRestartLoops：阈值、时序取最近、活槽位健康不盖写、代次圈定。
func TestOverlayRestartLoops(t *testing.T) {
	base := time.Now()
	pending := capability.WorkloadEvent{WorkloadID: "wl_app", Generation: 1, State: capability.WorkloadPending, Instance: "live"}

	t.Run("单次失败：事实入账但不按循环改写状态（首次失败不咬 L2）", func(t *testing.T) {
		f1 := replacedFailure("t1", 1, base, 1)
		out := overlayRestartLoops([]capability.WorkloadEvent{pending}, []replacedExit{f1})
		require.Len(t, out, 1)
		assert.Equal(t, capability.WorkloadPending, out[0].State, "a single failure keeps the live slot state (the L2 watchdog must not bite on it)")
		assert.EqualValues(t, 1, out[0].RestartCount)
		require.NotNil(t, out[0].ExitCode, "every failure is a terminal fact — the first one included")
		assert.Equal(t, 1, *out[0].ExitCode)
		assert.Equal(t, "t1", out[0].Instance)
	})

	t.Run("两次失败即循环：Degraded + RestartCount + 最近一次失败（输入乱序）", func(t *testing.T) {
		f1 := replacedFailure("t1", 1, base, 1)
		f2 := replacedFailure("t2", 1, base.Add(5*time.Second), 137)
		// 收集序故意把更早的失败放在后面：取「最近」按时刻，不按输入序。
		out := overlayRestartLoops([]capability.WorkloadEvent{pending}, []replacedExit{f2, f1})
		require.Len(t, out, 1)
		assert.Equal(t, capability.WorkloadDegraded, out[0].State)
		assert.EqualValues(t, 2, out[0].RestartCount)
		require.NotNil(t, out[0].ExitCode)
		assert.Equal(t, 137, *out[0].ExitCode, "the latest failure's exit code wins")
		assert.Equal(t, "t2", out[0].Instance, "the dedupe signature's instance segment follows the latest failed task")
		assert.Equal(t, f2.ev.Message, out[0].Message)
	})

	t.Run("活槽位 running：历史失败不盖写在服事实", func(t *testing.T) {
		f1 := replacedFailure("t1", 1, base, 1)
		f2 := replacedFailure("t2", 1, base.Add(time.Second), 1)
		running := pending
		running.State = capability.WorkloadRunning
		out := overlayRestartLoops([]capability.WorkloadEvent{running}, []replacedExit{f1, f2})
		require.Len(t, out, 1)
		assert.Equal(t, capability.WorkloadRunning, out[0].State)
		assert.Nil(t, out[0].ExitCode)
	})

	t.Run("旧代的失败不计入本代（滚动替换留下的不是本代的崩溃循环）", func(t *testing.T) {
		f1 := replacedFailure("t1", 2, base, 1)
		f2 := replacedFailure("t2", 2, base.Add(time.Second), 1)
		out := overlayRestartLoops([]capability.WorkloadEvent{pending}, []replacedExit{f1, f2})
		require.Len(t, out, 1)
		assert.Equal(t, capability.WorkloadPending, out[0].State)
		assert.Nil(t, out[0].ExitCode, "old-generation failures must not be attached to the current generation")
	})

	t.Run("无被替换失败：原样返回", func(t *testing.T) {
		out := overlayRestartLoops([]capability.WorkloadEvent{pending}, nil)
		require.Len(t, out, 1)
		assert.Equal(t, pending, out[0])
	})
}

// TestReplacedExitEventScope：只收 App 域的异常终态被替换 task。
func TestReplacedExitEventScope(t *testing.T) {
	labels := map[string]string{labelManaged: "true", labelWorkload: "wl_app", labelGeneration: "1"}
	base := time.Now()
	_, _, ok := replacedExitEvent(swarm.Task{ID: "live", DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStateFailed}}, labels)
	assert.False(t, ok, "a live-slot task belongs to taskEvent")

	_, _, ok = replacedExitEvent(swarm.Task{ID: "done", DesiredState: swarm.TaskStateShutdown, Status: swarm.TaskStatus{State: swarm.TaskStateComplete}}, labels)
	assert.False(t, ok, "exit 0 completion is not a failure fact")

	taskDomain := map[string]string{labelManaged: "true", labelWorkload: "run_1", labelGeneration: "1", labelTask: "run_1"}
	_, _, ok = replacedExitEvent(swarm.Task{ID: "r1", DesiredState: swarm.TaskStateShutdown, Status: swarm.TaskStatus{State: swarm.TaskStateFailed}}, taskDomain)
	assert.False(t, ok, "Task domain is served per-task by taskEvent")

	_, _, ok = replacedExitEvent(swarm.Task{ID: "u1", DesiredState: swarm.TaskStateShutdown, Status: swarm.TaskStatus{State: swarm.TaskStateFailed}}, map[string]string{})
	assert.False(t, ok, "unmanaged carriers are invisible")

	// rejected：未建容器的终态 ContainerStatus 为 nil——不得击穿，退出码保持缺失。
	ev, _, ok := replacedExitEvent(swarm.Task{ID: "rj", DesiredState: swarm.TaskStateShutdown, Status: swarm.TaskStatus{State: swarm.TaskStateRejected, Timestamp: base}}, labels)
	require.True(t, ok)
	assert.Nil(t, ev.ExitCode)
	assert.Equal(t, capability.WorkloadFailed, ev.State)
}

// TestPollTasksReportsCrashLoop：pollTasks 全链——活槽位 pending + 两个被替换
// 的失败 task → 单条 Degraded 观测（RestartCount=2、最近一次失败的退出码与
// 实例）；第三次失败后 RestartCount=3 且实例换成新 task（去抖签名随之变化，
// 逐次失败各入账一次）。
func TestPollTasksReportsCrashLoop(t *testing.T) {
	d := newFakeDaemon()
	svc := swarm.Service{ID: "srv-m", Spec: swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: "fleetly-p-app-web", Labels: map[string]string{
			labelManaged:    "true",
			labelWorkload:   "wl_app",
			labelGeneration: "1",
		}},
	}}
	d.store[svc.Spec.Name] = svc
	base := time.Now()
	live := swarm.Task{ID: "live", ServiceID: "srv-m", DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStatePreparing, Timestamp: base.Add(6 * time.Second)}}
	failed := func(id string, at time.Time, code int) swarm.Task {
		return swarm.Task{ID: id, ServiceID: "srv-m", DesiredState: swarm.TaskStateShutdown,
			Status: swarm.TaskStatus{Timestamp: at, State: swarm.TaskStateFailed, Err: "task: non-zero exit (1)",
				ContainerStatus: &swarm.ContainerStatus{ExitCode: code}}}
	}
	d.tasks["srv-m"] = []swarm.Task{failed("t1", base, 1), failed("t2", base.Add(5*time.Second), 1), live}

	p := &Provider{cli: d.newClient(t)}
	poll := func() capability.WorkloadEvent {
		out := make(chan capability.WorkloadEvent, 8)
		require.NoError(t, p.pollTasks(context.Background(), out))
		close(out)
		var evs []capability.WorkloadEvent
		for ev := range out {
			evs = append(evs, ev)
		}
		require.Len(t, evs, 1, "one aggregated observation per workload per poll")
		return evs[0]
	}

	// 首次失败：事实入账（非零退出码即终态事实），状态仍是活槽位的 pending。
	d.tasks["srv-m"] = []swarm.Task{failed("t1", base, 1), live}
	first := poll()
	assert.Equal(t, capability.WorkloadPending, first.State)
	assert.EqualValues(t, 1, first.RestartCount)
	require.NotNil(t, first.ExitCode)
	assert.Equal(t, "t1", first.Instance)

	d.tasks["srv-m"] = []swarm.Task{failed("t1", base, 1), failed("t2", base.Add(5*time.Second), 1), live}
	ev := poll()
	assert.Equal(t, "wl_app", ev.WorkloadID)
	assert.Equal(t, capability.WorkloadDegraded, ev.State, "a slot cycling through failures must surface as degraded (the L2 watchdog bites on degraded + restarts)")
	assert.EqualValues(t, 2, ev.RestartCount)
	require.NotNil(t, ev.ExitCode)
	assert.Equal(t, 1, *ev.ExitCode)
	assert.Equal(t, "t2", ev.Instance)

	// 重复轮询：同一观测（去抖签名不变——引擎侧不重复入账）。
	assert.Equal(t, ev, poll())

	// 第三次失败：计数与实例都变。
	d.tasks["srv-m"] = append(d.tasks["srv-m"], failed("t3", base.Add(9*time.Second), 137))
	ev3 := poll()
	assert.EqualValues(t, 3, ev3.RestartCount)
	assert.Equal(t, "t3", ev3.Instance)
	assert.Equal(t, 137, *ev3.ExitCode)

	// 活槽位恢复 running：不盖写（脱离失败态——引擎侧走 crash-loop 退出沿）。
	d.tasks["srv-m"] = []swarm.Task{failed("t1", base, 1), failed("t2", base.Add(5*time.Second), 1), failed("t3", base.Add(9*time.Second), 137),
		{ID: "ok", ServiceID: "srv-m", DesiredState: swarm.TaskStateRunning, Status: swarm.TaskStatus{State: swarm.TaskStateRunning, Timestamp: base.Add(20 * time.Second)}}}
	ok := poll()
	assert.Equal(t, capability.WorkloadRunning, ok.State)
	assert.Nil(t, ok.ExitCode)
}
