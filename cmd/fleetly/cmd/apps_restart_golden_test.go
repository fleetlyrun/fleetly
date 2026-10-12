package cmd

// apps restart golden（ADR-0069）：进程重启动词双形态——已知进程的人类
// 行式回执 + --json 空体 protojson；未知进程精确 E_NOT_FOUND 错误信封
// （k3s 驱逐对零命中静默，CLI 面钉死引擎侧冻结体校验的诚实拒绝）。

import (
	"strings"
	"testing"
)

func TestGoldenAppsRestart(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, out, stderr := runCLI(t, "apps", "restart", "--app", appID, "--process", "web")
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-restart"), normalizeGolden(out))

	// --json：空响应体的 protojson 形态。
	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--process", "web", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-restart")+"-json", normalizeGolden(out))
}

func TestGoldenAppsRestartUnknownProcess(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, out, stderr := runCLI(t, "apps", "restart", "--app", appID, "--process", "wrker")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("apps restart (unknown process): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-unknown"), normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--process", "wrker", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("apps restart --json (unknown process): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-unknown")+"-json", normalizeGolden(stderr))
}
