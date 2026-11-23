package engine

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// fakeCheckProvider 是探活聚合的行为桩（healthy/block 两态）。
type fakeCheckProvider struct {
	name    string
	cap     capability.Kind
	healthy bool
	details string
	block   bool
}

func (f fakeCheckProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: f.name, Capability: f.cap}
}

func (f fakeCheckProvider) Health(ctx context.Context) capability.HealthReport {
	if f.block {
		<-ctx.Done()
		return capability.HealthReport{Healthy: false, Details: "cancelled"}
	}
	return capability.HealthReport{Healthy: f.healthy, Details: f.details}
}

// newAgg 构造测试聚合器（TTL 极短由测试显式控时；now 可注入）。
func newAgg(checkers []capability.Provider, now time.Time) *componentsHealthAgg {
	return &componentsHealthAgg{checkers: checkers, ttl: ComponentsHealthCacheTTL, checkedAt: now}
}

func TestComponentsHealthSnapshotAggregates(t *testing.T) {
	now := time.Now()
	agg := newAgg([]capability.Provider{
		fakeCheckProvider{name: "healthy-one", cap: capability.KindProxy, healthy: true},
		fakeCheckProvider{name: "sick-one", cap: capability.KindRegistry, healthy: false, details: "dial refused"},
	}, now)
	snap := agg.snapshot(context.Background(), now)
	require.Len(t, snap, 2)
	assert.True(t, snap[0].Healthy)
	assert.False(t, snap[1].Healthy)
	assert.Equal(t, "dial refused", snap[1].Details)
	assert.Equal(t, []string{"sick-one"}, capability.UnhealthyNames(snap))
}

func TestComponentsHealthSnapshotCacheWindow(t *testing.T) {
	now := time.Now()
	agg := newAgg([]capability.Provider{fakeCheckProvider{name: "a", cap: capability.KindProxy, healthy: true}}, now)
	_ = agg.snapshot(context.Background(), now)
	// TTL 窗内：不重探（计数面由组件健康行等价性锚定——窗口内返回同一快照）。
	again := agg.snapshot(context.Background(), now.Add(5*time.Second))
	assert.Equal(t, now, again[0].CheckedAt, "cached snapshot is reused within the TTL window")
	// 窗外：重探（CheckedAt 推进）。
	later := agg.snapshot(context.Background(), now.Add(ComponentsHealthCacheTTL+time.Second))
	assert.Equal(t, now.Add(ComponentsHealthCacheTTL+time.Second), later[0].CheckedAt)
}

func TestComponentsHealthSnapshotTimeoutCountsUnhealthy(t *testing.T) {
	now := time.Now()
	agg := newAgg([]capability.Provider{fakeCheckProvider{name: "slow", cap: capability.KindProxy, block: true}}, now)
	snap := agg.snapshot(context.Background(), now)
	require.Len(t, snap, 1)
	assert.False(t, snap[0].Healthy)
	assert.NotEmpty(t, snap[0].Details)
}

func TestComponentsHealthSnapshotEmptyCheckers(t *testing.T) {
	agg := newAgg(nil, time.Now())
	assert.Nil(t, agg.snapshot(context.Background(), time.Now()), "no checkers yields an empty snapshot")
}
