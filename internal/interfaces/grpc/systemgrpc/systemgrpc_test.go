package systemgrpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lynxland/lynx/buildinfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// 注意：本包测试只链接 systemgrpc → schema（builtin Spec 条目），
// 事件 payload 的全量贡献对账在 internal/assembly（组合根链接全部
// 贡献方后的完备性守卫）。

func testService() *Service { return New(buildinfo.BuildInfo{Version: "0.1.0-test"}, nil) }

// fakeSystemEngine 是 Engine 窄接口的快照桩（B2b：探活聚合搬 engine——
// systemgrpc 消费快照导出，本桩替代旧 checkers 形）。
type fakeSystemEngine struct {
	Engine
	snapshot []capability.ComponentHealth
}

func (f *fakeSystemEngine) ComponentsHealthSnapshot(context.Context) []capability.ComponentHealth {
	return f.snapshot
}

func (f *fakeSystemEngine) HealthComponents() []api.HealthComponent { return nil }

// snapshotOf 是 capability.ComponentHealth 集的便捷构造（name/healthy/details
// 三元组——测试可读性）。
func snapshotOf(entries ...[3]any) []capability.ComponentHealth {
	out := make([]capability.ComponentHealth, 0, len(entries))
	for _, e := range entries {
		name, _ := e[0].(string)
		healthy, _ := e[1].(bool)
		details, _ := e[2].(string)
		out = append(out, capability.ComponentHealth{Name: name, Healthy: healthy, Details: details})
	}
	return out
}

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
	eng := &fakeSystemEngine{snapshot: snapshotOf(
		[3]any{"healthy-one", true, ""},
		[3]any{"sick-one", false, "dial refused"},
	)}
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, eng)
	resp, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState())
	require.Len(t, resp.GetComponents(), 2)
	assert.True(t, resp.GetComponents()[0].GetHealthy())
	assert.False(t, resp.GetComponents()[1].GetHealthy())
	assert.Equal(t, "dial refused", resp.GetComponents()[1].GetDetails())
}

func TestGetStatusHealthyWhenAllPass(t *testing.T) {
	eng := &fakeSystemEngine{snapshot: snapshotOf(
		[3]any{"a", true, ""},
		[3]any{"b", true, ""},
	)}
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, eng)
	resp, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_HEALTHY, resp.GetState())
	assert.Len(t, resp.GetComponents(), 2)
}

func TestGetStatusTimeoutCountsUnhealthy(t *testing.T) {
	eng := &fakeSystemEngine{snapshot: snapshotOf(
		[3]any{"slow", false, "health check timed out"},
	)}
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, eng)
	resp, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
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

// authCtx 注入一条已认证身份（GetStatus 的详情面门：匿名只回最小态）。
func authCtx() context.Context {
	return authn.WithIdentity(context.Background(), &authn.Identity{
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleMemberID, TokenName: "status-probe",
	})
}

// countingProvider 记录 Health 调用次数（探活缓存命中锚）。
type countingProvider struct {
	name  string
	calls *int
}

func (c countingProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: c.name, Capability: capability.KindRuntime, Version: "test"}
}

func (c countingProvider) Health(context.Context) capability.HealthReport {
	*c.calls++
	return capability.HealthReport{Healthy: true, Details: "ok at 10.99.0.7:9000"}
}

// TestGetStatusAnonymousMinimalState：公开/匿名面只回最小态——不含
// components（内部拓扑/上游错误详情的载体）。
func TestGetStatusAnonymousMinimalState(t *testing.T) {
	eng := &fakeSystemEngine{snapshot: snapshotOf(
		[3]any{"managed-vl", false, "managed log store unreachable at 10.99.0.7:9428"},
	)}
	svc := New(buildinfo.BuildInfo{Version: "0.1.0-test"}, eng)
	resp, err := svc.GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState())
	assert.Equal(t, "0.1.0-test", resp.GetVersion())
	assert.Empty(t, resp.GetComponents(), "anonymous callers must not receive component details")
	body, err := protojson.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "components")
	assert.NotContains(t, string(body), "10.99.0.7")

	// 鉴权面：同一状态可见 components 与详情（排障动线）。
	authed, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	require.Len(t, authed.GetComponents(), 1)
	assert.Contains(t, authed.GetComponents()[0].GetDetails(), "10.99.0.7")
}

