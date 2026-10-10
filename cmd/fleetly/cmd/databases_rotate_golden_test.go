package cmd

// databases rotate-password golden（IA v3 二期⑤b）：golden harness 无
// runtime/utility 装配（方言轮换的真机面在 engine 单测与 dind 演练承载）
// ——精确拒绝面双形态钉死：未知行 E_NOT_FOUND + 未在服 E_DATABASE_NOT_READY
//（受理位在服门先行于 engine 调用，harness 内确定性）；happy 流路由由
// engine 单测钉。

import (
	"strings"
	"testing"
)

func TestGoldenRotatePasswordRejections(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "rotpw")
	dbID, _ := createDatabase(t, projectID, "postgres", "shop", false)

	// 未知行：E_NOT_FOUND 双形态（精确动词名 golden——覆盖守卫的钉面对象）。
	code, out, stderr := runCLI(t, "databases", "rotate-password", "01JD0BKPMISSING000000000001")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("rotate-password (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "databases-rotate-password", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "databases", "rotate-password", "01JD0BKPMISSING000000000001", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("rotate-password --json (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "databases-rotate-password-json", normalizeGolden(stderr))

	// 在服门：harness 无收敛环，库恒 pending——E_DATABASE_NOT_READY 双形态。
	code, out, stderr = runCLI(t, "databases", "rotate-password", dbID)
	if code == 0 || out != "" || !strings.Contains(stderr, "E_DATABASE_NOT_READY") {
		t.Fatalf("rotate-password (not running): code=%d out=%q stderr=%q (want not ready)", code, out, stderr)
	}
	compareGolden(t, "databases-rotate-password-not-ready", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "databases", "rotate-password", dbID, "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_DATABASE_NOT_READY") {
		t.Fatalf("rotate-password --json (not running): code=%d out=%q stderr=%q (want not ready)", code, out, stderr)
	}
	compareGolden(t, "databases-rotate-password-not-ready-json", normalizeGolden(stderr))
}
