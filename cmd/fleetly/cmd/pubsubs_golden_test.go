package cmd

// pubsubs 命令组 golden（B4b，ADR-0096 决策 8；B4c 确认面三种数据服务类型对齐）：
// databases 组同构的双形态钉板——create/list/get/update/delete 生命周期、
// backup/backups/verify 备份面（golden 夹具无执行链：pending 行的拒绝面即
// 确定性输出）、download-backup 无对象仓拒绝面、rotate-password 未在服拒绝
// 面；update 确认三形态（B4c：--preview/--ticket/--yes/交互 + 拒绝面）。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
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

// waitPubSubRunning 驱动收敛环直到 pubsub 行 running（waitDatabaseRunning
// 同构——pubsubs 表读面）。
func waitPubSubRunning(t *testing.T, h *apitest.Harness, id string) {
	t.Helper()
	ctx := context.Background()
	driveCtx := fleetly.WithToken(ctx, h.Token)
	deadline := time.Now().Add(10 * time.Second)
	status := ""
	for {
		h.Drive(driveCtx)
		if err := h.DB.Runner().QueryRowContext(ctx,
			`SELECT status FROM pubsubs WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatalf("read pubsub status for %s: %v", id, err)
		}
		if status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pubsub %s did not reach running within the drive budget (last status %q)", id, status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// seedRunningPubSub 建 nats pubsub（带一枚 static 旋钮基线
// jetstream_max_file=1gb——补丁语义「未点名键保留」的可见锚）并推到
// running（Update 受理门：status != running 拒 E_CONFLICT）。
func seedRunningPubSub(t *testing.T, h *apitest.Harness, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "nats",
		"--set", "jetstream_max_file=1gb", name)
	require.Zero(t, code, "seed pubsub %s: %s", name, stderr)
	id := extractTailID(out)
	require.NotEmpty(t, id, "seed pubsub %s: no id in %q", name, out)
	h.Drive(fleetly.WithToken(context.Background(), h.Token))
	var gen capability.Generation
	for _, call := range h.Runtime.Calls() {
		if call.NS.PubSub == id {
			gen = call.Gen
		}
	}
	require.NotZero(t, gen, "seed pubsub %s: the carrier must have converged through Ensure", name)
	h.Runtime.ReportRunning(id, gen)
	waitPubSubRunning(t, h, id)
	return id
}

// previewPubSubTicketFromCLI 走两调用流第 1 步（--preview --json）取票。
func previewPubSubTicketFromCLI(t *testing.T, id string, flags ...string) string {
	t.Helper()
	args := append(append([]string{"pubsubs", "update"}, flags...), "--preview", id, "--json")
	code, out, stderr := runCLI(t, args...)
	require.Zero(t, code, "preview leg: %s", stderr)
	var payload struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "preview --json must be protojson: %q", out)
	require.NotEmpty(t, payload.Ticket)
	return payload.Ticket
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

	// update：golden 夹具实例恒 pending（无 Runtime 观测）——B4c 确认面后
	// 非 TTY 裸调用是本地确认指引（确认门先于在服门）；--yes 直发撞在服
	// 门（E_CONFLICT——成功面在 TestGoldenPubSubsUpdate 承载）。
	forceNoTTY(t)
	code, _, stderr = runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=2gb", id)
	if code == 0 {
		t.Fatalf("updating a pending pubsub without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-update", normalizeConfirmGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=2gb", id, "--json")
	if code == 0 {
		t.Fatalf("updating a pending pubsub (--json) must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-update-json", normalizeConfirmGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=2gb", "--yes", id)
	if code == 0 {
		t.Fatalf("updating a pending pubsub (--yes) must be refused by the running gate, got exit 0")
	}
	compareGolden(t, "pubsubs-update-not-running", normalizeConfirmGolden(stderr))

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

	// rotate-password：pending 实例拒（E_DATABASE_NOT_READY 码跨类型复用）。
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

// TestGoldenPubSubsUpdate（B4c 确认面三种数据服务类型对齐，ADR-0089/0095 决策 9）：
// update 动词确认三形态（--preview 退出 / --yes / --ticket 两调用流 /
// 交互 y·N）+ 拒绝面（非 TTY 本地指引 / 伪造票 MISMATCH）+ 用法面。本族
// 首批旋钮全 static：预演恒 replace、instant lane 恒空；补丁语义可见锚：
// 基线 jetstream_max_file=1gb 在 --set 其他键后保留（回执 settings 全集）。
func TestGoldenPubSubsUpdate(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "pubs-update")
	base := seedRunningPubSub(t, h, projectID, "tuned")

	// ---- --preview：预演 + ticket 行后退出（零写）。static 键 + 资源轴
	// （rollout=replace，恒）。----
	t.Run("preview", func(t *testing.T) {
		dualForm(t, "pubsubs update preview", []string{"pubsubs", "update",
			"--set", "jetstream_max_memory=256mb", "--cpu-millis", "500", "--preview", base})
	})
	// --unset 基线键：预演 diff 的 (default) 端点。
	t.Run("preview unset", func(t *testing.T) {
		code, out, stderr := runCLI(t, "pubsubs", "update", "--unset", "jetstream_max_file", "--preview", base)
		require.Zero(t, code, "unset preview: %s", stderr)
		compareGolden(t, "pubsubs-update-preview-unset", normalizeConfirmGolden(out))
	})

	// ---- --yes：skip 直发 + 一行说明（人类形态）。每轮现铸实例。----
	t.Run("yes", func(t *testing.T) {
		dualFormRound(t, "pubsubs update yes", func(label string) []string {
			id := seedRunningPubSub(t, h, projectID, "upd-yes-"+label)
			return []string{"pubsubs", "update", "--set", "jetstream_max_memory=256mb", "--memory-mb", "512", "--yes", id}
		})
	})

	// ---- --ticket：两调用流第 2 步（票经 CLI --preview --json 铸出）。----
	t.Run("ticket", func(t *testing.T) {
		dualFormRound(t, "pubsubs update ticket", func(label string) []string {
			id := seedRunningPubSub(t, h, projectID, "upd-ticket-"+label)
			ticket := previewPubSubTicketFromCLI(t, id, "--set", "jetstream_max_memory=256mb")
			return []string{"pubsubs", "update", "--set", "jetstream_max_memory=256mb", "--ticket", ticket, id}
		})
	})

	// ---- 交互形态（伪 TTY）：预演打印 + y/N 提示 + y 带票直发 / N 中止。----
	t.Run("interactive confirm", func(t *testing.T) {
		fakeConfirmTTY(t, "y\n")
		dualFormRound(t, "pubsubs update interactive", func(label string) []string {
			id := seedRunningPubSub(t, h, projectID, "upd-tty-"+label)
			return []string{"pubsubs", "update", "--set", "jetstream_max_file=4gb", id}
		})
	})
	t.Run("interactive abort", func(t *testing.T) {
		fakeConfirmTTY(t, "n\n")
		code, out, stderr := runCLI(t, "pubsubs", "update", "--set", "jetstream_max_file=4gb", base)
		require.Zero(t, code, "interactive abort: %s", stderr)
		compareGolden(t, "pubsubs-update-interactive-abort", normalizeConfirmGolden(out))
	})

	// ---- 拒绝面：非 TTY 无旗标（本地指引，不发请求）/ 伪造票。----
	t.Run("non-tty refusal", func(t *testing.T) {
		forceNoTTY(t)
		dualFormErr(t, "pubsubs update confirm-required",
			[]string{"pubsubs", "update", "--set", "jetstream_max_memory=256mb", base}, 1)
	})
	t.Run("ticket mismatch", func(t *testing.T) {
		dualFormErr(t, "pubsubs update ticket-mismatch",
			[]string{"pubsubs", "update", "--set", "jetstream_max_memory=256mb", "--ticket", "bogus-ticket", base}, 1)
	})

	// ---- 用法面（exit 64，拨号前）。----
	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{
			{"pubsubs", "update", base}, // 无变更
			{"pubsubs", "update", "--set", "jetstream_max_file=1gb", "--unset", "jetstream_max_file", base}, // set/unset 冲突
			{"pubsubs", "update", "--set", "jetstream_max_file", base},                                      // 非 KEY=VALUE
			{"pubsubs", "update", "--set", "jetstream_max_file=1gb", "--yes", "--preview", base},            // 确认旗标互斥
			{"pubsubs", "update", "--cpu-millis", "lots", base},                                             // 值类型
			{"pubsubs", "update", "--cpu-millis", "-1", base},                                               // 界外
		} {
			code, _, _ := runCLI(t, args...)
			require.Equal(t, exitUsage, code, "%v must be a usage error", args)
		}
	})
}

// kafka 引擎腿（B6a，ADR-0096 决策 6/7）：创建（旋钮全量集）→ heap 联动
// 受理拒（红队 F-13）→ dynamic 热应用（kafka-configs.sh 面——golden 夹具
// utility 假面恒成功）→ TriggerBackup 拒（无备份面挂账文案）→ restore 源
// 受理拒。nats 腿之上的引擎级能力位 + 双车道钉板。
func TestGoldenPubSubsKafka(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "pubs-kafka")

	forceNoTTY(t)
	code, out, stderr := runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "kafka",
		"--set", "heap_mb=384mb", "--set", "num.partitions=4", "--set", "log.retention.ms=90d", "drill")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs create kafka: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-kafka-create", normalizeGolden(out))
	id := extractTailID(out)

	_, out, stderr = runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "kafka", "logs", "--json")
	if stderr != "" {
		t.Fatalf("pubsubs create kafka --json: %q", stderr)
	}
	compareGolden(t, "pubsubs-kafka-create-json", normalizeGolden(out))

	// heap 联动受理拒（F-13 三分支：两者皆设 → heap ≤ memory×0.7）。
	code, _, stderr = runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "kafka",
		"--memory-mb", "512", "--set", "heap_mb=512mb", "greedy")
	if code == 0 {
		t.Fatalf("heap_mb 512mb against a 512MiB memory limit must be refused at admission, got exit 0")
	}
	compareGolden(t, "pubsubs-kafka-heap-linkage-refused", normalizeGolden(stderr))

	// 推 running（假 Runtime 观测——seedRunningPubSub 的 kafka 形态）。
	driveCtx := fleetly.WithToken(context.Background(), h.Token)
	h.Drive(driveCtx)
	var gen capability.Generation
	for _, call := range h.Runtime.Calls() {
		if call.NS.PubSub == id {
			gen = call.Gen
		}
	}
	if gen == 0 {
		t.Fatalf("kafka pubsub %s did not converge through Ensure", id)
	}
	h.Runtime.ReportRunning(id, gen)
	waitPubSubRunning(t, h, id)

	// dynamic 热应用（--yes 直发；kafka dynamic 面 = kafka-configs.sh——
	// utility 假面成功后行落库）。
	code, out, stderr = runCLI(t, "pubsubs", "update", "--set", "log.retention.ms=30d", "--yes", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs update kafka dynamic: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-kafka-update-dynamic", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "get", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs get kafka: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-kafka-get-json", normalizeGolden(out))

	// TriggerBackup 拒：无备份面（挂账文案——无官方转储工具）。
	code, _, stderr = runCLI(t, "pubsubs", "backup", id)
	if code == 0 {
		t.Fatalf("triggering a kafka backup must be refused (no data backup face), got exit 0")
	}
	compareGolden(t, "pubsubs-kafka-backup-refused", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "backup", id, "--json")
	if code == 0 {
		t.Fatalf("triggering a kafka backup (--json) must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-kafka-backup-refused-json", normalizeGolden(stderr))

	// restore 源受理拒：无备份面即无恢复源（连锁拒绝先于台账查找）。
	code, _, stderr = runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "kafka",
		"--restore-from-backup", "bk_01jdkfk0000000000000000000", "res")
	if code == 0 {
		t.Fatalf("restoring into a kafka pubsub must be refused (no data backup face), got exit 0")
	}
	compareGolden(t, "pubsubs-kafka-restore-refused", normalizeGolden(stderr))
}

// redpanda 引擎腿（B6b，ADR-0096 决策 7）：kafka 协议族第二实现——创建
// （dynamic 旋钮全量集，snake_case 键）→ dynamic 热应用（rpk cluster
// config 面——golden 夹具 utility 假面恒成功）→ get 回读 → TriggerBackup
// 拒 + restore 源受理拒（kafka 同族挂账文案）。heap 联动不适用（native 无
// JVM 堆——不设 heap_mb 旋钮）。
func TestGoldenPubSubsRedpanda(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "pubs-redpanda")

	forceNoTTY(t)
	code, out, stderr := runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "redpanda",
		"--set", "log_retention_ms=90d", "--set", "log_segment_size=256mb", "rdrill")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs create redpanda: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-redpanda-create", normalizeGolden(out))
	id := extractTailID(out)

	_, out, stderr = runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "redpanda", "rlogs", "--json")
	if stderr != "" {
		t.Fatalf("pubsubs create redpanda --json: %q", stderr)
	}
	compareGolden(t, "pubsubs-redpanda-create-json", normalizeGolden(out))

	// 推 running（假 Runtime 观测）。
	driveCtx := fleetly.WithToken(context.Background(), h.Token)
	h.Drive(driveCtx)
	var gen capability.Generation
	for _, call := range h.Runtime.Calls() {
		if call.NS.PubSub == id {
			gen = call.Gen
		}
	}
	if gen == 0 {
		t.Fatalf("redpanda pubsub %s did not converge through Ensure", id)
	}
	h.Runtime.ReportRunning(id, gen)
	waitPubSubRunning(t, h, id)

	// dynamic 热应用（--yes 直发；redpanda dynamic 面 = rpk cluster config
	// set——utility 假面成功后行落库）。
	code, out, stderr = runCLI(t, "pubsubs", "update", "--set", "log_retention_ms=30d", "--yes", id)
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs update redpanda dynamic: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-redpanda-update-dynamic", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "get", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("pubsubs get redpanda: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-redpanda-get-json", normalizeGolden(out))

	// TriggerBackup 拒 + restore 源受理拒：kafka 同族挂账文案。
	code, _, stderr = runCLI(t, "pubsubs", "backup", id)
	if code == 0 {
		t.Fatalf("triggering a redpanda backup must be refused (no data backup face), got exit 0")
	}
	compareGolden(t, "pubsubs-redpanda-backup-refused", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "pubsubs", "create", "--project", projectID, "--engine", "redpanda",
		"--restore-from-backup", "bk_01jdkfk0000000000000000000", "res-rp")
	if code == 0 {
		t.Fatalf("restoring into a redpanda pubsub must be refused (no data backup face), got exit 0")
	}
	compareGolden(t, "pubsubs-redpanda-restore-refused", normalizeGolden(stderr))
}
