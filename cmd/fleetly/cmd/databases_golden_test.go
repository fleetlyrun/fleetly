package cmd

// databases 命令组 golden（F1.12，ADR-0029）：create/list/get/delete 双形
// 态；独立项目夹具（含网络——数据库可达性前置），删除后卷与凭证 Secret
// 残留的口径在 apitest 承载。

import (
	"testing"
)

// seedDatabaseProject 建项目 + 网络，返回 projectID。
func seedDatabaseProject(t *testing.T, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "projects", "create", name)
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, _, stderr = runCLI(t, "networks", "create", "--project", projectID, "default")
	if code != 0 || stderr != "" {
		t.Fatalf("seed network: code=%d stderr=%q", code, stderr)
	}
	return projectID
}

// createDatabase 建库并返回 (dbID, 人类形态输出)。
func createDatabase(t *testing.T, projectID, engine, name string, jsonOut bool) (string, string) {
	t.Helper()
	args := []string{"databases", "create", "--project", projectID, "--engine", engine, name}
	if jsonOut {
		args = append(args, "--json")
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("databases create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}

func TestGoldenDatabasesLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbs")

	dbID, out := createDatabase(t, projectID, "postgres", "shop", false)
	compareGolden(t, "databases-create", normalizeGolden(out))
	_, out = createDatabase(t, projectID, "redis", "cache", true)
	compareGolden(t, "databases-create-json", normalizeGolden(out))

	code, out, stderr := runCLI(t, "databases", "list", "--project", projectID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-list", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "list", "--project", projectID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "get", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-get", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "get", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-get-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "databases", "delete", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("databases delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-delete", normalizeGolden(out))

	// --json 删除轮：人类形态另建（id 可提取）再以 --json 删。
	secondID, _ := createDatabase(t, projectID, "postgres", "shop-2", false)
	code, out, stderr = runCLI(t, "databases", "delete", secondID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("databases delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-delete-json", normalizeGolden(out))
}
