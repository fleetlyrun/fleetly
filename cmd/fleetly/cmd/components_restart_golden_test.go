package cmd

// components restart golden（IA v3 二期④）：未知组件的精确 E_NOT_FOUND
// 错误信封双形态（golden harness 无受管自宿 Provider——载体重启的 happy
// 路径由 engine 单测钉路由、staging e2e 钉真机重排）。

import (
	"strings"
	"testing"
)

func TestGoldenComponentsRestartUnknown(t *testing.T) {
	_ = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "components", "restart", "who-am-i")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("components restart (unknown): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, "components-restart", normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "components", "restart", "who-am-i", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("components restart --json (unknown): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, "components-restart-json", normalizeGolden(stderr))
}
