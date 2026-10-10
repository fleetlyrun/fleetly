package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// restartRecorder 是 RuntimeRestart 子面的记录假件（含内嵌全量 fake）。
type restartRecorder struct {
	*fakeRuntime
	got []string
}

func (r *restartRecorder) Restart(_ context.Context, _ capability.NamespaceRef, workloadID string) error {
	r.got = append(r.got, workloadID)
	return nil
}

// managedFake 是受管自宿声明假件（Describe.Managed 契约 + Managed 面）；
// 内嵌 fakeMetrics 补齐 Metrics 端口形状，实名由 Describe 覆写。
type managedFake struct {
	*fakeMetrics
	name      string
	namespace capability.NamespaceRef
	workloads []capability.Workload
}

func (m *managedFake) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: m.name, Capability: capability.KindMetrics, Version: "test"}
}
func (m *managedFake) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (m *managedFake) ManagedNamespace() capability.NamespaceRef { return m.namespace }
func (m *managedFake) ManagedWorkloads() []capability.Workload   { return m.workloads }

func TestRestartComponentRoutesByProviderName(t *testing.T) {
	base := newFakeRuntime()
	rt := &restartRecorder{fakeRuntime: base}
	db, _ := statetest.New(t)
	e := New(Deps{
		DB:      db,
		Runtime: rt,
		Metrics: &managedFake{fakeMetrics: &fakeMetrics{}, name: "victoriametrics",
			namespace: capability.NamespaceRef{Team: "fleetly", Project: "system", App: "metrics"},
			workloads: []capability.Workload{{ID: "vm-carrier"}, {ID: "vm-second"}},
		},
		Logger: discardLogger(),
	}, Options{})
	restarted, err := e.RestartComponent(context.Background(), "victoriametrics")
	require.NoError(t, err)
	assert.Equal(t, 2, restarted)
	assert.Equal(t, []string{"vm-carrier", "vm-second"}, rt.got)
}

func TestRestartComponentUnknownAndUnsupported(t *testing.T) {
	db, _ := statetest.New(t)
	e := New(Deps{
		DB:      db,
		Runtime: newFakeRuntime(),
		// 在册但不声明受管部署形态（无载体可重启）。
		Metrics: &unmanagedNamedFake{fakeMetrics: &fakeMetrics{}, name: "victoriametrics"},
		Logger:  discardLogger(),
	}, Options{})

	_, err := e.RestartComponent(context.Background(), "victoriametrics")
	assert.True(t, errors.Is(err, ErrRestartUnsupported), err)

	_, err = e.RestartComponent(context.Background(), "who-am-i")
	assert.True(t, errors.Is(err, ErrComponentUnknown), err)

	_, err = e.RestartComponent(context.Background(), "")
	assert.True(t, errors.Is(err, ErrComponentUnknown), err)
}

// unmanagedNamedFake 是在册但非受管自宿的 Provider 假件（实名可覆写——
// 路由命中的前提）。
type unmanagedNamedFake struct {
	*fakeMetrics
	name string
}

func (u *unmanagedNamedFake) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: u.name, Capability: capability.KindMetrics, Version: "test"}
}

func TestRestartComponentRequiresRuntimeSubface(t *testing.T) {
	// Runtime 不实现 RuntimeRestart（第三 runtime 缺席形态）→ 精确拒绝。
	db, _ := statetest.New(t)
	e := New(Deps{
		DB:      db,
		Runtime: newFakeRuntime(),
		Metrics: &managedFake{fakeMetrics: &fakeMetrics{}, name: "victoriametrics", workloads: []capability.Workload{{ID: "vm"}}},
		Logger:  discardLogger(),
	}, Options{})
	_, err := e.RestartComponent(context.Background(), "victoriametrics")
	assert.True(t, errors.Is(err, ErrRuntimeNoRestart), err)
}
