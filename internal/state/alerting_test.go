package state

// alerting.* 的存储层测试（W5-S2，D-V3W5-1）：alerts.mode 设置（缺省态/
// 保存往返/前置门 metrics.mode=on 409）+ alert_rules CRUD（名字平台级唯一/
// expr 形状/for_duration/labels/channels JSON 形状/审计 alerting.rule_
// changed/**零事件红线**——全链 events 流零新增）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// newAlertingStore 是告警测试的公共装配（testfixture 同款真库）。
func newAlertingStore(t *testing.T) *Store {
	t.Helper()
	return newTestStore(t)
}

// eventsCountSinceZero 读事件流总数（零事件红线的账面）。
func eventsCountSinceZero(t *testing.T, st *Store) int {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return len(evs)
}

// TestAlertsModeDefaultUnset 缺省态：未设置 = unset 生效、Set=false。
func TestAlertsModeDefaultUnset(t *testing.T) {
	st := newAlertingStore(t)
	in, err := st.LoadAlertsSettings(context.Background())
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if in.Mode != AlertsModeUnset || in.Set {
		t.Fatalf("default alerts settings = %+v, want unset/set=false", in)
	}
}

// TestSaveAlertsSettingsRequiresMetricsOn 前置门（设计 §2.1）：metrics.mode
// 非 on 时 alerts.mode=on 拒绝 409 E_ALERTS_METRICS_REQUIRED；metrics.mode=on
// 后放行；unset 恒允许（关闭不依赖 metrics 形态）。
func TestSaveAlertsSettingsRequiresMetricsOn(t *testing.T) {
	st := newAlertingStore(t)
	ctx := context.Background()

	// ① metrics 未设置（缺省 unset）：alerts.mode=on 拒绝 409。
	err := st.SaveAlertsSettings(ctx, AlertsModeOn, AlertsSaveOptions{Actor: "human"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != "E_ALERTS_METRICS_REQUIRED" {
		t.Fatalf("save on without metrics: err = %v, want E_ALERTS_METRICS_REQUIRED", err)
	}
	if ae.HTTPStatus() != 409 {
		t.Fatalf("gate status = %d, want 409", ae.HTTPStatus())
	}
	// 拒绝后设置未落行。
	in, err := st.LoadAlertsSettings(ctx)
	if err != nil {
		t.Fatalf("load after rejection: %v", err)
	}
	if in.Set {
		t.Fatalf("rejected save must not persist, got %+v", in)
	}

	// ② metrics.mode=on 后放行。
	if err := st.SaveMetricsSettings(ctx, MetricsModeOn, MetricsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save metrics on: %v", err)
	}
	if err := st.SaveAlertsSettings(ctx, AlertsModeOn, AlertsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save on with metrics on: %v", err)
	}
	in, err = st.LoadAlertsSettings(ctx)
	if err != nil {
		t.Fatalf("load after save: %v", err)
	}
	if in.Mode != AlertsModeOn || !in.Set {
		t.Fatalf("saved alerts settings = %+v, want on/set=true", in)
	}

	// ③ unset 恒允许（metrics 仍 on）。
	if err := st.SaveAlertsSettings(ctx, AlertsModeUnset, AlertsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save unset: %v", err)
	}

	// ④ metrics 回 unset 后再开 alerts 仍被门拦。
	if err := st.SaveMetricsSettings(ctx, MetricsModeUnset, MetricsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save metrics unset: %v", err)
	}
	if err := st.SaveAlertsSettings(ctx, AlertsModeOn, AlertsSaveOptions{Actor: "human"}); !errors.As(err, &ae) || ae.Code() != "E_ALERTS_METRICS_REQUIRED" {
		t.Fatalf("save on with metrics unset: err = %v, want E_ALERTS_METRICS_REQUIRED", err)
	}

	// ⑤ 非法值显式拒绝（loud-fail）。
	if err := st.SaveAlertsSettings(ctx, "bogus", AlertsSaveOptions{Actor: "human"}); err == nil {
		t.Fatal("save bogus mode must fail")
	}
}

// TestSaveAlertsSettingsAuditsOnly 保存落审计且**零事件**（告警全链不经事
// 件流——防回环红线的设置面锚）。metrics.mode 的保存（前置门铺路）自带既
// 有事件 metrics.mode_updated，故以它落定后的账面为基线，断言 alerts 保存
// 零新增。
func TestSaveAlertsSettingsAuditsOnly(t *testing.T) {
	st := newAlertingStore(t)
	ctx := context.Background()
	if err := st.SaveMetricsSettings(ctx, MetricsModeOn, MetricsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save metrics on: %v", err)
	}
	baseline := eventsCountSinceZero(t, st)
	if err := st.SaveAlertsSettings(ctx, AlertsModeOn, AlertsSaveOptions{Actor: "human"}); err != nil {
		t.Fatalf("save alerts on: %v", err)
	}
	if eventsCountSinceZero(t, st) != baseline {
		t.Fatal("alerts.mode save must not emit events (zero-event red line)")
	}
	entries, _, err := st.ListAudits(ctx, AuditQuery{Action: "alerting.mode_changed", Limit: 10})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(entries))
	}
}

