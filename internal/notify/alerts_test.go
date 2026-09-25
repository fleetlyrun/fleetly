package notify

// 告警投递面单测（W5-S2，D-V3W5-1）：EnqueueAlert 的端点解析映射（缺省
// 全端点/channels 子集/未知与停用跳过/无端点零受理）、文案渲染表
//（FIRING/RESOLVED 前缀/severity 进文案）、投递全链（告警台账行 →
// attemptAlert → 假接收方）、**零事件红线**（投递前后 events 流零新增）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// seedEndpoint 落一条端点。
func seedEndpoint(t *testing.T, st *state.Store, name, typ string, enabled bool) state.WebhookEndpoint {
	t.Helper()
	ep, err := st.CreateWebhookEndpoint(context.Background(), state.WebhookEndpointWrite{
		Name: name, URL: "https://receiver.test/hook", Type: typ,
		SecretCipher: "cipher", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"*"}, Enabled: enabled,
	})
	if err != nil {
		t.Fatalf("seed endpoint %s: %v", name, err)
	}
	return ep
}

// eventsCount 是事件流账面（零事件红线的计数面）。
func eventsCount(t *testing.T, st *state.Store) int {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return len(evs)
}

// TestEnqueueAlertEndpointResolution 端点解析映射（设计 §2.3 语义表）。
func TestEnqueueAlertEndpointResolution(t *testing.T) {
	t.Run("default all enabled endpoints", func(t *testing.T) {
		m, st, _ := newTestEnv(t)
		epA := seedEndpoint(t, st, "a", state.WebhookChannelSlack, true)
		seedEndpoint(t, st, "b", state.WebhookChannelSlack, true)
		seedEndpoint(t, st, "c-disabled", state.WebhookChannelSlack, false)

		n, err := m.EnqueueAlert(context.Background(), AlertNotice{Name: "HighCPU", Status: AlertStatusFiring})
		if err != nil {
			t.Fatalf("EnqueueAlert: %v", err)
		}
		if n != 2 {
			t.Fatalf("accepted = %d, want 2 (enabled only)", n)
		}
		rows, err := st.ListWebhookDeliveries(context.Background(), "", state.WebhookDeliveryPending, 10)
		if err != nil {
			t.Fatalf("list deliveries: %v", err)
		}
		ids := map[string]bool{}
		for _, r := range rows {
			ids[r.EndpointID] = true
			if r.EventSeq != 0 || r.AlertPayload == "" {
				t.Fatalf("row = %+v, want event_seq=0 with payload", r)
			}
		}
		if !ids[epA.ID] || len(ids) != 2 {
			t.Fatalf("delivered endpoints = %v, want the two enabled ones", ids)
		}
		if eventsCount(t, st) != 0 {
			t.Fatal("alert enqueue must not emit events (zero-event red line)")
		}
	})

	t.Run("explicit subset with unknown and disabled skipped", func(t *testing.T) {
		m, st, _ := newTestEnv(t)
		epB := seedEndpoint(t, st, "b", state.WebhookChannelSlack, true)
		seedEndpoint(t, st, "a", state.WebhookChannelSlack, true)
		seedEndpoint(t, st, "off", state.WebhookChannelSlack, false)

		n, err := m.EnqueueAlert(context.Background(), AlertNotice{
			Name: "Down", Status: AlertStatusResolved,
			Channels: []string{epB.ID, "off", "ghost-ep"},
		})
		if err != nil {
			t.Fatalf("EnqueueAlert: %v", err)
		}
		if n != 1 {
			t.Fatalf("accepted = %d, want 1 (unknown/disabled skipped)", n)
		}
		rows, _ := st.ListWebhookDeliveries(context.Background(), "", "", 10)
		if len(rows) != 1 || rows[0].EndpointID != epB.ID {
			t.Fatalf("rows = %+v, want only epB", rows)
		}
		if eventsCount(t, st) != 0 {
			t.Fatal("zero-event red line broken")
		}
	})

	t.Run("no enabled endpoints accepts nothing", func(t *testing.T) {
		m, st, _ := newTestEnv(t)
		n, err := m.EnqueueAlert(context.Background(), AlertNotice{Name: "X", Status: AlertStatusFiring})
		if err != nil {
			t.Fatalf("EnqueueAlert: %v", err)
		}
		if n != 0 {
			t.Fatalf("accepted = %d, want 0", n)
		}
		if eventsCount(t, st) != 0 {
			t.Fatal("zero-event red line broken")
		}
	})
}

