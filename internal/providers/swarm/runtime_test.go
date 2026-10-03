package swarm

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// A2 回归（N0 修复批）：L1 数据源不得把未就绪任务报成 running——placement
// 落空（约束不可满足）时任务停在 pending 族，必须映射为 pending 让健康门
// 保持关闭（超时失败），而不是假 succeeded。
func TestTaskEventStateTight(t *testing.T) {
	for _, tc := range []struct {
		task swarm.TaskState
		want capability.WorkloadState
	}{
		{swarm.TaskStateRunning, capability.WorkloadRunning},
		{swarm.TaskStateNew, capability.WorkloadPending},
		{swarm.TaskStateAllocated, capability.WorkloadPending},
		{swarm.TaskStateAssigned, capability.WorkloadPending},
		{swarm.TaskStateAccepted, capability.WorkloadPending},
		{swarm.TaskStatePreparing, capability.WorkloadPending},
		{swarm.TaskStatePending, capability.WorkloadPending},
		{swarm.TaskStateReady, capability.WorkloadPending},
		{swarm.TaskStateStarting, capability.WorkloadPending},
		// ADR-0025 决策 2：一次性失败终态不再被 degraded 吞并（failed ≠
		// running，L1 门语义不变——门要求 running 才开）。
		{swarm.TaskStateFailed, capability.WorkloadFailed},
		{swarm.TaskStateRejected, capability.WorkloadFailed},
	} {
		if got := taskEventState(tc.task); got != tc.want {
			t.Errorf("taskEventState(%s) = %s, want %s", tc.task, got, tc.want)
		}
	}
}

// service 事件只代表 spec 变化，不代表载体就绪：create/update 必须 pending
// （就绪权威是任务轮询），remove 计 stopped。
func TestServiceEventStateTight(t *testing.T) {
	for action, want := range map[string]capability.WorkloadState{
		"create": capability.WorkloadPending,
		"update": capability.WorkloadPending,
		"remove": capability.WorkloadStopped,
	} {
		if got := serviceEventState(action); got != want {
			t.Errorf("serviceEventState(%s) = %s, want %s", action, got, want)
		}
	}
}

// 载体名碰撞显式拒绝（N1 收尾批 B9）：进程名仅差特殊字符（web.1 /
// web-1）经 sanitize 折叠成同载体名——旧行为后者静默覆盖前者，一进程
// 无声丢失。Ensure 期碰撞前置检是纯检：先于任何材料/服务副作用，零值
// Provider（nil cli）即可 hermetic 测试。
func TestEnsureRejectsCarrierNameCollision(t *testing.T) {
	p := &Provider{}
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	ws := []capability.Workload{
		{ID: "wl_01A", Process: "web.1", Image: "nginx:1"},
		{ID: "wl_01B", Process: "web-1", Image: "nginx:1"},
	}
	err := p.Ensure(context.Background(), ns, ws, capability.Generation(1), capability.Materials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wl_01A", "error names both colliding workloads")
	assert.Contains(t, err.Error(), "wl_01B")
	assert.Contains(t, err.Error(), `"fleetly-acme-shop-web-web-1"`, "error names the collapsed carrier")
	assert.Contains(t, err.Error(), "carrier name collision")
}
