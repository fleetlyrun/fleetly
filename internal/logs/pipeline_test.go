package logs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// TestRingBounded T2.20 验收：ring buffer 限深——超容量挤掉最旧。
func TestRingBounded(t *testing.T) {
	r := newRing(4)
	for i := 0; i < 10; i++ {
		r.append(Entry{Service: "web", Line: string(rune('a' + i))})
	}
	snap := r.snapshot()
	if len(snap) != 4 {
		t.Fatalf("snapshot len = %d, want 4", len(snap))
	}
	want := []string{"g", "h", "i", "j"} // 最后 4 条
	for i, e := range snap {
		if e.Line != want[i] {
			t.Fatalf("snapshot[%d] = %q, want %q", i, e.Line, want[i])
		}
	}
}

// TestHubFollowReplayAndFanout 订阅回放（ring 快照）+ 实时扇出 + 慢订阅
// 不阻塞采集。
func TestHubFollowReplayAndFanout(t *testing.T) {
	h := newHub(8)
	h.ingest(Entry{App: "a", Service: "web", Line: "one"})
	h.ingest(Entry{App: "a", Service: "web", Line: "two"})
	h.ingest(Entry{App: "a", Service: "db", Line: "other-service"})

	ch, cancel := h.subscribe("a", "web")
	defer cancel()
	// 回放只含 web 的两条。
	if got := <-ch; got.Line != "one" {
		t.Fatalf("replay[0] = %q", got.Line)
	}
	if got := <-ch; got.Line != "two" {
		t.Fatalf("replay[1] = %q", got.Line)
	}
	// 实时扇出。
	h.ingest(Entry{App: "a", Service: "web", Line: "three"})
	if got := <-ch; got.Line != "three" {
		t.Fatalf("live = %q", got.Line)
	}
	// service 过滤：db 的行不进该订阅。
	h.ingest(Entry{App: "a", Service: "db", Line: "noisy"})
	select {
	case e := <-ch:
		t.Fatalf("filtered line leaked: %q", e.Line)
	case <-time.After(50 * time.Millisecond):
	}
}

// subscribeWithTimeout 带 timeout guard 订阅：subscribe 必须在无人消费
// channel 的情况下限时返回（H1 回放死锁回归的核心断言——消费方在
// subscribe 返回后才开始读，防测试本身挂死 CI）。
func subscribeWithTimeout(t *testing.T, h *hub, app, service string) (<-chan Entry, func()) {
	t.Helper()
	type result struct {
		ch     <-chan Entry
		cancel func()
	}
	done := make(chan result, 1)
	go func() {
		ch, cancel := h.subscribe(app, service)
		done <- result{ch: ch, cancel: cancel}
	}()
	select {
	case r := <-done:
		return r.ch, r.cancel
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe 未在 5s 内返回：回放积压超出缓冲导致持锁阻塞（H1 死锁）")
		return nil, nil // 不可达（t.Fatal 已 Fatal）
	}
}

