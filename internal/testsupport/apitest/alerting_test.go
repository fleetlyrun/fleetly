package apitest_test

// Alerting/Metrics 面 apitest（F2.5，ADR-0041）：通道全生命周期 + 规则全
// 生命周期（delete coverage 守卫的消费面）+ 查询面停用形态 + 凭证只写
// 不读（list 形态断言）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestAlertingChannelsLifecycle(t *testing.T) {
	h := apitest.New(t)
	c, ctx := alertingClient(t, h)

	created, err := c.Alerting.CreateNotificationChannel(ctx, &telemetryv1.CreateNotificationChannelRequest{
		Name: "ops", Kind: "webhook", Url: "http://127.0.0.1:9/x",
	})
	require.NoError(t, err)
	require.NotNil(t, created.GetChannel())
	assert.NotEmpty(t, created.GetChannel().GetId())

	// 凭证只写不读：list 形态无 url/bot_token 字段（proto 本体无此字段——
	// 行级断言防未来面漂移）。
	listed, err := c.Alerting.ListNotificationChannels(ctx, &telemetryv1.ListNotificationChannelsRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetChannels(), 1)
	assert.Equal(t, "ops", listed.GetChannels()[0].GetName())

	// 重名拒绝。
	_, err = c.Alerting.CreateNotificationChannel(ctx, &telemetryv1.CreateNotificationChannelRequest{
		Name: "ops", Kind: "webhook", Url: "http://127.0.0.1:9/y",
	})
	assert.Error(t, err)

	// test：不可达端点的失败载荷是响应数据（delivered=false + error 文本）。
	tested, err := c.Alerting.TestNotificationChannel(ctx, &telemetryv1.TestNotificationChannelRequest{ChannelId: created.GetChannel().GetId()})
	require.NoError(t, err)
	assert.False(t, tested.GetDelivered())
	assert.NotEmpty(t, tested.GetError())

	// 删除（coverage 锚）+ 删后 404。
	_, err = c.Alerting.DeleteNotificationChannel(ctx, &telemetryv1.DeleteNotificationChannelRequest{ChannelId: created.GetChannel().GetId()})
	require.NoError(t, err)
	_, err = c.Alerting.DeleteNotificationChannel(ctx, &telemetryv1.DeleteNotificationChannelRequest{ChannelId: created.GetChannel().GetId()})
	assert.Error(t, err, "second delete must 404")
}

