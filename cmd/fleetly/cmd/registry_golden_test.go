package cmd

// registry 组 golden（IA v3 二期⑤b）：golden harness 无受管仓装配
//（catalog/tags 的真机面在 zot provider 单测与 dind 演练承载）——精确拒
// 绝面双形态钉死：越纲仓名 E_NOT_FOUND（归属校验先于上游调用）+ 前纲内
// 上游面缺席 E_INTERNAL（harness 假仓无 content 面）；happy 流由 provider
// 单测（httptest 上游）钉。

import (
	"strings"
	"testing"
)

func TestGoldenRegistryRejections(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "regcat")

	// catalog：上游面缺席 → E_INTERNAL 精确报文双形态。
	code, out, stderr := runCLI(t, "registry", "catalog", "--project", projectID)
	if code == 0 || out != "" || !strings.Contains(stderr, "does not offer the content face") {
		t.Fatalf("registry catalog: code=%d out=%q stderr=%q (want no content face)", code, out, stderr)
	}
	compareGolden(t, "registry-catalog", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "registry", "catalog", "--project", projectID, "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "does not offer the content face") {
		t.Fatalf("registry catalog --json: code=%d out=%q stderr=%q (want no content face)", code, out, stderr)
	}
	compareGolden(t, "registry-catalog-json", normalizeGolden(stderr))

	// tags 越纲（仓名不在项目前纲下）：E_NOT_FOUND 双形态——跨项目探测
	// 不可达（归属校验先于上游调用）。
	code, out, stderr = runCLI(t, "registry", "tags", "--project", projectID, "01jdownstream/app")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("registry tags (out-of-scope repo): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "registry-tags", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "registry", "tags", "--project", projectID, "01jdownstream/app", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("registry tags --json (out-of-scope repo): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "registry-tags-json", normalizeGolden(stderr))

	// tags 前纲内但上游面缺席（harness 假仓无 content 面）：E_INTERNAL 双
	// 形态——归属通过后的精确配置失败。
	inScope := strings.ToLower(projectID) + "/app"
	code, out, stderr = runCLI(t, "registry", "tags", "--project", projectID, inScope)
	if code == 0 || out != "" || !strings.Contains(stderr, "does not offer the content face") {
		t.Fatalf("registry tags (in-scope): code=%d out=%q stderr=%q (want no content face)", code, out, stderr)
	}
	compareGolden(t, "registry-tags-upstream", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "registry", "tags", "--project", projectID, inScope, "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "does not offer the content face") {
		t.Fatalf("registry tags --json (in-scope): code=%d out=%q stderr=%q (want no content face)", code, out, stderr)
	}
	compareGolden(t, "registry-tags-upstream-json", normalizeGolden(stderr))
}
