package cmd

// pubsubs 命令组 golden（B4b，ADR-0096 决策 8）：databases 组同构的双形态
// 钉板——create/list/get/update/delete 生命周期、backup/backups/verify 备份
// 面（golden 夹具无执行链：pending 行的拒绝面即确定性输出）、
// download-backup 无对象仓拒绝面、rotate-password 未在服拒绝面。

import (
	"path/filepath"
	"strings"
	"testing"
)

// createPubSub 建实例并返回 (id, 人类形态输出)。
func createPubSub(t *testing.T, projectID, engine, name string, jsonOut bool) (string, string) {
	t.Helper()
	args := []string{"pubsubs", "create", "--project", projectID, "--engine", engine, name}
	if jsonOut {
		args = append(args, "--json")
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}

func TestGoldenPubSubsLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "pubs")

	id, out := createPubSub(t, projectID, "nats", "events", false)
	compareGolden(t, "pubsubs-create", normalizeGolden(out))
	_, out = createPubSub(t, projectID, "nats", "jobs", true)
	compareGolden(t, "pubsubs-create-json", normalizeGolden(out))

	code, out, stderr := runCLI(t, "pubsubs", "list", "--project", projectID)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-list", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "list", "--project", projectID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "get", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-get", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "get", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-get-json", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "get", "--project", "pubs", "events")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs get (by name): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-get-by-name", normalizeGolden(out))

	// update：golden 夹具实例恒 pending（无 Runtime 观测）——在服门拒绝面
	// 即确定性输出（E_CONFLICT；成功面在 apitest 承载）。
	code, _, stderr = runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=2gb", id)
	if code == 0 {
		t.Fatalf("updating a pending pubsub must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-update", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=2gb", id, "--json")
	if code == 0 {
		t.Fatalf("updating a pending pubsub (--json) must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-update-json", normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "pubsubs", "delete", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-delete", normalizeGolden(out))
	secondID, _ := createPubSub(t, projectID, "nats", "events-2", false)
	code, out, stderr = runCLI(t, "pubsubs", "delete", secondID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-delete-json", normalizeGolden(out))
}

func TestGoldenPubSubsBackup(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "pubs-backup")
	id, _ := createPubSub(t, projectID, "nats", "events", false)

	code, out, stderr := runCLI(t, "pubsubs", "backup", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs backup: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-backup", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "backup", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs backup --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-backup-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "backups", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs backups: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-backups", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "backups", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs backups --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-backups-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "backup", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs backup round 3: code=%d stderr=%q", code, stderr)
	}
	backupID := extractTailID(out)
	code, _, stderr = runCLI(t, "pubsubs", "verify", "--pubsub", id, backupID)
	if code == 0 {
		t.Fatalf("verifying a pending backup must fail, got exit 0")
	}
	compareGolden(t, "pubsubs-verify", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "verify", "--pubsub", id, backupID, "--json")
	if code == 0 {
		t.Fatalf("verifying a pending backup (--json) must fail, got exit 0")
	}
	compareGolden(t, "pubsubs-verify-json", normalizeGolden(stderr))

	outPath := filepath.Join(t.TempDir(), "out.bin")
	code, out, stderr = runCLI(t, "pubsubs", "download-backup", "--pubsub", id, "--out", outPath, "missing-backup-9")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("download-backup (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "pubsubs-download-backup", normalizeGolden(stderr))
	if code, out, stderr = runCLI(t, "pubsubs", "download-backup", "--pubsub", id, "--out", outPath, "missing-backup-9", "--json"); code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("download-backup --json (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "pubsubs-download-backup-json", normalizeGolden(stderr))

	// rotate-password：pending 实例拒（E_DATABASE_NOT_READY 码跨族复用）。
	code, _, stderr = runCLI(t, "pubsubs", "rotate-password", id)
	if code == 0 {
		t.Fatalf("rotating a pending pubsub must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-rotate-password", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "rotate-password", id, "--json")
	if code == 0 {
		t.Fatalf("rotating a pending pubsub (--json) must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-rotate-password-json", normalizeGolden(stderr))
}
