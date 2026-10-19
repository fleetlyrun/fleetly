package swarm

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// restartNS 是重启测试的隔离域锚（app 轴）。
func restartNS() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "acme", Project: "shop", App: "app1"}
}

// seedRestartService 预置一个带平台标记的存量服务（carrierID = labelWorkload
// 值；BG 进程的 ID 是代次化形态 app1-web-g<gen>）。
func seedRestartService(f *fakeDaemon, ns capability.NamespaceRef, carrierID string) string {
	w := capability.Workload{ID: carrierID, Process: "web"}
	labels := workloadLabels(ns, w, 1)
	name := "fleetly-" + sanitizeNamePart(ns.Project) + "-" + sanitizeNamePart(carrierID)
	spec := swarm.ServiceSpec{}
	spec.Name = name // 经嵌入 Annotations 提升，复合字面量不能直写
	spec.Labels = labels
	f.addService(spec)
	return name
}

// R02-M3：动作面重启按代次化载体 ID 命中恰一个服务——其它代/裸 ID 的
// 存量服务零触碰（ForceUpdate 只自增被命中者）。
func TestRestartTargetsGenerationScopedCarrier(t *testing.T) {
	f := newFakeDaemon()
	p := &Provider{cli: f.newClient(t)}
	ns := restartNS()
	oldName := seedRestartService(f, ns, "app1-web-g1")
	newName := seedRestartService(f, ns, "app1-web-g2")
	bareName := seedRestartService(f, ns, "app1-web")
	require.NoError(t, p.Restart(context.Background(), ns, "app1-web-g2"))

	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Equal(t, uint64(1), f.store[newName].Spec.TaskTemplate.ForceUpdate,
		"the generation-scoped service must be force-updated exactly once")
	assert.Zero(t, f.store[oldName].Spec.TaskTemplate.ForceUpdate,
		"the previous generation must not be restarted")
	assert.Zero(t, f.store[bareName].Spec.TaskTemplate.ForceUpdate,
		"the unscoped id must not match a generation-scoped carrier")
}

// 零命中 = 未找到：错误携带 state.ErrNotFound（API 映射 E_NOT_FOUND），
// 不得静默成功（R02-M3 的 k3s 假成功同源语义）。
func TestRestartMissingCarrierIsNotFound(t *testing.T) {
	f := newFakeDaemon()
	p := &Provider{cli: f.newClient(t)}
	ns := restartNS()
	seedRestartService(f, ns, "app1-web-g1")

	err := p.Restart(context.Background(), ns, "app1-web-g9")
	require.Error(t, err)
	assert.True(t, errors.Is(err, capability.ErrRestartTargetNotFound), "a miss must wrap state.ErrNotFound, got %v", err)
}
