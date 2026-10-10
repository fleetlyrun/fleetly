package cmd

// databases download-backup golden（IA v3 二期⑤）：golden harness 无对象
// 仓装配（备份执行链在 engine 环测与 dind 演练承载）——精确拒绝面双形态
// 钉死；happy 流路由由 engine 单测钉（content 字节一致）。

import (
	"strings"
	"testing"
)

func TestGoldenDownloadBackupNoStore(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dlaxis")
	dbID, _ := createDatabase(t, projectID, "postgres", "shop", false)

	code, out, stderr := runCLI(t, "databases", "download-backup", "--database", dbID, "--out", "out.bin", "01JD0BKPMISSING000000000001")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("download-backup (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	// stdout 双形态为空（二进制不进管道文本面；回执只进错误信封）。
	compareGolden(t, "databases-download-backup", normalizeGolden(stderr))
	if code, out, stderr = runCLI(t, "databases", "download-backup", "--database", dbID, "--out", "out.bin", "01JD0BKPMISSING000000000001", "--json"); code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("download-backup --json (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "databases-download-backup-json", normalizeGolden(stderr))
}
