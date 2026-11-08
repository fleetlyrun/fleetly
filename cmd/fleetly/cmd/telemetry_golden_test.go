package cmd

// Telemetry 动词 golden（events list / logs——真 RPC 链，事件 seq 确定性：
// 夹具 deploy 流的 outbox 事件按固定序产生；logs --text 检索路径双形态
// ADR-0040）。

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestGoldenTelemetryVerbs(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	steps := []struct {
		verb string
		args []string
	}{
		{"events list", []string{"events", "list"}},
		{"logs", []string{"logs", "--app", appID}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			code, out, stderr := runCLI(t, st.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			code, out, stderr = runCLI(t, append(st.args, "--json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// TestGoldenEventsListTailWindow 钉 events list 尾窗缺省（staging 记录·
// 六十二立案①的根因形态）：outbox 越过缺省 limit（100 行）后，裸
// `events list` 必须展示最新窗口——旧头窗缺省（after-seq=0）在此形态下
// 恒定返回头部 100 行，在 staging 被误诊为「事件流停摆在 seq 100」
// （写路径/服务端读路径全程健康，journal 每拍 ListEvents 皆毫秒级成功）。
// 断言三钉：头部行缺席、尾部行在场、截断可见（提示行）；显式
// --after-seq 0 的头窗翻页语义不受缺省变更影响。golden 双形态同批。
func TestGoldenEventsListTailWindow(t *testing.T) {
	h := newGoldenHarness(t)
	seedOutboxRows(t, h, 105)

	code, out, stderr := runCLI(t, "events", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("events list: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "probe-105") || strings.Contains(out, "probe-001") {
		t.Fatalf("default window must be the newest events, not the frozen head window:\n%s", out)
	}
	if !strings.Contains(out, "older events not shown") {
		t.Fatalf("head truncation must be visible, not silent (the staging misdiagnosis hinged on it):\n%s", out)
	}
	compareGolden(t, goldenFile("events-list-tail"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "events", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("events list --json: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, `"seq": "105"`) || strings.Contains(out, `"seq": "1",`) {
		t.Fatalf("json tail window must carry the newest events:\n%s", out)
	}
	compareGolden(t, goldenFile("events-list-tail")+"-json", normalizeGolden(out))

	// 显式游标零值 = 保留窗头部起翻页（脚本/翻页语义不变）。
	code, out, stderr = runCLI(t, "events", "list", "--after-seq", "0", "--limit", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("events list --after-seq 0: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "probe-001") || !strings.Contains(out, "probe-003") {
		t.Fatalf("explicit head paging must keep working:\n%s", out)
	}
}

// seedOutboxRows 直插 n 行合成 outbox 事件（seq 1..n；确定性时间戳/载荷/
// 行 ID——事件名走注册表在册的 volume.created，team 轴 default）。绕经
// repo.Append 只为免注册面，夹具行仍与生产同表同列。
func seedOutboxRows(t *testing.T, h *apitest.Harness, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		createdAt := fmt.Sprintf("2026-01-01T00:%02d:%02dZ", (i-1)/60, (i-1)%60)
		if _, err := h.DB.Runner().ExecContext(context.Background(),
			`INSERT INTO outbox (name, aggregate, aggregate_id, payload, created_at, team_id)
			 VALUES ('volume.created', 'volume', ?, ?, ?, 'default')`,
			fmt.Sprintf("probe-%03d", i), fmt.Sprintf(`{"probe":%d}`, i), createdAt); err != nil {
			t.Fatalf("seed outbox row %d: %v", i, err)
		}
	}
}

// TestGoldenLogsTextSearch 钉检索路径（ADR-0040 决策 4）：--text 切换
// Logging.Query（行级隔离由查询构造承载——断言面经 FakeLogging.Queries）。
func TestGoldenLogsTextSearch(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)
	fl := &apitest.FakeLogging{}
	h.Services.Logging = fl

	code, out, stderr := runCLI(t, "logs", "--app", appID, "--text", "frame")
	if code != 0 || stderr != "" {
		t.Fatalf("logs --text: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-text"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "logs", "--app", appID, "--text", "frame", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("logs --text --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-text")+"-json", normalizeGolden(out))

	// 双径路由锚：检索查询携带本 App 域字段 + 文本过滤（行级隔离构造面）。
	queries := fl.Queries()
	if len(queries) != 2 {
		t.Fatalf("expected 2 routed search queries, got %d", len(queries))
	}
	for _, q := range queries {
		if q.Text != "frame" || q.Namespace.App != appID {
			t.Fatalf("search query must carry the app namespace and text filter, got %+v", q)
		}
	}
}