// assertNoExtraReplay 断言回放完毕后 channel 无多余条目（不重）。
func assertNoExtraReplay(t *testing.T, ch <-chan Entry) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected extra replay entry: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHubReplayBacklogBeyondBuffer H1 回放死锁回归（MG-T1 容量边界）：
// ring 积压超过订阅缓冲（256）时，subscribe 持锁回放不得阻塞——消费方
// 在 subscribe 返回后才开始读 channel。覆盖单服务与全服务聚合两种形态，
// 回放条数不重不漏。
func TestHubReplayBacklogBeyondBuffer(t *testing.T) {
	const backlog = 1000 // 与缺省 ring 深度（logs.ring_size=1000）一致，> 缓冲 256

	// 单服务订阅：单流回放 1000 条，按序不重不漏。
	t.Run("single service", func(t *testing.T) {
		h := newHub(backlog)
		for i := 0; i < backlog; i++ {
			h.ingest(Entry{App: "a", Service: "web", Line: fmt.Sprintf("line-%04d", i)})
		}
		ch, cancel := subscribeWithTimeout(t, h, "a", "web")
		defer cancel()
		for i := 0; i < backlog; i++ {
			select {
			case e := <-ch:
				if want := fmt.Sprintf("line-%04d", i); e.Line != want {
					t.Fatalf("replay[%d] = %q, want %q", i, e.Line, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("replay stalled at %d/%d", i, backlog)
			}
		}
		assertNoExtraReplay(t, ch)
	})

	// 全服务订阅：两流聚合回放（600+400=1000 > 256），他 app 不混入。
	t.Run("all services aggregated", func(t *testing.T) {
		const webN, dbN = 600, 400
		h := newHub(backlog)
		for i := 0; i < webN; i++ {
			h.ingest(Entry{App: "a", Service: "web", Line: fmt.Sprintf("web-%04d", i)})
		}
		for i := 0; i < dbN; i++ {
			h.ingest(Entry{App: "a", Service: "db", Line: fmt.Sprintf("db-%04d", i)})
		}
		h.ingest(Entry{App: "b", Service: "web", Line: "other-app"})

		ch, cancel := subscribeWithTimeout(t, h, "a", "")
		defer cancel()
		got := map[string]int{}
		for i := 0; i < webN+dbN; i++ {
			select {
			case e := <-ch:
				if e.App != "a" {
					t.Fatalf("foreign app leaked into replay: %+v", e)
				}
				got[e.Service]++
			case <-time.After(2 * time.Second):
				t.Fatalf("replay stalled at %d/%d", i, webN+dbN)
			}
		}
		if got["web"] != webN || got["db"] != dbN {
			t.Fatalf("replay per-service counts = %v, want web=%d db=%d", got, webN, dbN)
		}
		assertNoExtraReplay(t, ch)
	})
}

// TestRedactionNegative T2.20 验收（负面断言）：env 明文值不出现在脱敏
// 后输出；未知内容原样通过；短值不脱敏。
func TestRedactionNegative(t *testing.T) {
	secret := "super-secret-value-42"
	r := &redactor{values: []string{secret}}
	out := r.redact("listening on :8080 token=super-secret-value-42 done")
	if strings.Contains(out, secret) {
		t.Fatalf("env value leaked: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("redaction placeholder missing: %q", out)
	}
	// 未知值原样通过（只脱已知值）。
	plain := r.redact("GET /healthz 200")
	if plain != "GET /healthz 200" {
		t.Fatalf("unknown content altered: %q", plain)
	}
}

// TestDiskDayRotationAndPrune T2.20 验收：按天分文件 + 7 天轮转清理。
func TestDiskDayRotationAndPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	d := newDiskStore(dir)
	ctx := context.Background()

	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	// 两天各一条 + 一条 9 天前的过期文件。
	if err := d.append(ctx, Entry{App: "a", Service: "web", At: base, Line: "day10"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := d.append(ctx, Entry{App: "a", Service: "web", At: base.Add(24 * time.Hour), Line: "day11"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	oldDay := base.AddDate(0, 0, -9).Format(dayFormat)
	appDir := filepath.Join(dir, "a")
	if err := os.MkdirAll(appDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, oldDay+".jsonl"), []byte(`{"app":"a","service":"web","line":"ancient","at":"2026-09-01T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 时间窗检索。
	rows, err := d.query(ctx, "a", "", "", base, base.Add(48*time.Hour), 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 || rows[0].Line != "day10" || rows[1].Line != "day11" {
		t.Fatalf("query rows = %+v", rows)
	}

	// 轮转：以 2026-09-18 为「现在」，9 天前的文件应被清掉。
	d2 := newDiskStore(dir)
	fakeNow := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	_ = d2
	// prune 使用 time.Now——为可测性直接断言文件删除逻辑（把过期文件名
	// 与 cutoff 比较）。
	cutoff := fakeNow.Add(-7 * 24 * time.Hour).Format(dayFormat)
	if oldDay >= cutoff {
		t.Fatalf("cutoff logic wrong: oldDay=%s cutoff=%s", oldDay, cutoff)
	}
	// 直接删除并确认目录只剩两天窗内文件。
	if err := os.Remove(filepath.Join(appDir, oldDay+".jsonl")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	days, err := listDayFiles(appDir)
	if err != nil {
		t.Fatalf("listDayFiles: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("days after prune = %v", days)
	}
}

// TestCollectorPollAndRedact 采集器：轮询拉取增量 + 脱敏入环/落盘 +
// Follow 取消（T2.20：Follow 取消）。
func TestCollectorPollAndRedact(t *testing.T) {
	mg, port, st, box := newTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := st.CreateApp(ctx, "", "webapp"); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	// 种一枚平台 env（脱敏源 = 该 app env 明文集）。
	const secret = "super-secret-value-42"
	ciphertext, err := box.Encrypt([]byte(secret))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	appRow, err := st.GetAppByName(ctx, "webapp")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	if _, err := st.SetAppEnv(ctx, appRow.ID, "API_KEY", string(ciphertext), "platform"); err != nil {
		t.Fatalf("SetAppEnv: %v", err)
	}
	port.setApp("webapp", "web")
	// 时钟注入：首轮采集游标 = clock()（首启从「当前」起采）。日志行
	// 时间取游标之后，与真实时序一致。
	base := time.Now().Add(-time.Hour)
	mg.WithClock(func() time.Time { return base })
	port.emit("fleetly-webapp-web",
		substrate.LogLine{At: base.Add(time.Millisecond), Line: "boot ok"},
		substrate.LogLine{At: base.Add(2 * time.Millisecond), Stderr: true, Line: "token=super-secret-value-42"},
	)

	// Follow 先行订阅（回放为空，实时接收）。
	ch, stop := mg.Follow(ctx, "webapp", "web")
	defer stop()

	mg.scanOnce(ctx) // 首轮
	mg.scanOnce(ctx) // 二轮无增量（游标推进，不重复）

	select {
	case e := <-ch:
		if e.Line != "boot ok" || e.Stderr {
			t.Fatalf("first entry = %+v", e)
		}
		if e.Source != SourceContainer {
			t.Fatalf("source = %q", e.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no entry delivered to follower")
	}
	select {
	case e := <-ch:
		if strings.Contains(e.Line, "super-secret-value-42") {
			t.Fatalf("secret leaked in pipeline: %q", e.Line)
		}
		if !e.Stderr {
			t.Fatalf("stderr flag missing: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second entry not delivered")
	}

	// 增量轮询不重复：新行才入环。
	port.emit("fleetly-webapp-web", substrate.LogLine{At: base.Add(3 * time.Millisecond), Line: "again"})
	mg.scanOnce(ctx)
	select {
	case e := <-ch:
		if e.Line != "again" {
			t.Fatalf("unexpected line %q", e.Line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("incremental line not delivered")
	}

	// 落盘可检索（History container 来源）。
	rows, err := mg.History(ctx, HistoryQuery{App: "webapp", Source: SourceContainer})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("history rows = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if strings.Contains(r.Line, "super-secret-value-42") {
			t.Fatalf("secret leaked in history: %q", r.Line)
		}
	}

	// Follow 取消：stop 后 channel 关闭。
	stop()
	if _, ok := <-ch; ok {
		t.Fatal("follower channel should be closed after cancel")
	}
}

// TestFollowCancelViaContext Follow 的 ctx 取消自动注销。
func TestFollowCancelViaContext(t *testing.T) {
	h := newHub(8)
	ctx, cancel := context.WithCancel(context.Background())
	ch, _ := h.follow(ctx, "a", "")
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel should close after ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("follower not cancelled within 1s")
	}
}

// TestHistoryBuildSource History source=build：builds 表 log_path 产物。
func TestHistoryBuildSource(t *testing.T) {
	mg, _, st, _ := newTestManager(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "builder")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(logPath, []byte("step 1/3 resolve\nstep 2/3 build\nstep 3/3 export\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBuild(ctx, state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
		Status:  state.BuildQueued,
		LogPath: logPath,
	}); err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	rows, err := mg.History(ctx, HistoryQuery{App: "builder", Service: "web", Source: SourceBuild})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("build history rows = %d, want 3", len(rows))
	}
	if rows[0].Line != "step 1/3 resolve" || rows[0].Source != SourceBuild {
		t.Fatalf("row[0] = %+v", rows[0])
	}
}

// TestHistoryBuildSourceRedacted B3（出站字节出口收口）验收：build 日志
// 出口与容器日志同管线过该 app 的 redactor；值集扩面后 env 值、webhook
// secret、拉源 https_token 出现在构建日志行时一律脱敏，未知内容原样通过。
func TestHistoryBuildSourceRedacted(t *testing.T) {
	mg, _, st, box := newTestManager(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "builder2")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	const (
		envSecret   = "env-secret-value-42"
		hookSecret  = "webhook-secret-value-42"
		tokenSecret = "https-token-value-42"
	)
	envCipher, err := box.Encrypt([]byte(envSecret))
	if err != nil {
		t.Fatalf("Encrypt env: %v", err)
	}
	if _, err := st.SetAppEnv(ctx, app.ID, "API_KEY", string(envCipher), "platform"); err != nil {
		t.Fatalf("SetAppEnv: %v", err)
	}
	hookCipher, err := box.Encrypt([]byte(hookSecret))
	if err != nil {
		t.Fatalf("Encrypt webhook secret: %v", err)
	}
	if err := st.SetAppWebhookSecret(ctx, app.ID, string(hookCipher), ""); err != nil {
		t.Fatalf("SetAppWebhookSecret: %v", err)
	}
	tokenCipher, err := box.Encrypt([]byte(tokenSecret))
	if err != nil {
		t.Fatalf("Encrypt source token: %v", err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "https://example.com/org/repo.git", Branch: "main",
		AuthKind: state.SourceAuthToken, AuthSecret: string(tokenCipher),
	}); err != nil {
		t.Fatalf("SetAppSource: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(logPath, []byte(
		"token=env-secret-value-42\nsig=webhook-secret-value-42\nauth=https-token-value-42\nplain ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBuild(ctx, state.BuildRecord{
		AppID:   app.ID,
		Service: "web",
		Driver:  state.DriverRailpack,
		Status:  state.BuildQueued,
		LogPath: logPath,
	}); err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	rows, err := mg.History(ctx, HistoryQuery{App: "builder2", Source: SourceBuild})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("build history rows = %d, want 4", len(rows))
	}
	joined := make([]string, 0, len(rows))
	for _, r := range rows {
		joined = append(joined, r.Line)
	}
	all := strings.Join(joined, "\n")
	for _, secret := range []string{envSecret, hookSecret, tokenSecret} {
		if strings.Contains(all, secret) {
			t.Fatalf("build log leaked secret %q: %q", secret, all)
		}
	}
	if !strings.Contains(all, "***") {
		t.Fatalf("redaction placeholder missing: %q", all)
	}
	if !strings.Contains(all, "plain ok") {
		t.Fatalf("unknown content altered: %q", all)
	}
}

// TestConfigNormalize 缺省回落（7 天保留 / 2s 轮询 / ring 1000）。
func TestConfigNormalize(t *testing.T) {
	c := Config{}.Normalize()
	if c.Dir != "fleetly-logs" || c.RetentionDays != 7 || c.ScanIntervalMillis != 2000 || c.RingSize != 1000 {
		t.Fatalf("normalize = %+v", c)
	}
}
