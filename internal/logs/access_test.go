package logs

// 访问日志采集的 hermetic 单测（E6 观测专项设计 §3.2，W5-S2）：
//   - traefik access JSON 识别（非访问行/坏行跳过）；
//   - RouterName 反解 = ingress 真实命名公式（对照 import 钉死）+ 候选
//     匹配（含 '-' 的歧义消解）；
//   - 采集只进批量器：不进 ring（直播面零改动）、不落 JSONL；
//   - 部署归因 TTL 缓存（窗内一次查询；失败不缓存）；
//   - backend=jsonl 时采集整体跳过（无消费方——与 JSONL 边界一致）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// accessLineJSON 构造一条 traefik v3.5 形态的 access JSON 行（真实字段集
// ——识别与解析按这些键消费；e2e 断言组 B 消费同一形态的真机产物）。
func accessLineJSON(router, method, status, host, uri, clientAddr string, durationNS int64, at time.Time) string {
	return fmt.Sprintf(`{"DownstreamContentSize":0,"DownstreamStatus":%s,"Duration":%d,`+
		`"RequestAddr":"%s:443","RequestContentSize":0,"RequestHost":"%s","RequestMethod":"%s",`+
		`"RequestProtocol":"HTTP/2.0","RequestScheme":"https","RequestURI":"%s","RetryCount":0,`+
		`"RouterName":"%s","ServiceName":"fleetly-webapp-web@http","StartUTC":"%s",`+
		`"ClientAddr":"%s","ClientHost":"%s","level":"INFO","msg":"Traefik access log","time":"%s"}`,
		status, durationNS, host, host, method, uri, router,
		at.UTC().Format(time.RFC3339), clientAddr, clientAddr,
		at.UTC().Format(time.RFC3339))
}

func TestAccessLineOfParsesTraefikAccessJSON(t *testing.T) {
	at := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	line := accessLineJSON("fleetly-webapp-web-websecure@http", "GET", "200",
		"app.example.com", "/api/items?token=secret-token-12345", "10.216.0.5:53112", 1_500_000, at)
	acc, ok := accessLineOf(line)
	if !ok {
		t.Fatal("access line not recognized")
	}
	if acc.RouterName != "fleetly-webapp-web-websecure@http" {
		t.Fatalf("router = %q", acc.RouterName)
	}
	if acc.RequestMethod != "GET" || acc.DownstreamStatus.String() != "200" {
		t.Fatalf("method/status = %s/%s", acc.RequestMethod, acc.DownstreamStatus)
	}
	if acc.RequestHost != "app.example.com" || acc.RequestURI != "/api/items?token=secret-token-12345" {
		t.Fatalf("host/uri = %s / %s", acc.RequestHost, acc.RequestURI)
	}

	// 非访问行（traefik 运行日志/启动横幅/坏 JSON/缺 RouterName 的 JSON）。
	for name, raw := range map[string]string{
		"runtime text":        `time="2026-09-21T08:00:00Z" level=info msg="Configuration loaded"`,
		"startup banner":      "Traefik version 3.5.0",
		"bad json":            `{"DownstreamStatus":`,
		"json w/o router":     `{"level":"INFO","msg":"Traefik access log"}`,
		"empty":               "",
		"non-numeric status":  `{"RouterName":"r@http","DownstreamStatus":"ok"}`,
		"json w/o routername": `{"DownstreamStatus":200}`,
	} {
		if _, ok := accessLineOf(raw); ok {
			t.Fatalf("%s must not be recognized as an access line: %q", name, raw)
		}
	}
}

// TestAccessRouterNameFormulaMatchesIngress 公式一致性（验收：反解用
// ingress 真实命名公式）：生产代码本包私有副本 vs ingress.RouterName 真源
// ——单边改式即本测试红；入口服务名字符串同理对照。
func TestAccessRouterNameFormulaMatchesIngress(t *testing.T) {
	if accessIngressService != ingress.IngressServiceName {
		t.Fatalf("ingress service name drift: %q vs %q", accessIngressService, ingress.IngressServiceName)
	}
	for _, tc := range []struct{ app, service string }{
		{"webapp", "web"},
		{"my-cool-app", "web"},
		{"app", "web-app"},
		{"a1", "svc_2.proxy"},
		{"demo", "db"},
	} {
		if got, want := accessRouterName(tc.app, tc.service), ingress.RouterName(tc.app, tc.service); got != want {
			t.Fatalf("router name formula drift for (%s, %s): %q vs ingress %q", tc.app, tc.service, got, want)
		}
	}
}

