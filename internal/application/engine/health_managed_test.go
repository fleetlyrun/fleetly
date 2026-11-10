package engine

// 受管 Provider 载体存活行在引擎健康组件面的接线（HealthComponents 是
// GetStatus components 的数据源）：观测缓存单源，未观测不出行，失联/劣化/
// 退出态即不健康——受管 Proxy 宕机不再 "status 全绿"（R07-M5 的核侧面）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// managedRow 返回指定名的健康行（nil = 缺席）。
func managedRow(rows []HealthComponent, name string) *HealthComponent {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// 受管 Provider 健康行：未观测不出行；running 健康；stopped 不健康且
// Details 带 Workload ID/状态/消息。
func TestHealthComponentsReportManagedProviderWorkloads(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{
		DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)),
		Runtime: rt, Proxy: &fakeProxy{}, Logger: discardLogger(),
	}, Options{})
	t.Cleanup(func() { _ = e.Stop(context.Background()) })

	assert.Nil(t, managedRow(e.HealthComponents(), "managed proxy"),
		"an unobserved managed workload must not produce a health row (unknown is not unhealthy)")

	e.obs.observe(capability.WorkloadEvent{WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadRunning})
	row := managedRow(e.HealthComponents(), "managed proxy")
	require.NotNil(t, row)
	assert.True(t, row.Healthy)
	assert.Contains(t, row.Details, "workload=fleetly-proxy-fake")

	e.obs.observe(capability.WorkloadEvent{
		WorkloadID: "fleetly-proxy-fake", State: capability.WorkloadStopped,
		Message: "task exited with code 1",
	})
	row = managedRow(e.HealthComponents(), "managed proxy")
	require.NotNil(t, row)
	assert.False(t, row.Healthy, "a dead managed proxy workload must surface as an unhealthy component")
	assert.Contains(t, row.Details, "state=stopped")
	assert.Contains(t, row.Details, "task exited with code 1")

	// 引擎自身行不受影响（不重复/不吞并）。
	assert.NotNil(t, managedRow(e.HealthComponents(), ConvergenceHealthComponent))
}

// 无 Proxy 装配（apitest/最小引擎）不产生受管行——缺席不是不健康。
func TestHealthComponentsWithoutManagedProviders(t *testing.T) {
	e, _, _ := newTestEngine(t)
	for _, row := range e.HealthComponents() {
		assert.NotContains(t, row.Name, "managed ", "a minimal engine declares no managed provider rows")
	}
}