// TestAlertRuleCRUDRoundtrip 创建/读/改/删往返 + 审计 + 零事件。
func TestAlertRuleCRUDRoundtrip(t *testing.T) {
	st := newAlertingStore(t)
	ctx := context.Background()

	created, err := st.CreateAlertRule(ctx, AlertRuleWrite{
		Name:               "high-cpu",
		Expr:               `sum(rate(container_cpu_usage_seconds_total[2m])) > 0.9`,
		ForDurationSeconds: 300,
		Labels:             map[string]string{"severity": "critical", "team": "sre"},
		Channels:           []string{"ep_01", "ep_02"},
		Actor:              "human",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.Name != "high-cpu" || created.ForDurationSeconds != 300 {
		t.Fatalf("created = %+v", created)
	}
	if created.Labels["severity"] != "critical" || len(created.Channels) != 2 {
		t.Fatalf("created json roundtrip = %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("timestamps must be set: %+v", created)
	}

	// 列表/单读。
	rows, err := st.ListAlertRules(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %v (err %v), want 1 row", rows, err)
	}
	got, err := st.GetAlertRule(ctx, created.ID)
	if err != nil || got.Name != "high-cpu" {
		t.Fatalf("get = %+v (err %v)", got, err)
	}

	// 更新（部分字段；labels/channels 整体替换）。
	newExpr := `up == 0`
	newFor := int64(60)
	newLabels := map[string]string{"severity": "warning"}
	updated, err := st.UpdateAlertRule(ctx, created.ID, AlertRuleUpdate{
		Expr:               &newExpr,
		ForDurationSeconds: &newFor,
		Labels:             &newLabels,
		Channels:           []string{"ep_03"},
		Actor:              "human",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Expr != newExpr || updated.ForDurationSeconds != 60 || updated.Labels["severity"] != "warning" {
		t.Fatalf("updated = %+v", updated)
	}
	if len(updated.Channels) != 1 || updated.Channels[0] != "ep_03" {
		t.Fatalf("updated channels = %v", updated.Channels)
	}

	// 删除 + NotFound。
	if err := st.DeleteAlertRule(ctx, created.ID, "human", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetAlertRule(ctx, created.ID); !errors.Is(err, ErrAlertRuleNotFound) {
		t.Fatalf("get after delete = %v, want ErrAlertRuleNotFound", err)
	}
	if err := st.DeleteAlertRule(ctx, created.ID, "human", ""); !errors.Is(err, ErrAlertRuleNotFound) {
		t.Fatalf("double delete = %v, want ErrAlertRuleNotFound", err)
	}

	// 审计三行（create/update/delete 同一 action）+ 零事件。
	entries, total, err := st.ListAudits(ctx, AuditQuery{Action: "alerting.rule_changed", Limit: 10})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(entries) != 3 || total != 3 {
		t.Fatalf("audit entries = %d (total %d), want 3 (create/update/delete)", len(entries), total)
	}
	if eventsCountSinceZero(t, st) != 0 {
		t.Fatal("alert rule CRUD must not emit events (zero-event red line)")
	}
}

// TestAlertRuleValidation 形状校验矩阵（存储不变量第二道闸）。
func TestAlertRuleValidation(t *testing.T) {
	st := newAlertingStore(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		write  AlertRuleWrite
		wantErr string
	}{
		{"empty name", AlertRuleWrite{Name: "", Expr: "up == 0"}, "invalid"},
		{"bad name", AlertRuleWrite{Name: "-lead", Expr: "up == 0"}, "invalid"},
		{"empty expr", AlertRuleWrite{Name: "r1", Expr: "   "}, "E_ALERT_RULE_EXPR_INVALID"},
		{"long expr", AlertRuleWrite{Name: "r1", Expr: string(make([]byte, 2049))}, "E_ALERT_RULE_EXPR_INVALID"},
		{"negative for", AlertRuleWrite{Name: "r1", Expr: "up == 0", ForDurationSeconds: -1}, "E_ALERT_RULE_FOR_INVALID"},
		{"empty label key", AlertRuleWrite{Name: "r1", Expr: "up == 0", Labels: map[string]string{"": "v"}}, "E_ALERT_RULE_LABELS_INVALID"},
		{"empty channel", AlertRuleWrite{Name: "r1", Expr: "up == 0", Channels: []string{" "}}, "E_ALERT_RULE_CHANNELS_INVALID"},
	}
	for _, tc := range cases {
		_, err := st.CreateAlertRule(ctx, tc.write)
		if err == nil {
			t.Fatalf("%s: create must fail", tc.name)
		}
		if tc.wantErr != "invalid" {
			var ae *apperr.Error
			if !errors.As(err, &ae) || ae.Code() != tc.wantErr {
				t.Fatalf("%s: err = %v, want code %s", tc.name, err, tc.wantErr)
			}
		}
	}

	// 名字平台级唯一（UNIQUE → ErrAlertRuleNameConflict）。
	if _, err := st.CreateAlertRule(ctx, AlertRuleWrite{Name: "dup", Expr: "up == 0"}); err != nil {
		t.Fatalf("seed dup: %v", err)
	}
	if _, err := st.CreateAlertRule(ctx, AlertRuleWrite{Name: "dup", Expr: "up == 1"}); !errors.Is(err, ErrAlertRuleNameConflict) {
		t.Fatalf("dup create = %v, want ErrAlertRuleNameConflict", err)
	}
	// 改名撞唯一约束同投影。
	first, err := st.ListAlertRules(ctx)
	if err != nil || len(first) != 1 {
		t.Fatalf("list: %v (%v)", first, err)
	}
	other := "other"
	if err := ValidateAlertRuleName(other); err != nil {
		t.Fatalf("seed name: %v", err)
	}
	if _, err := st.CreateAlertRule(ctx, AlertRuleWrite{Name: other, Expr: "up == 0"}); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	rename := other
	if _, err := st.UpdateAlertRule(ctx, first[0].ID, AlertRuleUpdate{Name: &rename}); !errors.Is(err, ErrAlertRuleNameConflict) {
		t.Fatalf("rename conflict = %v, want ErrAlertRuleNameConflict", err)
	}
}

// TestCreateAlertDeliveries 告警投递台账原语：event_seq=0 哨兵 + 载荷列 +
// 端点隔离 + 零事件。
func TestCreateAlertDeliveries(t *testing.T) {
	st := newAlertingStore(t)
	ctx := context.Background()
	for _, name := range []string{"ep-a", "ep-b"} {
		if _, err := st.CreateWebhookEndpoint(ctx, WebhookEndpointWrite{
			Name: name, URL: "https://example.invalid/hook", Type: WebhookChannelSlack,
			SecretCipher: "cipher", SecretFingerprint: "0123456789abcdef",
			EventPatterns: []string{"*"}, Enabled: true,
		}); err != nil {
			t.Fatalf("seed endpoint %s: %v", name, err)
		}
	}
	eps, err := st.ListWebhookEndpoints(ctx)
	if err != nil || len(eps) != 2 {
		t.Fatalf("seed endpoints: %v (%v)", eps, err)
	}

	rows, err := st.CreateAlertDeliveries(ctx, []string{eps[0].ID, eps[1].ID}, `{"alertname":"HighCPU"}`)
	if err != nil {
		t.Fatalf("create alert deliveries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.EventSeq != 0 {
			t.Fatalf("alert delivery event_seq = %d, want 0 sentinel", r.EventSeq)
		}
		if r.Status != WebhookDeliveryPending || r.AlertPayload != `{"alertname":"HighCPU"}` {
			t.Fatalf("row = %+v", r)
		}
	}
	// 读面回读（列投影含 alert_payload）。
	got, err := st.GetWebhookDelivery(ctx, rows[0].ID)
	if err != nil || got.AlertPayload != `{"alertname":"HighCPU"}` {
		t.Fatalf("get delivery = %+v (err %v)", got, err)
	}
	due, err := st.DueWebhookDeliveries(ctx, time.Now().UTC(), 10, nil)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %d (err %v), want 2", len(due), err)
	}
	if eventsCountSinceZero(t, st) != 0 {
		t.Fatal("alert deliveries must not emit events (zero-event red line)")
	}
}
