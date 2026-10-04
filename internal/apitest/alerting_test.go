package apitest_test

// Alerting/Metrics 面 apitest（F2.5，ADR-0041）：通道全生命周期 + 规则全
// 生命周期（delete coverage 守卫的消费面）+ 查询面停用形态 + 凭证只写
// 不读（list 形态断言）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
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

	// states 含系统内置行。
	states, err := c.Alerting.ListAlertStates(ctx, &telemetryv1.ListAlertStatesRequest{})
	require.NoError(t, err)
	foundSystem := false
	for _, s := range states.GetStates() {
		if s.GetSystem() {
			foundSystem = true
			assert.Equal(t, "platform-offsite-backup", s.GetRuleId())
		}
	}
	assert.True(t, foundSystem, "system rule must ride the states face")

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
	// 夹具未配置 metrics.addr → 精确失败（E_INTERNAL 信封）。
	_, err := c.Metrics.QueryMetrics(ctx, &telemetryv1.QueryMetricsRequest{Query: "up"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metrics store")
}

// alertingClient 铸带 owner token 的类型化客户端（apitest 同款形态）。
func alertingClient(t *testing.T, h *apitest.Harness) (*fleetly.Client, context.Context) {
	t.Helper()
	c, err := fleetly.Dial("passthrough:///bufnet", h.DialOpts()...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, fleetly.WithToken(context.Background(), h.Token)
}

// alertingSeedApp 播 project/app 夹具行，返回 appID。
func alertingSeedApp(t *testing.T, h *apitest.Harness) string {
	t.Helper()
	c, err := fleetly.Dial("passthrough:///bufnet", h.DialOpts()...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := fleetly.WithToken(context.Background(), h.Token)
	proj, err := c.Projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "alerting"})
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	app, err := c.Apps.CreateApp(ctx, &structurev1.CreateAppRequest{ProjectId: proj.GetProject().GetId(), Name: "web"})
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}
	return app.GetApp().GetId()
}
