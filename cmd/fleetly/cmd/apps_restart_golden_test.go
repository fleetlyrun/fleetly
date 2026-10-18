package cmd

// apps restart golden（ADR-0083 §3 重启双面）：缺省声明面——最新冻结
// Revision 全进程 restart_seq+1 经部署管线（lineage 行 + admission 判定
// 随 golden 钉死）；--runtime 动作面（ADR-0069 逃生）——单进程零 lineage
// 的人类行式回执 + --json 空体 protojson；未知进程精确 E_NOT_FOUND；未
// 部署 App 无冻结 Revision 诚实报错。双形态 golden 钉死。

import (
	"strings"
	"testing"
)

func TestGoldenAppsRestart(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	// 缺省声明面：R1 全进程 seq 0→1，受理走 Deploy 同款管线。
	code, out, stderr := runCLI(t, "apps", "restart", "--app", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "restart_seq bumped") {
		t.Fatalf("apps restart: expected the declarative restart receipt, got %q", out)
	}
	compareGolden(t, goldenFile("apps-restart"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-restart")+"-json", normalizeGolden(out))

	// --runtime 动作面（逃生通道）：单进程零 lineage，回执形态不变
	//（ADR-0069 golden 语义平移到旗标面）。
	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--runtime", "--process", "web")
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart --runtime: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-runtime"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--runtime", "--process", "web", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps restart --runtime --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-runtime")+"-json", normalizeGolden(out))
}

func TestGoldenAppsRestartUnknownProcess(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	code, out, stderr := runCLI(t, "apps", "restart", "--app", appID, "--runtime", "--process", "wrker")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("apps restart --runtime (unknown process): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-unknown"), normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--runtime", "--process", "wrker", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "E_NOT_FOUND") {
		t.Fatalf("apps restart --runtime --json (unknown process): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-unknown")+"-json", normalizeGolden(stderr))
}

func TestGoldenAppsRestartNeverDeployed(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "restartnd")
	code, out, stderr := runCLI(t, "apps", "create", "--project", projectID, "bare")
	if code != 0 || stderr != "" {
		t.Fatalf("apps create: code=%d stderr=%q out=%q", code, stderr, out)
	}
	appID := extractTailID(out)

	// 无冻结 Revision：诚实报错（声明面无对象可重启；--runtime 是逃生）。
	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID)
	if code == 0 || out != "" || !strings.Contains(stderr, "no frozen revision to restart") {
		t.Fatalf("apps restart (never deployed): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-never-deployed"), normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "apps", "restart", "--app", appID, "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "no frozen revision to restart") {
		t.Fatalf("apps restart --json (never deployed): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, goldenFile("apps-restart-never-deployed")+"-json", normalizeGolden(stderr))
}

func TestAppsRestartFlagMisuse(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	// --process 无 --runtime：声明面重启全部进程，进程旗标无意义。
	if code, _, stderr := runCLI(t, "apps", "restart", "--app", appID, "--process", "web"); code == 0 || !strings.Contains(stderr, "--runtime") {
		t.Fatalf("apps restart (--process without --runtime): code=%d stderr=%q", code, stderr)
	}
	// --runtime 缺 --process：动作面必须点名进程。
	if code, _, stderr := runCLI(t, "apps", "restart", "--app", appID, "--runtime"); code == 0 || !strings.Contains(stderr, "--process is required") {
		t.Fatalf("apps restart (--runtime without --process): code=%d stderr=%q", code, stderr)
	}
}
