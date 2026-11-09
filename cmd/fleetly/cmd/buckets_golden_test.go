package cmd

// buckets 命令组 golden（B5，ADR-0096 决策 8；三种数据服务类型确认面同构）：pubsubs 组
// 同构的双形态钉板——create/list/get/update/delete 生命周期、rotate-password
// 未在服拒绝面；update 确认三形态（--preview/--ticket/--yes/交互 + 拒绝面）；
// 无 backup 动词族（silo 转储挂账——能力位裁剪不设面，golden 无对应夹具）。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// createBucketStore 建实例并返回 (id, 人类形态输出)。
func createBucketStore(t *testing.T, projectID, engine, name string, jsonOut bool) (string, string) {
	t.Helper()
	args := []string{"buckets", "create", "--project", projectID, "--engine", engine, name}
	if jsonOut {
		args = append(args, "--json")
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("buckets create (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out), out
}

// waitBucketStoreRunning 驱动收敛环直到 bucket store 行 running
// （waitPubSubRunning 同构——bucket_stores 表读面）。
func waitBucketStoreRunning(t *testing.T, h *apitest.Harness, id string) {
	t.Helper()
	ctx := context.Background()
	driveCtx := fleetly.WithToken(ctx, h.Token)
	deadline := time.Now().Add(10 * time.Second)
	status := ""
	for {
		h.Drive(driveCtx)
		if err := h.DB.Runner().QueryRowContext(ctx,
			`SELECT status FROM bucket_stores WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatalf("read bucket store status for %s: %v", id, err)
		}
		if status == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bucket store %s did not reach running within the drive budget (last status %q)", id, status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// seedRunningBucketStore 建 silo bucket store（带 cpu 基线 250——resources
// 面补丁语义「未点名轴保留」的可见锚）并推到 running（Update 受理门：
// status != running 拒 E_CONFLICT）。
func seedRunningBucketStore(t *testing.T, h *apitest.Harness, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "buckets", "create", "--project", projectID, "--engine", "silo",
		"--cpu-millis", "250", name)
	require.Zero(t, code, "seed bucket store %s: %s", name, stderr)
	id := extractTailID(out)
	require.NotEmpty(t, id, "seed bucket store %s: no id in %q", name, out)
	h.Drive(fleetly.WithToken(context.Background(), h.Token))
	var gen capability.Generation
	for _, call := range h.Runtime.Calls() {
		if call.NS.BucketStore == id {
			gen = call.Gen
		}
	}
	require.NotZero(t, gen, "seed bucket store %s: the carrier must have converged through Ensure", name)
	h.Runtime.ReportRunning(id, gen)
	waitBucketStoreRunning(t, h, id)
	return id
}

// previewBucketStoreTicketFromCLI 走两调用流第 1 步（--preview --json）取票。
func previewBucketStoreTicketFromCLI(t *testing.T, id string, flags ...string) string {
	t.Helper()
	args := append(append([]string{"buckets", "update"}, flags...), "--preview", id, "--json")
	code, out, stderr := runCLI(t, args...)
	require.Zero(t, code, "preview leg: %s", stderr)
	var payload struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "preview --json must be protojson: %q", out)
	require.NotEmpty(t, payload.Ticket)
	return payload.Ticket
}

func TestGoldenBucketsLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "bkts")

	id, out := createBucketStore(t, projectID, "silo", "assets", false)
	compareGolden(t, "buckets-create", normalizeGolden(out))
	_, out = createBucketStore(t, projectID, "silo", "media", true)
	compareGolden(t, "buckets-create-json", normalizeGolden(out))

	code, out, stderr := runCLI(t, "buckets", "list", "--project", projectID)
	if code != 0 || stderr != "" {
		t.Fatalf("buckets list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-list", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "list", "--project", projectID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("buckets list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "buckets", "get", id)
	if code != 0 || stderr != "" {
		t.Fatalf("buckets get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-get", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "get", id, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("buckets get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-get-json", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "get", "--project", "bkts", "assets")
	if code != 0 || stderr != "" {
		t.Fatalf("buckets get (by name): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-get-by-name", normalizeGolden(out))

	// update：golden 夹具实例恒 pending（无 Runtime 观测）——非 TTY 裸调用是
	// 本地确认指引（确认门先于在服门）；--yes 直发撞在服门（E_CONFLICT）。
	forceNoTTY(t)
	code, _, stderr = runCLI(t, "buckets", "update", "--cpu-millis", "500", id)
	if code == 0 {
		t.Fatalf("updating a pending bucket store without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "buckets-update", normalizeConfirmGolden(stderr))
	code, _, stderr = runCLI(t, "buckets", "update", "--cpu-millis", "500", id, "--json")
	if code == 0 {
		t.Fatalf("updating a pending bucket store (--json) must be refused, got exit 0")
	}
	compareGolden(t, "buckets-update-json", normalizeConfirmGolden(stderr))
	code, _, stderr = runCLI(t, "buckets", "update", "--cpu-millis", "500", "--yes", id)
	if code == 0 {
		t.Fatalf("updating a pending bucket store (--yes) must be refused by the running gate, got exit 0")
	}
	compareGolden(t, "buckets-update-not-running", normalizeConfirmGolden(stderr))

	// rotate-password：pending 实例拒（E_DATABASE_NOT_READY 码跨类型复用）。
	code, _, stderr = runCLI(t, "buckets", "rotate-password", id)
	if code == 0 {
		t.Fatalf("rotating a pending bucket store must be refused, got exit 0")
	}
	compareGolden(t, "buckets-rotate-password", normalizeGolden(stderr))
	code, _, stderr = runCLI(t, "buckets", "rotate-password", id, "--json")
	if code == 0 {
		t.Fatalf("rotating a pending bucket store (--json) must be refused, got exit 0")
	}
	compareGolden(t, "buckets-rotate-password-json", normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "buckets", "delete", id)
	if code != 0 || stderr != "" {
		t.Fatalf("buckets delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-delete", normalizeGolden(out))
	secondID, _ := createBucketStore(t, projectID, "silo", "assets-2", false)
	code, out, stderr = runCLI(t, "buckets", "delete", secondID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("buckets delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-delete-json", normalizeGolden(out))
}

// TestGoldenBucketsUpdate（三种数据服务类型确认面同构，ADR-0089/0095 决策 9）：update
// 动词确认三形态（--preview 退出 / --yes / --ticket 两调用流 / 交互 y·N）+
// 拒绝面（非 TTY 本地指引 / 伪造票 MISMATCH / 零旋钮 --set 受理拒）+ 用法
// 面。零旋钮族：预演 settings diff 恒空、资源变更即 replace；补丁语义可见
// 锚：基线 cpu 250 在 --memory-mb 后保留。
func TestGoldenBucketsUpdate(t *testing.T) {
	h := newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "bkts-update")
	base := seedRunningBucketStore(t, h, projectID, "tuned")

	t.Run("preview", func(t *testing.T) {
		dualForm(t, "buckets update preview", []string{"buckets", "update",
			"--memory-mb", "512", "--preview", base})
	})

	// 配额 4/项目（ADR-0096 决策 8）：每轮现铸实例须分项目，否则第五枚撞
	// E_QUOTA_EXCEEDED（pubsubs 配额 8 不显此面）。
	t.Run("yes", func(t *testing.T) {
		roundProject := seedDatabaseProject(t, "bkts-yes")
		dualFormRound(t, "buckets update yes", func(label string) []string {
			id := seedRunningBucketStore(t, h, roundProject, "upd-yes-"+label)
			return []string{"buckets", "update", "--memory-mb", "512", "--yes", id}
		})
	})

	t.Run("ticket", func(t *testing.T) {
		roundProject := seedDatabaseProject(t, "bkts-ticket")
		dualFormRound(t, "buckets update ticket", func(label string) []string {
			id := seedRunningBucketStore(t, h, roundProject, "upd-ticket-"+label)
			ticket := previewBucketStoreTicketFromCLI(t, id, "--memory-mb", "512")
			return []string{"buckets", "update", "--memory-mb", "512", "--ticket", ticket, id}
		})
	})

	t.Run("interactive confirm", func(t *testing.T) {
		roundProject := seedDatabaseProject(t, "bkts-tty")
		fakeConfirmTTY(t, "y\n")
		dualFormRound(t, "buckets update interactive", func(label string) []string {
			id := seedRunningBucketStore(t, h, roundProject, "upd-tty-"+label)
			return []string{"buckets", "update", "--cpu-millis", "750", id}
		})
	})
	t.Run("interactive abort", func(t *testing.T) {
		fakeConfirmTTY(t, "n\n")
		code, out, stderr := runCLI(t, "buckets", "update", "--cpu-millis", "750", base)
		require.Zero(t, code, "interactive abort: %s", stderr)
		compareGolden(t, "buckets-update-interactive-abort", normalizeConfirmGolden(out))
	})

	t.Run("non-tty refusal", func(t *testing.T) {
		forceNoTTY(t)
		dualFormErr(t, "buckets update confirm-required",
			[]string{"buckets", "update", "--memory-mb", "512", base}, 1)
	})
	t.Run("ticket mismatch", func(t *testing.T) {
		dualFormErr(t, "buckets update ticket-mismatch",
			[]string{"buckets", "update", "--memory-mb", "512", "--ticket", "bogus-ticket", base}, 1)
	})
	// 零旋钮族：任何 --set 键在受理位拒（E_INVALID_ARGUMENT——目录 knobs 空；
	// 预演腿即拒，零写）。
	t.Run("zero knobs refuse set", func(t *testing.T) {
		dualFormErr(t, "buckets update zero-knobs",
			[]string{"buckets", "update", "--set", "anything=1", "--preview", base}, 1)
	})

	t.Run("usage", func(t *testing.T) {
		for _, args := range [][]string{
			{"buckets", "update", base},
			{"buckets", "update", "--set", "k=1", "--unset", "k", base},
			{"buckets", "update", "--set", "k", base},
			{"buckets", "update", "--memory-mb", "512", "--yes", "--preview", base},
			{"buckets", "update", "--cpu-millis", "lots", base},
			{"buckets", "update", "--cpu-millis", "-1", base},
		} {
			code, _, _ := runCLI(t, args...)
			require.Equal(t, exitUsage, code, "%v must be a usage error", args)
		}
	})
}
