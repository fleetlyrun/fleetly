package swarm

import (
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestTaskDomainLifecycleMapping（ADR-0025 决策 1/4/6/7）：Run Workload 的
// 翻译面——never 重启、StopGrace、双级 DNS 别名、Task 域命名与标记。
func TestTaskDomainLifecycleMapping(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01JTASK0000000000000000"}
	grace := 30 * time.Second
	w := capability.Workload{
		ID:        "01JRUN0000000000000000000",
		Process:   "run",
		Image:     "ghcr.io/acme/worker:1",
		Replicas:  1,
		Restart:   capability.RestartNever,
		StopGrace: grace,
		Networks:  []string{"taskgrp-dispatcher"},
		Addressing: []capability.Address{
			{Name: "task-01JTASK0000000000000000"},
			{Name: "run-01JRUN0000000000000000000"},
		},
	}
	spec := toServiceSpec(ns, w, capability.Generation(1), nil)

	// Task 域命名：fleetly-run-<run id>。
	assert.Equal(t, "fleetly-run-01jrun0000000000000000000", spec.Name)
	// 生命周期：never → swarm none（one-shot 退出即终态，决策 1）。
	require.NotNil(t, spec.TaskTemplate.RestartPolicy)
	assert.Equal(t, swarm.RestartPolicyConditionNone, spec.TaskTemplate.RestartPolicy.Condition)
	// StopGrace → 容器停止宽限。
	require.NotNil(t, spec.TaskTemplate.ContainerSpec.StopGracePeriod)
	assert.Equal(t, grace, *spec.TaskTemplate.ContainerSpec.StopGracePeriod)
	// 网络组挂靠 + 双级 DNS 名映射为网络别名（排序稳定）。
	require.Len(t, spec.TaskTemplate.Networks, 1)
	assert.Equal(t, "fleetly-net-shop-taskgrp-dispatcher", spec.TaskTemplate.Networks[0].Target)
	assert.Equal(t, []string{"run-01jrun0000000000000000000", "task-01jtask0000000000000000"},
		spec.TaskTemplate.Networks[0].Aliases)
	// 标记集：Task 轴、无空 labelApp。
	labels := workloadLabels(ns, w, capability.Generation(1))
	assert.Equal(t, "01jtask0000000000000000", labels[labelTask])
	assert.NotContains(t, labels, labelApp)
	for k, v := range nsSelector(ns) {
		assert.Equal(t, v, labels[k], "selector key %s", k)
	}
}

// TestDefaultRestartPolicyIsAny：长运行缺省/always → swarm any（不变式）。
func TestDefaultRestartPolicyIsAny(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	for _, restart := range []capability.RestartPolicy{capability.RestartDefault, "", capability.RestartAlways} {
		w := capability.Workload{ID: "wl_01H", Process: "web", Image: "nginx:1", Restart: restart}
		spec := toServiceSpec(ns, w, capability.Generation(1), nil)
		require.NotNil(t, spec.TaskTemplate.RestartPolicy)
		assert.Equal(t, swarm.RestartPolicyConditionAny, spec.TaskTemplate.RestartPolicy.Condition,
			"restart %q must map to any", restart)
	}
}
