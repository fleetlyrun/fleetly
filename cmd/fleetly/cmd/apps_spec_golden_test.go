package cmd

// apps spec golden（IA v3 二期②）：冻结 Spec 只读回读面——部署后非空
// protojson（双形态同构钉死）+ 未部署精确 E_NOT_FOUND。

import (
	"strings"
	"testing"
)

func TestGoldenAppsSpec(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, out, stderr := runCLI(t, "apps", "spec", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps spec: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "processes") {
		t.Fatalf("apps spec: expected a spec payload with processes, got %q", out)
	}
	compareGolden(t, goldenFile("apps-spec"), normalizeGolden(out))

	// --json 同构（Spec 本就是 JSON 边界——双形态输出一致是有意契约）。
	code, outJSON, stderr := runCLI(t, "apps", "spec", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps spec --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-spec")+"-json", normalizeGolden(outJSON))
}

func TestAppsSpecNotDeployed(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "specnd")
	if code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, "bare"); code != 0 || stderr != "" {
		t.Fatalf("apps create: code=%d stderr=%q out=%q", code, stderr, out)
	}
	// 未部署 = 无冻结 Revision → E_NOT_FOUND（诚实：无冻结即无 Spec）。
	if code, _, stderr := runCLI(t, "apps", "spec", "bare"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("apps spec (never deployed): code=%d stderr=%q (want not found)", code, stderr)
	}
}
