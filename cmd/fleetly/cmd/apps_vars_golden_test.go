package cmd

// apps vars 三动词 golden（ADR-0072 三级合成顶层）：list/set/unset 双形态
// + set 的"重部署生效"回执提示 + unset 回落语义 + deploy --override-hook
// 受理形态。

import (
	"strings"
	"testing"
)

func TestGoldenAppsVars(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	// 组清单（coverage 守卫面：嵌套组也是动词，双形态 golden 钉死）。
	code, out, stderr := runCLI(t, "apps", "vars")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars"), normalizeGolden(out))
	code, outJSON, stderr := runCLI(t, "apps", "vars", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars")+"-json", normalizeGolden(outJSON))

	// set → list → unset 全链。
	code, out, stderr = runCLI(t, "apps", "vars", "set", "--app", appID, "--value", "edge", "TIER")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars set: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "redeploy") {
		t.Fatalf("apps vars set: expected the redeploy hint, got %q", out)
	}
	compareGolden(t, goldenFile("apps vars set"), normalizeGolden(out))
	code, outJSON, stderr = runCLI(t, "apps", "vars", "set", "--app", appID, "--value", "edge", "TIER", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars set --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars set")+"-json", normalizeGolden(outJSON))

	code, out, stderr = runCLI(t, "apps", "vars", "list", "--app", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars list: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "TIER") {
		t.Fatalf("apps vars list: expected the TIER row, got %q", out)
	}
	compareGolden(t, goldenFile("apps vars list"), normalizeGolden(out))
	code, outJSON, stderr = runCLI(t, "apps", "vars", "list", "--app", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars list")+"-json", normalizeGolden(outJSON))

	code, out, stderr = runCLI(t, "apps", "vars", "unset", "--app", appID, "TIER")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars unset: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars unset"), normalizeGolden(out))
	// json 形态用重设后的第二把（首把已删，再删自然 E_NOT_FOUND）。
	if code, _, stderr := runCLI(t, "apps", "vars", "set", "--app", appID, "--value", "edge", "TIER"); code != 0 || stderr != "" {
		t.Fatalf("apps vars set (reseed): code=%d stderr=%q", code, stderr)
	}
	code, outJSON, stderr = runCLI(t, "apps", "vars", "unset", "--app", appID, "TIER", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps vars unset --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps vars unset")+"-json", normalizeGolden(outJSON))

	// unset 不存在的名：E_NOT_FOUND（诚实上抛）。
	if code, _, stderr := runCLI(t, "apps", "vars", "unset", "--app", appID, "NOPE"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("apps vars unset (unknown name): code=%d stderr=%q (want not found)", code, stderr)
	}
}

func TestGoldenDeployOverrideHook(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	// 无 hook 的应用：override-hook 旗标随行受理（幂等面覆盖）。
	code, out, stderr := runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.28", "--override-hook")
	if code != 0 || stderr != "" {
		t.Fatalf("deploy --override-hook: code=%d stderr=%q out=%q", code, stderr, out)
	}
	compareGolden(t, goldenFile("deploy override-hook"), normalizeGolden(out))
	code, outJSON, stderr := runCLI(t, "deploy", "--app", appID, "--image", "nginx:1.28", "--override-hook", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("deploy --override-hook --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("deploy override-hook")+"-json", normalizeGolden(outJSON))
}
