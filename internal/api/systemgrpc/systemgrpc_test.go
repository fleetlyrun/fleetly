package systemgrpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 注意：本包测试只链接 systemgrpc → schema（builtin Spec 条目），
// 事件 payload 的全量贡献对账在 internal/assembly（组合根链接全部
// 贡献方后的完备性守卫）。

func testService() *Service { return New(buildinfo.BuildInfo{Version: "0.1.0-test"}, nil) }

// fakeHealthProvider 是健康聚合的最小探针（降级矩阵口径：任一不健康 /
// 超时即 DEGRADED，IA v3 二期③）。
type fakeHealthProvider struct {
	name    string
	healthy bool
	details string
	block   bool
}

func (f fakeHealthProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: f.name, Capability: capability.KindRuntime, Version: "test"}
}
func (f fakeHealthProvider) Health(ctx context.Context) capability.HealthReport {
	if f.block {
		<-ctx.Done()
		return capability.HealthReport{Healthy: false, Details: "cancelled"}
	}
	return capability.HealthReport{Healthy: f.healthy, Details: f.details}
}

func TestGetStatusAggregatesComponentHealth(t *testing.T) {
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, []capability.Provider{
		fakeHealthProvider{name: "healthy-one", healthy: true},
		fakeHealthProvider{name: "sick-one", healthy: false, details: "dial refused"},
	})
	resp, err := svc.GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState())
	require.Len(t, resp.GetComponents(), 2)
	assert.True(t, resp.GetComponents()[0].GetHealthy())
	assert.False(t, resp.GetComponents()[1].GetHealthy())
	assert.Equal(t, "dial refused", resp.GetComponents()[1].GetDetails())
}

func TestGetStatusHealthyWhenAllPass(t *testing.T) {
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, []capability.Provider{
		fakeHealthProvider{name: "a", healthy: true},
		fakeHealthProvider{name: "b", healthy: true},
	})
	resp, err := svc.GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_HEALTHY, resp.GetState())
	assert.Len(t, resp.GetComponents(), 2)
}

func TestGetStatusTimeoutCountsUnhealthy(t *testing.T) {
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, []capability.Provider{
		fakeHealthProvider{name: "slow", block: true},
	})
	resp, err := svc.GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState())
	assert.Contains(t, resp.GetComponents()[0].GetDetails(), "timed out")
}

func TestGetStatusEmptyCheckersStaysHealthy(t *testing.T) {
	resp, err := testService().GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_HEALTHY, resp.GetState())
	assert.Empty(t, resp.GetComponents())
}

func TestGetSchemaListsEntriesSorted(t *testing.T) {
	resp, err := testService().GetSchema(context.Background(), &systemv1.GetSchemaRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetEntries())
	for i := 1; i < len(resp.GetEntries()); i++ {
		assert.Less(t, resp.GetEntries()[i-1].GetName(), resp.GetEntries()[i].GetName())
	}
	// schema_json 是可解析的 canonical JSON 且带方言自报键。
	var tree map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.GetEntries()[0].GetSchemaJson()), &tree))
	assert.Contains(t, tree, "$schema")
}

func TestExplainResolvesSpecKind(t *testing.T) {
	resp, err := testService().Explain(context.Background(), &systemv1.ExplainRequest{Resource: "app"})
	require.NoError(t, err)
	assert.Equal(t, "app", resp.GetEntry().GetName())
	assert.Equal(t, "spec", resp.GetEntry().GetKind())
	assert.NotEmpty(t, resp.GetEntry().GetSummary())
	assert.Contains(t, resp.GetEntry().GetSchemaJson(), `"type":"object"`)
}

func TestExplainRejectsUnknownAndEmpty(t *testing.T) {
	_, err := testService().Explain(context.Background(), &systemv1.ExplainRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")

	_, err = testService().Explain(context.Background(), &systemv1.ExplainRequest{Resource: "definitely.not-a-name"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	assert.Contains(t, err.Error(), "fleetly schema")
}
