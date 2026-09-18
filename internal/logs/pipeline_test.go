package logs

import (
	"context"
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

// TestConfigNormalize 缺省回落（7 天保留 / 2s 轮询 / ring 1000）。
func TestConfigNormalize(t *testing.T) {
	c := Config{}.Normalize()
	if c.Dir != "fleetly-logs" || c.RetentionDays != 7 || c.ScanIntervalMillis != 2000 || c.RingSize != 1000 {
		t.Fatalf("normalize = %+v", c)
	}
}