func TestAlertingRulesLifecycle(t *testing.T) {
	h := apitest.New(t)
	c, ctx := alertingClient(t, h)
	appID := alertingSeedApp(t, h)

	created, err := c.Alerting.CreateAlertRule(ctx, &telemetryv1.CreateAlertRuleRequest{
		AppId: appID, Metric: "cpu_percent", Threshold: 90, ForSeconds: 300,
	})
	require.NoError(t, err)
	ruleID := created.GetRule().GetId()

	// 值域执法：未知 metric 拒绝。
	_, err = c.Alerting.CreateAlertRule(ctx, &telemetryv1.CreateAlertRuleRequest{
		AppId: appID, Metric: "disk_full", Threshold: 1,
	})
	assert.Error(t, err)
	// 跨 Team App 拒绝（行级授权——种子外项目）。
	_, err = c.Alerting.CreateAlertRule(ctx, &telemetryv1.CreateAlertRuleRequest{
		AppId: "01M01M01M01M01M01M01M01M01M", Metric: "cpu_percent", Threshold: 1,
	})
	assert.Error(t, err)

	rules, err := c.Alerting.ListAlertRules(ctx, &telemetryv1.ListAlertRulesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, rules.GetRules(), 1)

	// states 含系统内置行（ADR-0082 起两行：platform-offsite-backup +
	// converge-failed——收敛冷却的内置告警面）。
	states, err := c.Alerting.ListAlertStates(ctx, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	systemRules := map[string]bool{}
	for _, s := range states.GetStates() {
		if s.GetSystem() {
			systemRules[s.GetRuleId()] = true
			assert.Equal(t, "ok", s.GetState(), "no cooldown/fire at rest")
		}
	}
	assert.True(t, systemRules["platform-offsite-backup"], "offsite rule must ride the states face")
	assert.True(t, systemRules["converge-failed"], "converge rule must ride the states face")

	// 删除（coverage 锚）+ 删后 404 + states 收缩。
	_, err = c.Alerting.DeleteAlertRule(ctx, &telemetryv1.DeleteAlertRuleRequest{RuleId: ruleID})
	require.NoError(t, err)
	_, err = c.Alerting.DeleteAlertRule(ctx, &telemetryv1.DeleteAlertRuleRequest{RuleId: ruleID})
	assert.Error(t, err)
	states, err = c.Alerting.ListAlertStates(ctx, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	for _, s := range states.GetStates() {
		assert.NotEqual(t, ruleID, s.GetRuleId())
	}
}

func TestMetricsQueryDisabledFace(t *testing.T) {
	h := apitest.New(t)
	c, ctx := alertingClient(t, h)
	// 夹具未配置 metrics.addr → 精确失败（P4-3 批 3 未配置族码分流：
	// E_METRICS_STORE_UNAVAILABLE 503 信封；message 本就面向用户（含
	// metrics.addr 处置提示），非 E_INTERNAL 不铸 error_id——P0-3 出口
	// 纪律零影响面；此前落 E_INTERNAL 脱敏 500）。
	_, err := c.Metrics.QueryMetrics(ctx, &telemetryv1.QueryMetricsRequest{Query: "up"})
	require.Error(t, err)
	assert.Equal(t, "Unavailable", status.Code(err).String())
	assert.Contains(t, status.Convert(err).Message(), "require the managed metrics store",
		"config-level message is user-facing by design, not a leaked cause")
	env, ok := apperrstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_METRICS_STORE_UNAVAILABLE", env.Code())
	assert.NotEmpty(t, env.Suggestion())
	assert.Empty(t, env.ErrorID(), "non-E_INTERNAL exits mint no error_id")

	// REST 面：503 + 同信封（errcodeToHTTP 显式映射；与 registry 503 同款
	// 断言形态——语义真源在错误码，不在传输层机械映射）。
	rec := h.REST().Get("/v1/metrics?query=up")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_METRICS_STORE_UNAVAILABLE"`)
}

// TestMetricsQueryUpstreamRejection 钉上游 4xx 的信封分流（F-B1）：假
// Metrics 面回 capability.MetricsQueryError（真机形态：VM 对缺 group_left
// 的多对一除法回 422）→ E_INVALID_ARGUMENT 带上游原文，绝不落 E_INTERNAL
// 500——调用方查询错与平台故障面的信封边界。
func TestMetricsQueryUpstreamRejection(t *testing.T) {
	h := apitest.New(t)
	h.Services.Metrics = &fakeMetrics{queryErr: &capability.MetricsQueryError{
		Status:  http.StatusUnprocessableEntity,
		Message: `error when executing query: duplicate time series on the left side of / on(node)`,
	}}
	c, ctx := alertingClient(t, h)
	_, err := c.Metrics.QueryMetrics(ctx, &telemetryv1.QueryMetricsRequest{Query: "100 * rate(x[2m]) / on(node) y"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")
	assert.Contains(t, err.Error(), "duplicate time series")
	assert.NotContains(t, err.Error(), "E_INTERNAL")
}

// fakeMetrics 是只承接查询错误注入的假 Metrics 面（其余动词不在用例面）。
type fakeMetrics struct {
	queryErr error

	// scoped 记录 scoped 查询（授权/参数构造的断言面）；scopedPage 与
	// scopedErr 是注入的返回面。
	scoped     []capability.ScopedQuery
	scopedPage capability.SeriesPage
	scopedErr  error
}

func (f *fakeMetrics) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-metrics", Capability: capability.KindMetrics}
}

func (f *fakeMetrics) Health(_ context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeMetrics) ImportPrometheus(_ context.Context, _ []byte, _ map[string]string) error {
	return nil
}

func (f *fakeMetrics) QuerySeries(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]capability.Series, error) {
	return nil, f.queryErr
}

func (f *fakeMetrics) QueryScoped(_ context.Context, q capability.ScopedQuery) (capability.SeriesPage, error) {
	f.scoped = append(f.scoped, q)
	return f.scopedPage, f.scopedErr
}

// scopedQueries 返回 scoped 查询记录快照。
func (f *fakeMetrics) scopedQueries() []capability.ScopedQuery {
	out := make([]capability.ScopedQuery, len(f.scoped))
	copy(out, f.scoped)
	return out
}

// alertingClient 铸带 owner token 的类型化客户端（apitest 同款形态）。
func alertingClient(t *testing.T, h *apitest.Harness) (*fleetly.Client, context.Context) {
	t.Helper()
	c, err := fleetly.Dial("passthrough:///bufnet", h.DialOpts()...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, h.Ctx()
}

// alertingSeedApp 播 project/app 夹具行，返回 appID（builder 三件收口）。
func alertingSeedApp(t *testing.T, h *apitest.Harness) string {
	t.Helper()
	return h.CreateApp(t, h.CreateProject(t, "alerting").GetId(), "web").GetId()
}