// TestGetStatusSurfacesEngineHealthComponents：引擎环死亡/收敛能力行经
// 健康组件面透出（R02-M7 / R16-L3）——任一不健康即 DEGRADED；匿名面照旧
// 不含 components（R08-M9 的最小态约束不被本行绕过）。
func TestGetStatusSurfacesEngineHealthComponents(t *testing.T) {
	svc := testService()
	svc.engineHealth = func() []api.HealthComponent {
		return []api.HealthComponent{
			{Name: "engine ring deployment", Healthy: false, Details: "ring=deployment dead"},
			{Name: api.ConvergenceHealthComponent, Healthy: true, Details: "posture=all, inspector=present"},
		}
	}
	resp, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState(),
		"a dead engine ring degrades the control plane status")
	require.Len(t, resp.GetComponents(), 2)
	assert.Equal(t, "engine ring deployment", resp.GetComponents()[0].GetName())
	assert.Equal(t, "engine", resp.GetComponents()[0].GetCapability())
	assert.Equal(t, "ring=deployment dead", resp.GetComponents()[0].GetDetails())
	assert.Equal(t, api.ConvergenceHealthComponent, resp.GetComponents()[1].GetName())

	anon, err := svc.GetStatus(context.Background(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Empty(t, anon.GetComponents(), "anonymous callers still get the minimal state (no engine details)")
}

// TestGetStatusDegradedByManagedProviderRow：受管 Provider 载体存活行
// （engine 健康组件面的 "managed proxy"）不健康即 DEGRADED——受管 Proxy
// Workload 宕机不再被 GetStatus 聚合吞掉（R07-M5 的接口侧面）。
func TestGetStatusDegradedByManagedProviderRow(t *testing.T) {
	svc := testService()
	svc.engineHealth = func() []api.HealthComponent {
		return []api.HealthComponent{
			{Name: api.ConvergenceHealthComponent, Healthy: true, Details: "posture=all, inspector=present"},
			{Name: "managed proxy", Healthy: false, Details: "workload=fleetly-proxy-traefik state=stopped: task exited with code 1"},
		}
	}
	resp, err := svc.GetStatus(authCtx(), &systemv1.GetStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, systemv1.StatusState_STATUS_STATE_DEGRADED, resp.GetState(),
		"a dead managed proxy workload degrades the control plane status")
	require.Len(t, resp.GetComponents(), 2)
	assert.Equal(t, "managed proxy", resp.GetComponents()[1].GetName())
	assert.False(t, resp.GetComponents()[1].GetHealthy())
	assert.Contains(t, resp.GetComponents()[1].GetDetails(), "state=stopped")
}

// TestGetStatusCachesProbes：探活结果 TTL 缓存——聚合搬 engine 后由
// TestComponentsHealthSnapshotCacheWindow（engine 侧）承载，本面只断言
// 快照消费的直通（同快照两次 GetStatus 零二次探活面）。〔B2b 搬迁〕
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

func TestExplainRejectsUnknownName(t *testing.T) {
	// 空 resource 的 E_INVALID_ARGUMENT 面已退役 descriptor 注解（P4-1 批 1
	// Run B2）：由 protovalidate 拦截器在受理面执法（文案逐字节钉在
	// apivalidate 负例矩阵 "ExplainRequest empty resource"），本 handler 级
	// 单测不经拦截器，只覆盖在案服务内检查。
	_, err := testService().Explain(context.Background(), &systemv1.ExplainRequest{Resource: "definitely.not-a-name"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	assert.Contains(t, err.Error(), "fleetly schema")
}

// TestRestartComponentEngineAbsent 钉引擎面缺席的精确拒绝（P4-3 批 3）：
// 重启编排经引擎面路由（capability.Managed 声明者），face 缺席是 honest
// failure——E_RUNTIME_FACE_UNSUPPORTED / Unimplemented，不再落 E_INTERNAL
// 脱敏 500（testService 的 engine 即 nil，直达分支）。S2 起平台管理员门
// 先于引擎分支，故本用例注入平台 owner 身份（handler 级单测不经拦截器，
// 身份按 authn.WithIdentity 装配）。
func TestRestartComponentEngineAbsent(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), &authn.Identity{
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID,
	})
	_, err := testService().RestartComponent(ctx, &systemv1.RestartComponentRequest{Name: "traefik"})
	require.Error(t, err)
	ae, ok := err.(*apperr.Error)
	require.True(t, ok, "engine-absent branch must produce an apperr envelope, got %T", err)
	assert.Equal(t, "E_RUNTIME_FACE_UNSUPPORTED", ae.Code())
	assert.Equal(t, "Unimplemented", apperrstatus.ToGRPCStatus(ae).Code().String())
	assert.Contains(t, ae.Message(), "requires the engine face")
	assert.NotEmpty(t, ae.Suggestion())
}
