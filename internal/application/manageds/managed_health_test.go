package manageds

// 受管 Provider 健康行白盒（Health → engine.HealthComponents →
// GetStatus components）：观测缓存单源——running 健康、失联/劣化/退出态
// 不健康、未观测/启动窗不出行（未知不等于不健康）；行名与 Provider 顺序
// 稳定（managed proxy → managed registry → ...），多 Workload Provider 只
// 出一行。

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// observationBox 是观测窄接口（managed 轴）的捕获盒：Health 的载体存活
// 读面（核 observationOf 的等价窄面，通则⑥）。
type observationBox struct {
	mu sync.Mutex
	ev map[string]capability.WorkloadEvent
}

func newObservationBox() *observationBox {
	return &observationBox{ev: map[string]capability.WorkloadEvent{}}
}

// observe 落一条观测（测试驱动；last-write-wins 与核缓存同语义）。
func (o *observationBox) observe(ev capability.WorkloadEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ev[ev.WorkloadID] = ev
}

func (o *observationBox) observationOf(id string) (capability.WorkloadEvent, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ev, ok := o.ev[id]
	return ev, ok
}

// 未观测（watch 首事件前）不出行；running 健康；stopped 不健康且 Details
// 带 Workload ID / 状态 / 消息。
func TestManagedHealthFollowsObservation(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)

	assert.Empty(t, fx.m.Health(), "an unobserved managed provider must not report a row (unknown is not unhealthy)")

	fx.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadRunning})
	rows := fx.m.Health()
	require.Len(t, rows, 1)
	assert.Equal(t, "managed proxy", rows[0].Name)
	assert.True(t, rows[0].Healthy)
	assert.Contains(t, rows[0].Details, "workload=fleetly-proxy-fake")
	assert.Contains(t, rows[0].Details, "state=running")

	fx.obs.observe(capability.WorkloadEvent{
		WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadStopped,
		Message: "task exited with code 1",
	})
	rows = fx.m.Health()
	require.Len(t, rows, 1)
	assert.Equal(t, "managed proxy", rows[0].Name)
	assert.False(t, rows[0].Healthy)
	assert.Contains(t, rows[0].Details, "workload=fleetly-proxy-fake")
	assert.Contains(t, rows[0].Details, "state=stopped")
	assert.Contains(t, rows[0].Details, "task exited with code 1")
}

// 失联/劣化/退出态都按不健康计（受管面是长运行形态：completed 也不在
// 服务）；pending 是启动窗中间态，不制造假降级。
func TestManagedHealthStateSemantics(t *testing.T) {
	for _, state := range []capability.WorkloadState{
		capability.WorkloadStopped,
		capability.WorkloadDegraded,
		capability.WorkloadFailed,
		capability.WorkloadOrphaned,
		capability.WorkloadCompleted,
	} {
		t.Run(string(state)+" is unhealthy", func(t *testing.T) {
			fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
			fx.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-fake", State: state})
			rows := fx.m.Health()
			require.Len(t, rows, 1)
			assert.False(t, rows[0].Healthy)
			assert.Contains(t, rows[0].Details, "state="+string(state))
		})
	}

	t.Run("pending is not unhealthy", func(t *testing.T) {
		fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
		fx.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadPending})
		assert.Empty(t, fx.m.Health(), "the startup window must not be reported as a degradation")
	})
}

// 行序与 ManagedProviders 同序（Proxy → Registry），每个 Provider 至多
// 一行；多 Workload Provider 汇总全部失联项。
func TestManagedHealthStableRowsPerProvider(t *testing.T) {
	reg := newFakeRegistry()
	reg.managed = true
	fx := newFixture(t, nil, &fakeProxy{}, reg, nil, 30*time.Second)

	fx.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadRunning})
	fx.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-registry-fake", State: capability.WorkloadDegraded, Reason: "task rejected"})

	rows := fx.m.Health()
	require.Len(t, rows, 2)
	assert.Equal(t, "managed proxy", rows[0].Name)
	assert.True(t, rows[0].Healthy)
	assert.Equal(t, "managed registry", rows[1].Name)
	assert.False(t, rows[1].Healthy)
	assert.Contains(t, rows[1].Details, "state=degraded")
	assert.Contains(t, rows[1].Details, "task rejected", "Reason is the fallback when Message is empty")

	// 多 Workload Provider：失联项逐条汇总，仍只一行。
	proxy := &fakeProxy{ws: []capability.Workload{
		{ID: "fleetly-proxy-a", Process: "proxy", Image: "fake/proxy:1", Replicas: 1},
		{ID: "fleetly-proxy-b", Process: "proxy", Image: "fake/proxy:1", Replicas: 1},
	}}
	fx2 := newFixture(t, nil, proxy, nil, nil, 30*time.Second)
	fx2.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-a", State: capability.WorkloadRunning})
	fx2.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-b", State: capability.WorkloadFailed})
	rows = fx2.m.Health()
	require.Len(t, rows, 1, "one row per managed provider, not one per workload")
	assert.False(t, rows[0].Healthy)
	assert.NotContains(t, rows[0].Details, "fleetly-proxy-a", "only the unhealthy workloads are listed")
	assert.Contains(t, rows[0].Details, "fleetly-proxy-b")
}

// 无观测端口（手工装配）不出行，不 panic。
func TestManagedHealthWithoutObservationPort(t *testing.T) {
	fx := newFixture(t, nil, &fakeProxy{}, nil, nil, 30*time.Second)
	fx.m.observationOf = nil
	assert.Empty(t, fx.m.Health())
}
