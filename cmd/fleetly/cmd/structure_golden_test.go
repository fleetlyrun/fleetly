package cmd

// apps delete golden（ADR-0023 收口删除；独立夹具：主业务流 golden 的
// GOLDEN_APP 不能删——后续步骤还在用）。

import (
	"testing"
)

func TestGoldenAppsDelete(t *testing.T) {
	h := newGoldenHarness(t)

	code, out, stderr := runCLI(t, "projects", "create", "teardown")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)

	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "gone")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	appID := extractTailID(out)

	code, out, stderr = runCLI(t, "apps", "delete", "--app", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-delete", normalizeGolden(out))

	// --json 轮：另建一个 App 删（唯一名）。
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "gone-json")
	if code != 0 || stderr != "" {
		t.Fatalf("seed json app: code=%d stderr=%q", code, stderr)
	}
	jsonAppID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "delete", "--app", jsonAppID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-delete-json", normalizeGolden(out))
	_ = h
}
