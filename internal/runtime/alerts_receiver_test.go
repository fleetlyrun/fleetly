package runtime

// 平台内建告警接收器的单测（W5-S2，D-V3W5-1）：载荷映射表驱动（firing/
// resolved/双状态回退/channels 解析/缺省全端点/畸形输入 400）、认证矩阵
//（Bearer/Basic 双形态、错凭据 401）、root handler 分派面（精确路径 POST、
// 分派面 = 豁免面）。

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/notify"
)

// fakeAlertNotifier 是 alertNotifier 的账面假件。
type fakeAlertNotifier struct {
	notices []notify.AlertNotice
	err     error
}

func (f *fakeAlertNotifier) EnqueueAlert(_ context.Context, n notify.AlertNotice) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.notices = append(f.notices, n)
	return 1, nil
}

func newTestReceiver(fk *fakeAlertNotifier) http.Handler {
	return newAlertsReceiverHandler("secret-token", fk, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestReceiverPayloadMappingTable 载荷映射表驱动：每例输入 Alertmanager v2
// 载荷 + 期望受理结果（通知字段逐项对照——接收器映射语义表的可执行形态）。
func TestReceiverPayloadMappingTable(t *testing.T) {
	const bearer = "Bearer secret-token"
	cases := []struct {
		name    string
		body    string
		wantCT  int   // 受理通知数
		wantSE  int   // 期望 HTTP 码
		check   func(t *testing.T, fk *fakeAlertNotifier)
	}{
		{
			name:   "firing with severity and channels",
			body:   `{"version":"5","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"HighCPU","severity":"critical"},"annotations":{"channels":"ep-1, ep-2","summary":"cpu high"},"startsAt":"2026-09-25T00:00:00Z"}]}`,
			wantCT: 1, wantSE: 200,
			check: func(t *testing.T, fk *fakeAlertNotifier) {
				n := fk.notices[0]
				if n.Name != "HighCPU" || n.Status != notify.AlertStatusFiring || n.Severity != "critical" {
					t.Fatalf("notice = %+v", n)
				}
				if strings.Join(n.Channels, "|") != "ep-1|ep-2" {
					t.Fatalf("channels = %v, want [ep-1 ep-2]", n.Channels)
				}
				if n.StartsAt.IsZero() {
					t.Fatal("startsAt not parsed")
				}
				if n.Annotations["summary"] != "cpu high" {
					t.Fatalf("annotations = %+v", n.Annotations)
				}
			},
		},
		{
			name:   "resolved per-alert status wins over top-level",
			body:   `{"status":"firing","alerts":[{"status":"resolved","labels":{"alertname":"HighCPU"}}]}`,
			wantCT: 1, wantSE: 200,
			check: func(t *testing.T, fk *fakeAlertNotifier) {
				if !fk.notices[0].Resolved() {
					t.Fatalf("status = %s, want resolved", fk.notices[0].Status)
				}
			},
		},
		{
			name:   "top-level status fallback and firing default head",
			body:   `{"status":"resolved","alerts":[{"labels":{"alertname":"Down"}},{"labels":{"alertname":"X"}},{"status":"firing","labels":{"alertname":"Y"}}]}`,
			wantCT: 3, wantSE: 200,
			check: func(t *testing.T, fk *fakeAlertNotifier) {
				if !fk.notices[0].Resolved() || !fk.notices[1].Resolved() {
					t.Fatalf("top-level resolved fallback broken: %+v", fk.notices)
				}
				if fk.notices[2].Status != notify.AlertStatusFiring {
					t.Fatalf("per-alert firing must win: %+v", fk.notices[2])
				}
			},
		},
		{
			name:   "missing alertname becomes unnamed",
			body:   `{"alerts":[{"labels":{}}]}`,
			wantCT: 1, wantSE: 200,
			check: func(t *testing.T, fk *fakeAlertNotifier) {
				if fk.notices[0].Name != "unnamed" {
					t.Fatalf("name = %q, want unnamed", fk.notices[0].Name)
				}
			},
		},
		{
			name:   "empty channels default-all sentinel",
			body:   `{"alerts":[{"labels":{"alertname":"A"},"annotations":{"channels":""}}]}`,
			wantCT: 1, wantSE: 200,
			check: func(t *testing.T, fk *fakeAlertNotifier) {
				if len(fk.notices[0].Channels) != 0 {
					t.Fatalf("channels = %v, want empty (default-all)", fk.notices[0].Channels)
				}
			},
		},
		{
			name:   "empty alerts array is a no-op",
			body:   `{"alerts":[]}`,
			wantCT: 0, wantSE: 200,
		},
		{
			name:   "invalid status rejected",
			body:   `{"alerts":[{"status":"bogus","labels":{"alertname":"A"}}]}`,
			wantCT: 0, wantSE: 400,
		},
		{
			name:   "bad json rejected",
			body:   `{not-json`,
			wantCT: 0, wantSE: 400,
		},
		{
			name:   "empty body rejected",
			body:   ``,
			wantCT: 0, wantSE: 400,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &fakeAlertNotifier{}
			srv := httptest.NewServer(newTestReceiver(fk))
			defer srv.Close()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/alerts", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Authorization", bearer)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.wantSE {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantSE)
			}
			if len(fk.notices) != tc.wantCT {
				t.Fatalf("accepted = %d, want %d", len(fk.notices), tc.wantCT)
			}
			if tc.check != nil {
				tc.check(t, fk)
			}
		})
	}
}

