package cmd

// logs 三轴 golden（IA v3 T8 二期）：--database / --run 载体域寻址（双形态
// golden；空流形态钉死——builds-logs 同款）+ FakeLogging 查询构造断言
// （workload 锚 = databaseID / runID，行级隔离由查询构造执法，ADR-0040）。

import (
	"context"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apitest"
	runrepo "github.com/fleetlyrun/fleetly/internal/state/run"
	taskrepo "github.com/fleetlyrun/fleetly/internal/state/task"
)

func TestGoldenLogsDatabaseAxis(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "logaxis")
	dbID, _ := createDatabase(t, projectID, "postgres", "shop", false)

	code, out, stderr := runCLI(t, "logs", "--database", dbID)
	if code != 0 || stderr != "" {
		t.Fatalf("logs --database: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-database"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "logs", "--database", dbID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("logs --database --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-database")+"-json", normalizeGolden(out))

	// 检索径：查询构造锚断言（Namespace.Database 域 + workload=databaseID）。
	fl := &apitest.FakeLogging{}
	h.Services.Logging = fl
	if code, _, stderr := runCLI(t, "logs", "--database", dbID, "--text", "x"); code != 0 || stderr != "" {
		t.Fatalf("logs --database --text: code=%d stderr=%q", code, stderr)
	}
	queries := fl.Queries()
	if len(queries) != 1 {
		t.Fatalf("expected 1 logging query, got %d", len(queries))
	}
	if queries[0].Namespace.Database != dbID || queries[0].WorkloadID != dbID {
		t.Fatalf("query anchors: ns.Database=%q workload=%q (want both %q)", queries[0].Namespace.Database, queries[0].WorkloadID, dbID)
	}

	// process 在 db 轴精确拒绝（载体单 workload，语义无意义）。
	if code, _, stderr := runCLI(t, "logs", "--database", dbID, "--process", "web"); code == 0 || !strings.Contains(stderr, "process") {
		t.Fatalf("logs --database --process: code=%d stderr=%q (want precise rejection)", code, stderr)
	}
}

func TestGoldenLogsRunAxis(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "runaxis")

	// 直插 Task + Run 行（run 无 CLI 直铸面——池由 engine 消费；日志轴只读
	// 行链，不依赖 engine 活体）。
	taskID := "01LOGAXISTASK0000000000"
	if err := h.Services.Tasks.Create(context.Background(), h.Services.DB.Runner(), &taskrepo.Task{
		ID: taskID, ProjectID: projectID, Name: "worker", Form: "resident",
		State: taskrepo.StateActive, Spec: []byte("{}"),
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	runID := "01LOGAXISRUN000000000001"
	if err := h.Services.Runs.Create(context.Background(), h.Services.DB.Runner(), &runrepo.Run{
		ID: runID, TaskID: taskID, ProjectID: projectID,
		State: runrepo.StateRunning, WorkloadID: runID,
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	code, out, stderr := runCLI(t, "logs", "--run", runID)
	if code != 0 || stderr != "" {
		t.Fatalf("logs --run: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-run"), normalizeGolden(out))

	code, out, stderr = runCLI(t, "logs", "--run", runID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("logs --run --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, goldenFile("logs-run")+"-json", normalizeGolden(out))

	// 检索径：查询构造锚断言（Namespace.Task 域 + workload=runID）。
	fl := &apitest.FakeLogging{}
	h.Services.Logging = fl
	if code, _, stderr := runCLI(t, "logs", "--run", runID, "--text", "x"); code != 0 || stderr != "" {
		t.Fatalf("logs --run --text: code=%d stderr=%q", code, stderr)
	}
	queries := fl.Queries()
	if len(queries) != 1 {
		t.Fatalf("expected 1 logging query, got %d", len(queries))
	}
	if queries[0].Namespace.Task != taskID || queries[0].WorkloadID != runID {
		t.Fatalf("query anchors: ns.Task=%q workload=%q (want %q/%q)", queries[0].Namespace.Task, queries[0].WorkloadID, taskID, runID)
	}
}

func TestLogsAxesMutualExclusion(t *testing.T) {
	_ = newGoldenHarness(t)
	if code, _, stderr := runCLI(t, "logs"); code == 0 || !strings.Contains(stderr, "one of --app") {
		t.Fatalf("logs (no axis): code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI(t, "logs", "--app", "a", "--database", "d"); code == 0 || !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("logs (two axes): code=%d stderr=%q", code, stderr)
	}
}
