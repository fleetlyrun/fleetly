package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestProjectTranslatesTaskGroupRefs（ADR-0025 决策 5）：taskGroup:<name>
// 跨挂在投影层翻译为实际平台网络名——Provider 收到的 Networks 一律是平台
// 网络名（translate.go 的"engine 已翻译"从此是事实而非谎言注释）。
func TestProjectTranslatesTaskGroupRefs(t *testing.T) {
	spec := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: "01JAPP", Project: "shop"},
		Processes: []*specv1.ProcessSpec{{
			Name:        "web",
			ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"},
			Networks:    []string{"default", "taskGroup:dispatcher", "taskgrp-plain"},
		}},
	}
	ws, ns, err := Project(spec, "acme", nil, PeerRefs{})
	require.NoError(t, err)
	assert.Equal(t, capability.NamespaceRef{Team: "acme", Project: "shop", App: "01JAPP"}, ns)
	require.Len(t, ws, 1)
	assert.Equal(t, []string{"default", "taskgrp-dispatcher", "taskgrp-plain"}, ws[0].Networks)
	// ADR-0034：App Process 网络别名 = 进程名（compose 服务名互访语义）。
	require.Len(t, ws[0].Addressing, 1)
	assert.Equal(t, "web", ws[0].Addressing[0].Name)
}

// TestProjectTaskRunWorkload（ADR-0025 决策 2/4/6）：Run Workload 投影——
// Task 轴 ns、never 生命周期、双级 DNS 铸名、网络组挂靠、排空缩零。
func TestProjectTaskRunWorkload(t *testing.T) {
	ts := &specv1.TaskSpec{
		SchemaVersion: 1,
		Task:          &specv1.TaskRef{Id: "01JTASK", Project: "shop"},
		Process: &specv1.ProcessSpec{
			Name:        "run",
			ImageOrigin: &specv1.ProcessSpec_Image{Image: "ghcr.io/acme/worker:1"},
			Command:     []string{"/worker"},
			Env:         map[string]string{"POOL": "browser"},
			Resources:   &specv1.ResourcesSpec{CpuMillis: 500, MemoryMb: 512},
		},
		NetworkGroup: "dispatcher",
	}
	runID := "01JRUN0000000000000000000"

	w, ns, err := ProjectTask(ts, "acme", runID, true)
	require.NoError(t, err)
	assert.Equal(t, capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01JTASK"}, ns)
	assert.Equal(t, runID, w.ID)
	assert.Equal(t, capability.RestartNever, w.Restart)
	assert.Equal(t, int64(1), w.Replicas)
	assert.Equal(t, []string{"taskgrp-dispatcher"}, w.Networks)
	require.Len(t, w.Addressing, 2)
	assert.Equal(t, "task-01jtask", w.Addressing[0].Name)
	assert.Equal(t, "run-01jrun0000000000000000000", w.Addressing[1].Name)
	assert.Equal(t, map[string]string{"POOL": "browser"}, w.Env)
	require.NotNil(t, w.Resources)
	assert.Equal(t, int64(500), w.Resources.CPUMillis)

	// 排空缩零：stopping Run 投影为 replicas 0（承载 SIGTERM+StopGrace 路径）。
	wDrain, _, err := ProjectTask(ts, "acme", runID, false)
	require.NoError(t, err)
	assert.Equal(t, int64(0), wDrain.Replicas)
}

// TestProjectTaskRejectsMissingImage：Task 的构建源面不存在——无镜像即
// 精确失败（from_build 是 App 专属面）。
func TestProjectTaskRejectsMissingImage(t *testing.T) {
	ts := &specv1.TaskSpec{
		SchemaVersion: 1,
		Task:          &specv1.TaskRef{Id: "01JTASK", Project: "shop"},
		Process: &specv1.ProcessSpec{
			Name:        "run",
			ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "x"},
		},
	}
	_, _, err := ProjectTask(ts, "acme", "01JRUN", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no image origin")
}

// TestDNSMintFormulas（ADR-0025 决策 6/R-3）：铸名公式住 engine，跨 Runtime
// 不变——per-Task 池级轮询名 + per-Run 稳定名。
func TestDNSMintFormulas(t *testing.T) {
	assert.Equal(t, "task-01jtask", TaskDNSName("01JTASK"))
	assert.Equal(t, "run-01jrun", RunDNSName("01JRUN"))
	assert.Equal(t, "taskgrp-dispatcher", TaskGroupNetworkName("dispatcher"))
	assert.Equal(t, "taskgrp-abc123", TaskGroupNetworkName("ABC123"), "组名归一小写")
}