// TestReceiverAuthMatrix 认证矩阵：Bearer/Basic 双形态（vmalert basicAuth
// 的发送形态 = 密码位凭据）、错凭据/缺凭据/坏编码恒 401 无信息泄露。
func TestReceiverAuthMatrix(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"bearer ok", "Bearer secret-token", 200},
		{"bearer wrong", "Bearer wrong-token", 401},
		{"bearer case-insensitive scheme", "bearer secret-token", 200},
		{"basic ok (password carries the credential)", "Basic " + base64.StdEncoding.EncodeToString([]byte("fleetly:secret-token")), 200},
		{"basic wrong password", "Basic " + base64.StdEncoding.EncodeToString([]byte("fleetly:wrong")), 401},
		{"basic credential in username slot", "Basic " + base64.StdEncoding.EncodeToString([]byte("secret-token:x")), 401},
		{"basic bad encoding", "Basic !!!not-base64!!!", 401},
		{"missing header", "", 401},
		{"foreign scheme", "Digest abc", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := &fakeAlertNotifier{}
			srv := httptest.NewServer(newTestReceiver(fk))
			defer srv.Close()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/alerts", strings.NewReader(`{"alerts":[]}`))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// TestReceiverMethodGate 分派面：POST 精确路径进 handler；其余方法 405。
func TestReceiverMethodGate(t *testing.T) {
	fk := &fakeAlertNotifier{}
	srv := httptest.NewServer(newTestReceiver(fk))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/alerts", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", res.StatusCode)
	}
}

// TestReceiverDispatchViaRootHandler root handler 分派：装配后的 POST /
// internal/alerts 走接收器（分派面 = 豁免面——其余路径原样进 gateway）。
func TestReceiverDispatchViaRootHandler(t *testing.T) {
	fk := &fakeAlertNotifier{}
	alerts := newAlertsReceiverHandler("secret-token", fk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	root := newRootHandler(nil, nil, nil, alerts, http.NotFoundHandler())

	// POST 精确路径分派到接收器（错凭据 401 来自接收器而非 fallback 404）。
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/alerts", strings.NewReader(`{"alerts":[]}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	root.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("dispatched POST status = %d, want 200", rec.Code)
	}
	if len(fk.notices) != 0 {
		t.Fatalf("notices = %d, want 0 (empty alerts)", len(fk.notices))
	}

	// 非 POST 不分派（进 fallback 404——分派面 = 精确 POST 面）。
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal/alerts", nil)
	root.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("undispatched GET status = %d, want 404", rec.Code)
	}

	// alerts handler 未装配（nil）时路径进 gateway fallback（既有行为不变）。
	root2 := newRootHandler(nil, nil, nil, nil, http.NotFoundHandler())
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/internal/alerts", strings.NewReader(`{}`))
	root2.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unassembled POST status = %d, want 404 (fallback)", rec.Code)
	}
}
