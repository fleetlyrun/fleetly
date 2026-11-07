package cmd

// databases upgrade golden（B7，ADR-0094 决策 5 / design §2.4）：预览形态
// 双钉（计划面：版本 diff + 恢复源 + purge 清单 + 断连窗 + 六步表——无服务
// 端向导动词，--json 是本地计划对象）+ 同版本误用拒绝。中间名时间戳段
// 占位化（wizard 用墙钟，golden 确定性归一）。执行面（六步断点重入）由
// upgradeRPC fake 单测承载（golden 夹具无备份执行链）。再生成：
// go test ./cmd/fleetly/cmd -update。

import (
	"testing"
)

// normalizeUpgradeGolden 委托共享 normalizeGolden（中间名时间戳段的占位
// 化已收进共享面——dualFormRound 轮与本文件轮同源）。
func normalizeUpgradeGolden(s string) string {
	return normalizeGolden(s)
}

// TestGoldenDatabasesUpgradeDualForm 是动词级双形态钉（coverage 守卫的
// databases-upgrade/-json golden 源）：--preview 计划面两形态。
func TestGoldenDatabasesUpgradeDualForm(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbup-dual")
	_, _ = createDatabase2(t, projectID, "postgres", "17", "pgshop")
	dualFormRound(t, "databases upgrade", func(string) []string {
		return []string{"databases", "upgrade", "--project", projectID, "--version", "18", "--preview", "pgshop"}
	})
}

func TestGoldenDatabasesUpgradePreview(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "dbup")
	_, _ = createDatabase2(t, projectID, "postgres", "17", "pgshop")

	// --preview 人类形态：版本 diff + 无备份事实 + purge 清单 + 断连窗 +
	// 六步表。
	code, out, stderr := runCLI(t, "databases", "upgrade", "--project", projectID, "--version", "18", "--preview", "pgshop")
	if code != 0 || stderr != "" {
		t.Fatalf("databases upgrade --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-upgrade-preview", normalizeUpgradeGolden(out))

	// --preview --json：本地计划对象。
	code, out, stderr = runCLI(t, "databases", "upgrade", "--project", projectID, "--version", "18", "--preview", "--json", "pgshop")
	if code != 0 || stderr != "" {
		t.Fatalf("databases upgrade --preview --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "databases-upgrade-preview-json", normalizeUpgradeGolden(out))

	// 名形态解析 + 同版本误用：已在目标版本即拒。
	code, out, stderr = runCLI(t, "databases", "upgrade", "--project", projectID, "--version", "17", "--preview", "pgshop")
	if code == 0 || out != "" || stderr == "" {
		t.Fatalf("same-version upgrade must refuse, got code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, "databases-upgrade-same-version", normalizeUpgradeGolden(stderr))
}

// createDatabase2 是带 --version 的建库 helper（createDatabase 不带版本——
// Default 版本无法测 from 面）。
func createDatabase2(t *testing.T, projectID, engine, version, name string) (string, string) {
	t.Helper()
	args := []string{"databases", "create", "--project", projectID, "--engine", engine, "--version", version, name}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("databases create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}