// TestAlertNoticeRenderingTable 文案渲染表：RESOLVED 前缀、severity 进文案、
// channels 注记不上文案（路由事实不上接收方内容面）。
func TestAlertNoticeRenderingTable(t *testing.T) {
	cases := []struct {
		name       string
		notice     AlertNotice
		wantSubstr []string
		notWant    []string
	}{
		{
			name: "firing head carries severity",
			notice: AlertNotice{
				Name: "HighCPU", Status: AlertStatusFiring, Severity: "critical",
				Labels: map[string]string{"alertname": "HighCPU", "severity": "critical"},
			},
			wantSubstr: []string{"[fleetly] FIRING HighCPU (severity=critical)", "label.severity: critical"},
		},
		{
			name: "resolved head carries the RESOLVED prefix",
			notice: AlertNotice{
				Name: "HighCPU", Status: AlertStatusResolved, Severity: "critical",
			},
			wantSubstr: []string{"[fleetly] RESOLVED HighCPU (severity=critical)"},
		},
		{
			name: "channels annotation stays off the text",
			notice: AlertNotice{
				Name:        "A",
				Status:      AlertStatusFiring,
				Annotations: map[string]string{"channels": "ep-1,ep-2", "runbook": "https://x"},
			},
			wantSubstr: []string{"annotation.runbook: https://x"},
			notWant:    []string{"ep-1,ep-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.notice.SlackText()
			for _, want := range tc.wantSubstr {
				if !strings.Contains(text, want) {
					t.Fatalf("text %q missing %q", text, want)
				}
			}
			for _, bad := range tc.notWant {
				if strings.Contains(text, bad) {
					t.Fatalf("text %q must not contain %q", text, bad)
				}
			}
			subject := tc.notice.EmailSubject()
			if !strings.Contains(subject, tc.notice.HeadText()) {
				t.Fatalf("email subject %q missing head text", subject)
			}
		})
	}
}

// TestAlertDeliveryWorkerEndToEnd 投递全链：EnqueueAlert 落台账 → 假接收方
// 收到 RESOLVED 文案（slack 形态）→ 台账终态 ok；**事件流零新增**。
func TestAlertDeliveryWorkerEndToEnd(t *testing.T) {
	m, st, _ := newTestEnv(t)
	ep := seedEndpoint(t, st, "slack-main", state.WebhookChannelSlack, true)

	var gotText string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotText = string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	testURL := receiver.URL
	if _, err := st.UpdateWebhookEndpoint(context.Background(), ep.ID, state.WebhookEndpointUpdate{URL: &testURL}); err != nil {
		t.Fatalf("point endpoint at test receiver: %v", err)
	}

	eventsBefore := eventsCount(t, st)
	if _, err := m.EnqueueAlert(context.Background(), AlertNotice{
		Name: "HighCPU", Status: AlertStatusResolved, Severity: "critical",
		Labels: map[string]string{"severity": "critical"},
	}); err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}
	if eventsCount(t, st) != eventsBefore {
		t.Fatal("alert delivery must not emit events (zero-event red line)")
	}

	// 直接驱动单次尝试（worker 主循环的调用面；不等 poll 周期）。
	due, err := st.DueWebhookDeliveries(context.Background(), time.Now().UTC().Add(time.Minute), 10, nil)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %d (err %v), want 1", len(due), err)
	}
	m.attempt(context.Background(), deliveryJob{deliveryID: due[0].ID, endpointID: due[0].EndpointID})

	d, err := st.GetWebhookDelivery(context.Background(), due[0].ID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if d.Status != state.WebhookDeliveryOK {
		t.Fatalf("delivery status = %s (last_error %q), want ok", d.Status, d.LastError)
	}
	if !strings.Contains(gotText, "[fleetly] RESOLVED HighCPU (severity=critical)") {
		t.Fatalf("receiver text = %q, want RESOLVED head", gotText)
	}

	// 事件流仍然零新增（投递完成后复核）。
	if eventsCount(t, st) != eventsBefore {
		t.Fatal("zero-event red line broken after delivery")
	}
}

