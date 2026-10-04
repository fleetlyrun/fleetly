package cmd

// Telemetry 动词 golden（events list / logs——真 RPC 链，事件 seq 确定性：
// 夹具 deploy 流的 outbox 事件按固定序产生；logs --text 检索路径双形态
// ADR-0040）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
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