func TestStripAccessRouterKey(t *testing.T) {
	for raw, want := range map[string]string{
		"fleetly-webapp-web-web@http":       "fleetly-webapp-web",
		"fleetly-webapp-web-websecure@http": "fleetly-webapp-web",
		"fleetly-registry-web@http":         "fleetly-registry",
		"fleetly-registry-websecure@http":   "fleetly-registry",
		"fleetly-fallback@http":             "fleetly-fallback",
		"acme-challenge-http@http":          "acme-challenge-http",
	} {
		if got := stripAccessRouterKey(raw); got != want {
			t.Fatalf("strip(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAccessRouteOfResolvesViaCandidates(t *testing.T) {
	apps := []accessTarget{
		{app: state.App{ID: "id1", Name: "webapp"}, service: "web"},
		{app: state.App{ID: "id2", Name: "my-cool-app"}, service: "web"},
		{app: state.App{ID: "id3", Name: "webapp"}, service: "web-app"},
	}
	candidates := make(map[string]accessTarget, len(apps))
	for _, tc := range apps {
		candidates[accessRouterName(tc.app.Name, tc.service)] = tc
	}

	// websecure 路由（443）与 web 路由（80）都反解到同一归属。
	for _, router := range []string{
		"fleetly-webapp-web-web@http",
		"fleetly-webapp-web-websecure@http",
	} {
		got, ok := accessRouteOf(router, candidates)
		if !ok || got.app.Name != "webapp" || got.service != "web" || got.app.ID != "id1" {
			t.Fatalf("route(%q) = %+v ok=%v, want webapp/web", router, got, ok)
		}
	}
	// 歧义消解：app 与 service 都含 '-'，切分不唯一——候选匹配给出唯一解。
	got, ok := accessRouteOf("fleetly-my-cool-app-web-web@http", candidates)
	if !ok || got.app.ID != "id2" || got.service != "web" {
		t.Fatalf("ambiguous route resolved to %+v ok=%v, want my-cool-app/web", got, ok)
	}
	got, ok = accessRouteOf("fleetly-webapp-web-app-websecure@http", candidates)
	if !ok || got.app.ID != "id3" || got.service != "web-app" {
		t.Fatalf("ambiguous route resolved to %+v ok=%v, want webapp/web-app", got, ok)
	}
	// 平台路由段（registry/fallback）不在候选集：跳过。
	for _, router := range []string{"fleetly-registry-web@http", "fleetly-fallback@http"} {
		if _, ok := accessRouteOf(router, candidates); ok {
			t.Fatalf("platform route %q must not resolve to an app", router)
		}
	}
}

func TestAccessEntryOfBuildsSummaryAndRedacts(t *testing.T) {
	at := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	acc, ok := accessLineOf(accessLineJSON("fleetly-webapp-web-websecure@http", "GET", "200",
		"app.example.com", "/api/items?token=secret-token-12345", "10.216.0.5:53112", 1_500_000, at))
	if !ok {
		t.Fatal("precondition: access line must parse")
	}
	red := &redactor{values: []string{"secret-token-12345"}}
	target := accessTarget{app: state.App{ID: "id1", Name: "webapp"}, service: "web"}
	e := accessEntryOf(acc, target, at, red)

	if e.App != "webapp" || e.Service != "web" || e.Source != SourceAccess {
		t.Fatalf("entry = %+v", e)
	}
	if !e.At.Equal(at) {
		t.Fatalf("at = %v, want %v", e.At, at)
	}
	// 紧凑摘要形态（设计 §3.2：METHOD STATUS HOST PATH duration；redactor
	// 只替换值子串——query 参数名保留，值以 *** 呈现）。
	if want := "GET 200 app.example.com /api/items?token=*** 1ms"; e.Line != want {
		t.Fatalf("summary = %q, want %q", e.Line, want)
	}
	if e.Fields[FieldMethod] != "GET" || e.Fields[FieldStatus] != "200" ||
		e.Fields[FieldRoute] != "fleetly-webapp-web-websecure@http" ||
		e.Fields[FieldDurationMS] != "1" || e.Fields[FieldClientIP] != "10.216.0.5" {
		t.Fatalf("fields = %v", e.Fields)
	}
	// host/path 字段过同一 redactor（URL query 凭证不落明文）。
	if e.Fields[FieldPath] != "/api/items?token=***" {
		t.Fatalf("path field = %q, want redacted", e.Fields[FieldPath])
	}
	if e.Fields[FieldHost] != "app.example.com" {
		t.Fatalf("host field = %q", e.Fields[FieldHost])
	}
}

// TestAccessEntryFallbacks 容忍缺省字段的行（RequestURI 缺失回退
// RequestPath；At 缺失回退 StartUTC；ClientAddr 无端口原样取）。
func TestAccessEntryFallbacks(t *testing.T) {
	raw := `{"DownstreamStatus":404,"RequestPath":"/missing.png","RequestMethod":"POST",` +
		`"RouterName":"fleetly-webapp-web-web@http","StartUTC":"2026-09-21T08:00:05Z",` +
		`"ClientAddr":"10.0.0.9"}`
	acc, ok := accessLineOf(raw)
	if !ok {
		t.Fatal("access line not recognized")
	}
	red := &redactor{}
	target := accessTarget{app: state.App{ID: "id1", Name: "webapp"}, service: "web"}
	e := accessEntryOf(acc, target, time.Time{}, red) // 底座无时间戳 → StartUTC 兜底
	if want := time.Date(2026, 9, 21, 8, 0, 5, 0, time.UTC); !e.At.Equal(want) {
		t.Fatalf("at = %v, want StartUTC %v", e.At, want)
	}
	if e.Fields[FieldPath] != "/missing.png" {
		t.Fatalf("path = %q, want RequestPath fallback", e.Fields[FieldPath])
	}
	if e.Fields[FieldClientIP] != "10.0.0.9" {
		t.Fatalf("client ip = %q", e.Fields[FieldClientIP])
	}
	if !strings.Contains(e.Line, "404") || !strings.Contains(e.Line, "/missing.png") {
		t.Fatalf("summary = %q", e.Line)
	}
}

// makeSucceededDeployment 走合法链把一次部署推进到 succeeded（state 转移
// 表纪律——测试不做旁路写）。
func makeSucceededDeployment(t *testing.T, st *state.Store, appID, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateDeployment(ctx, state.DeployRecord{ID: id, AppID: appID, Kind: "deploy"}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	prev := state.DeployQueued
	for _, next := range []state.DeploymentStatus{
		state.DeployPreparing, state.DeployBuilding, state.DeployReleasing,
		state.DeployObserving, state.DeploySucceeded,
	} {
		n, p := next, prev
		if err := st.UpdateDeployment(ctx, id, state.DeploymentPatch{Status: &n, PrevStatus: &p}); err != nil {
			t.Fatalf("transition %s -> %s: %v", prev, next, err)
		}
		prev = next
	}
}

// TestPollAccessCollectsIntoIngesterOnly Manager 级端到端：访问行识别 +
// 反解 + 脱敏 + 归因 → 只进批量器；非访问行/未归属行跳过并计数；ring 零
// 感知；游标推进到本轮最末行。
func TestPollAccessCollectsIntoIngesterOnly(t *testing.T) {
	mg, port, st, box := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fb := &fakeBackend{}
	mg.WithIngestBackend(fb)
	if err := st.SaveLogsSettings(ctx, "victorialogs", state.LogsSaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("save backend: %v", err)
	}
	mg.refreshBackendGate(ctx)

	app, err := st.CreateApp(ctx, "", "webapp")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	port.setApp("webapp", "web")
	makeSucceededDeployment(t, st, app.ID, "dep-acc-1")

	const secret = "secret-token-12345"
	ct, err := box.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := st.SetAppEnv(ctx, app.ID, "TOKEN", string(ct), "platform"); err != nil {
		t.Fatalf("SetAppEnv: %v", err)
	}

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	mg.WithClock(func() time.Time { return base })
	hitAt := base.Add(2 * time.Millisecond)
	port.emit("fleetly-ingress",
		// 运行日志（文本形态）——跳过。
		substrate.LogLine{At: base.Add(time.Millisecond), Line: `level=info msg="Configuration loaded"`},
		// 合法访问行（命中 webapp/web，443 路由；query 带凭证）。
		substrate.LogLine{At: hitAt, Line: accessLineJSON("fleetly-webapp-web-websecure@http", "GET", "200",
			"app.example.com", "/api/items?token="+secret, "10.216.0.5:53112", 1_500_000, hitAt)},
		// 缺 RouterName 的 JSON——跳过。
		substrate.LogLine{At: base.Add(3 * time.Millisecond), Line: `{"level":"INFO","msg":"x"}`},
		// 平台路由段（registry）——无归属，跳过。
		substrate.LogLine{At: base.Add(4 * time.Millisecond), Line: accessLineJSON("fleetly-registry-web@http", "GET", "200",
			"registry.example.com", "/v2/", "10.216.0.7:40000", 900_000, base)},
	)

	// 直播订阅先行：访问行绝不进 ring（FollowLogs 零改动的结构保证）。
	ch, stop := mg.Follow(ctx, "webapp", "web")
	defer stop()

	mg.scanOnce(ctx)
	mg.ing.flushTick(ctx)

	batches, _ := fb.snapshot()
	var accessRows []Entry
	for _, b := range batches {
		for _, e := range b {
			if e.Source == SourceAccess {
				accessRows = append(accessRows, e)
			}
		}
	}
	if len(accessRows) != 1 {
		t.Fatalf("access rows = %d, want exactly 1 (batches: %d)", len(accessRows), len(batches))
	}
	row := accessRows[0]
	if row.App != "webapp" || row.Service != "web" {
		t.Fatalf("attribution = %s/%s, want webapp/web", row.App, row.Service)
	}
	if row.Fields[FieldDeploymentID] != "dep-acc-1" {
		t.Fatalf("deployment_id = %q, want dep-acc-1", row.Fields[FieldDeploymentID])
	}
	// 摘要与 path 字段已脱敏（query 里的凭证不落明文；secret 值集 = 该 app
	// env 已知值——脱敏后以 *** 呈现）。
	if strings.Contains(row.Line, secret) || strings.Contains(row.Fields[FieldPath], secret) {
		t.Fatalf("query credential leaked unredacted: line=%q path=%q", row.Line, row.Fields[FieldPath])
	}
	if !strings.Contains(row.Line, "***") || row.Fields[FieldPath] != "/api/items?token=***" {
		t.Fatalf("redaction marker missing: line=%q path=%q", row.Line, row.Fields[FieldPath])
	}

	// 跳过计数：文本运行日志 + 缺 RouterName JSON + registry 未归属 = 3。
	mg.mu.Lock()
	skipped := mg.accSkipped
	mg.mu.Unlock()
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}

	// ring 零感知。
	select {
	case e := <-ch:
		t.Fatalf("access line leaked into the ring: %+v", e)
	case <-time.After(100 * time.Millisecond):
	}

	// 游标推进到本轮最末行（含跳过行）。
	mg.mu.Lock()
	since := mg.accSince
	mg.mu.Unlock()
	if !since.Equal(base.Add(4 * time.Millisecond)) {
		t.Fatalf("cursor = %v, want last line at %v", since, base.Add(4*time.Millisecond))
	}
}

// TestPollAccessGateSkipsOnJSONL 采集开关：backend=jsonl 时访问日志不采集
//（无消费方——与 JSONL 边界一致；游标锚点也不建立）。
func TestPollAccessGateSkipsOnJSONL(t *testing.T) {
	mg, port, st, _ := newTestManager(t)
	ctx := context.Background()

	fb := &fakeBackend{}
	mg.WithIngestBackend(fb)
	if err := st.SaveLogsSettings(ctx, "jsonl", state.LogsSaveOptions{Actor: "system"}); err != nil {
		t.Fatalf("save backend: %v", err)
	}
	mg.refreshBackendGate(ctx)
	if mg.vlEnabled() {
		t.Fatal("precondition: jsonl backend must close the ingest gate")
	}

	if _, err := st.CreateApp(ctx, "", "webapp"); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	port.setApp("webapp", "web")
	base := time.Now().Add(-time.Hour)
	mg.WithClock(func() time.Time { return base })
	port.emit("fleetly-ingress", substrate.LogLine{At: base.Add(time.Millisecond),
		Line: accessLineJSON("fleetly-webapp-web-web@http", "GET", "200", "h", "/", "1.2.3.4:5", 1_000, base)})

	mg.scanOnce(ctx)
	batches, _ := fb.snapshot()
	total := 0
	for _, b := range batches {
		total += len(b)
	}
	if total != 0 || mg.ing.Pending() != 0 {
		t.Fatalf("access lines collected in jsonl mode: batches=%d pending=%d", total, mg.ing.Pending())
	}
	mg.mu.Lock()
	started, skipped := mg.accStarted, mg.accSkipped
	mg.mu.Unlock()
	if started {
		t.Fatal("access cursor anchor must not be created in jsonl mode")
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d in jsonl mode, want untouched 0", skipped)
	}
}

// TestDepAttributorTTLCache 部署归因缓存：TTL 窗内一次查询；窗过期重查；
// 查询失败不缓存（下轮重试）；空结果同样进缓存（从未成功部署的 app 不重
// 复查库）。
func TestDepAttributorTTLCache(t *testing.T) {
	d := newDepAttributor(nil)
	queries := 0
	d.lookup = func(context.Context, string) (string, error) {
		queries++
		switch queries {
		case 1, 2:
			return "dep-1", nil
		case 3:
			return "", nil // 无成功部署（空结果）
		default:
			return "", errors.New("db down")
		}
	}
	now := time.Now()
	d.clock = func() time.Time { return now }

	if id, err := d.get(context.Background(), "app1"); err != nil || id != "dep-1" {
		t.Fatalf("get = %q, %v", id, err)
	}
	// 窗内：命中缓存。
	now = now.Add(depAttributionTTL - time.Second)
	if _, err := d.get(context.Background(), "app1"); err != nil {
		t.Fatalf("cached get: %v", err)
	}
	if queries != 1 {
		t.Fatalf("queries = %d within TTL, want 1", queries)
	}
	// 窗过期：重查。
	now = now.Add(2 * time.Second)
	if _, err := d.get(context.Background(), "app1"); err != nil {
		t.Fatalf("get after ttl: %v", err)
	}
	if queries != 2 {
		t.Fatalf("queries = %d after TTL, want 2", queries)
	}
	// 空结果（无成功部署）进缓存。
	now = now.Add(depAttributionTTL + time.Second)
	id, err := d.get(context.Background(), "app1")
	if err != nil || id != "" {
		t.Fatalf("empty-result get = %q, %v", id, err)
	}
	now = now.Add(depAttributionTTL - time.Second)
	if _, err := d.get(context.Background(), "app1"); err != nil {
		t.Fatalf("cached empty get: %v", err)
	}
	if queries != 3 {
		t.Fatalf("queries = %d (empty result must be cached), want 3", queries)
	}
	// 失败不缓存：立即重试即重查。
	now = now.Add(depAttributionTTL + time.Second)
	if _, err := d.get(context.Background(), "app1"); err == nil {
		t.Fatal("expected lookup error")
	}
	if queries != 4 {
		t.Fatalf("queries = %d after failure, want 4", queries)
	}
	now = now.Add(time.Second)
	if _, err := d.get(context.Background(), "app1"); err == nil {
		t.Fatal("expected repeated lookup error")
	}
	if queries != 5 {
		t.Fatalf("failed lookup must not be cached: queries = %d, want 5", queries)
	}
}