// TestAlertDeliveryFailureRecordsBackoff 投递失败：退避定时落账（预算沿
// notify 既有表）；**零事件红线**在失败路径同样钉死。
func TestAlertDeliveryFailureRecordsBackoff(t *testing.T) {
	m, st, _ := newTestEnv(t)
	ep := seedEndpoint(t, st, "slack-main", state.WebhookChannelSlack, true)

	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()
	receiverURL := receiver.URL
	if _, err := st.UpdateWebhookEndpoint(context.Background(), ep.ID, state.WebhookEndpointUpdate{URL: &receiverURL}); err != nil {
		t.Fatalf("point endpoint at failing receiver: %v", err)
	}

	if _, err := m.EnqueueAlert(context.Background(), AlertNotice{Name: "A", Status: AlertStatusFiring}); err != nil {
		t.Fatalf("EnqueueAlert: %v", err)
	}
	due, err := st.DueWebhookDeliveries(context.Background(), time.Now().UTC().Add(time.Minute), 10, nil)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %d (err %v), want 1", len(due), err)
	}
	m.attempt(context.Background(), deliveryJob{deliveryID: due[0].ID, endpointID: due[0].EndpointID})

	d, err := st.GetWebhookDelivery(context.Background(), due[0].ID)
	if err != nil {
		t.Fatalf("get delivery: %v", err)
	}
	if d.Status != state.WebhookDeliveryPending || d.Attempts != 1 || d.NextRetryAt == nil {
		t.Fatalf("after failure: status=%s attempts=%d next=%v, want pending/1/scheduled", d.Status, d.Attempts, d.NextRetryAt)
	}
	if eventsCount(t, st) != 0 {
		t.Fatal("failing alert delivery must not emit events (zero-event red line)")
	}
}

// TestAlertPayloadRestartRoundtrip 重启恢复路径：台账行载荷重建（alert_
// payload 列 → alertNoticeFromJSON → webhook 载荷 type=alert、seq=0）。
func TestAlertPayloadRestartRoundtrip(t *testing.T) {
	notice := AlertNotice{
		Name: "HighCPU", Status: AlertStatusResolved, Severity: "critical",
		Labels:      map[string]string{"severity": "critical", "job": "cadvisor"},
		Annotations: map[string]string{"channels": "ep-1", "summary": "cpu"},
		Channels:    []string{"ep-1"},
		StartsAt:    time.Unix(1790000000, 0).UTC(),
	}
	raw, err := notice.payloadJSON()
	if err != nil {
		t.Fatalf("payloadJSON: %v", err)
	}
	back, err := alertNoticeFromJSON(raw)
	if err != nil {
		t.Fatalf("alertNoticeFromJSON: %v", err)
	}
	if back.Name != notice.Name || back.Status != notice.Status || back.Severity != "critical" {
		t.Fatalf("roundtrip = %+v", back)
	}
	if !back.StartsAt.Equal(notice.StartsAt) {
		t.Fatalf("startsAt = %v, want %v", back.StartsAt, notice.StartsAt)
	}
	payload := back.toPayload(1790000001, raw)
	if payload.Type != PayloadTypeAlert || payload.Seq != 0 || payload.Name != "alert.resolved" {
		t.Fatalf("payload = %+v", payload)
	}
	if !strings.Contains(string(payload.Payload), `"alertname":"HighCPU"`) {
		t.Fatalf("payload facts = %s", payload.Payload)
	}
}
