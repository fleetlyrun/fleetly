package cmd

// Telemetry 动词 golden（events list / logs——真 RPC 链，事件 seq 确定性：
// 夹具 deploy 流的 outbox 事件按固定序产生）。

import (
	"testing"
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
