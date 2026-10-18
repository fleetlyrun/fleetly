package cmd

// revisions get / apps workloads golden（ADR-0071 读面）：冻结全文回读与
// Workload 视图双形态（投影缺省/显式 --seq/--live）+ 负例（未知 seq、无
// 成功部署的缺省寻址）。

import (
	"strings"
	"testing"
)

func TestGoldenRevisionsGet(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, out, stderr := runCLI(t, "revisions", "get", "--app", appID, "--seq", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("revisions get: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "processes") {
		t.Fatalf("revisions get: expected the full spec payload, got %q", out)
	}
	compareGolden(t, goldenFile("revisions-get"), normalizeGolden(out))

	code, outJSON, stderr := runCLI(t, "revisions", "get", "--app", appID, "--seq", "1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("revisions get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("revisions-get")+"-json", normalizeGolden(outJSON))

	// 未知 seq 精确 E_NOT_FOUND。
	if code, _, stderr := runCLI(t, "revisions", "get", "--app", appID, "--seq", "9"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("revisions get (unknown seq): code=%d stderr=%q (want not found)", code, stderr)
	}
}

func TestGoldenAppsWorkloads(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	// 投影缺省寻址（当前生效 = 最新成功部署的 to_revision）。
	code, out, stderr := runCLI(t, "apps", "workloads", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "web") {
		t.Fatalf("apps workloads: expected the web process row, got %q", out)
	}
	compareGolden(t, goldenFile("apps-workloads"), normalizeGolden(out))

	code, outJSON, stderr := runCLI(t, "apps", "workloads", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-workloads")+"-json", normalizeGolden(outJSON))

	// 在服观测形态。
	code, outLive, stderr := runCLI(t, "apps", "workloads", "--live", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads --live: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(outLive, "running") {
		t.Fatalf("apps workloads --live: expected a running observation, got %q", outLive)
	}
	compareGolden(t, goldenFile("apps-workloads")+"-live", normalizeGolden(outLive))

	code, outLiveJSON, stderr := runCLI(t, "apps", "workloads", "--live", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads --live --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-workloads")+"-live-json", normalizeGolden(outLiveJSON))
}

func TestAppsWorkloadsNoSucceeded(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "wlnd")
	if code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, "bare"); code != 0 || stderr != "" {
		t.Fatalf("apps create: code=%d stderr=%q out=%q", code, stderr, out)
	}
	// 从未成功部署：缺省寻址（当前生效）精确 E_NOT_FOUND。
	if code, _, stderr := runCLI(t, "apps", "workloads", "bare"); code == 0 || !strings.Contains(stderr, "not found") {
		t.Fatalf("apps workloads (never deployed): code=%d stderr=%q (want not found)", code, stderr)
	}
}

// ADR-0083 seq 非零夹具：声明面重启（restart_seq 0→1）驱动到 succeeded
// 后，投影视图的 RESTART_SEQ 列携带非零序数（"这次重启轮到第几拍"的
// 读面回显）。
func TestGoldenAppsWorkloadsRestartSeq(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, _, stderr := runCLI(t, "apps", "restart", "--app", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart: code=%d stderr=%q", code, stderr)
	}
	promoteCarrierToSucceeded(t, h, appID, appID+"-web", 2)

	code, out, stderr := runCLI(t, "apps", "workloads", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads (post-restart): code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "RESTART_SEQ") {
		t.Fatalf("apps workloads: expected the RESTART_SEQ column, got %q", out)
	}
	compareGolden(t, goldenFile("apps-workloads-restartseq"), normalizeGolden(out))

	code, outJSON, stderr := runCLI(t, "apps", "workloads", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps workloads --json (post-restart): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-workloads-restartseq")+"-json", normalizeGolden(outJSON))

	// revisions get seq 非零夹具（ADR-0083 验收锚）：重启 Revision 的冻结
	// 全文携带重启序数（该动词人读面是 protojson.Marshal 缺省 camelCase
	// ——既有 golden 形态，restartSeq 键名同源）。
	code, out, stderr = runCLI(t, "revisions", "get", "--app", appID, "--seq", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("revisions get (post-restart): code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "restartSeq") {
		t.Fatalf("revisions get: expected restartSeq in the frozen spec, got %q", out)
	}
	compareGolden(t, goldenFile("revisions-get-restartseq"), normalizeGolden(out))

	code, outJSON, stderr = runCLI(t, "revisions", "get", "--app", appID, "--seq", "2", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("revisions get --json (post-restart): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("revisions-get-restartseq")+"-json", normalizeGolden(outJSON))
}
