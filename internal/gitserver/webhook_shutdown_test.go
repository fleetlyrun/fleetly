package gitserver

// X-6/MG-4（B6）：webhook worker 停机静默丢失的披露回归测试。
//   - 排空预算尽（在处理项未返回）→ 队列剩余 job 落 app.webhook_interrupted
//     审计（delivery id + sha）+ 撤坑，确定性丢弃；
//   - 在处理项不被误披露（它有自己的终局路径）；
//   - 预算内排空（空队列停机）零披露噪音。

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// interruptedAudits 读取当前 app.webhook_interrupted 审计行。
func interruptedAudits(t *testing.T, st *state.Store) []state.AuditRecord {
	t.Helper()
	rows, err := st.RecentAudits(context.Background(), 200)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var out []state.AuditRecord
	for _, a := range rows {
		if a.Action == "app.webhook_interrupted" {
			out = append(out, a)
		}
	}
	return out
}

// waitForInterruptedAudits 轮询等待披露审计到位（披露看门狗异步触发）。
func waitForInterruptedAudits(t *testing.T, st *state.Store, want int) []state.AuditRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := interruptedAudits(t, st)
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("interrupted audits = %d, want %d（披露看门狗未生效）", len(got), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestWebhookShutdownDisclosesQueuedJobs 核心回归：停机时在处理项挂起（门闸
// 模拟慢 fetch）、队列仍有已受理 job → 排空预算尽后剩余 job 被披露丢弃
// （审计可对账），在处理项不误披露；放行后在处理项照常完成。
func TestWebhookShutdownDisclosesQueuedJobs(t *testing.T) {
	savedBudget := webhookDrainBudget
	webhookDrainBudget = 150 * time.Millisecond
	t.Cleanup(func() { webhookDrainBudget = savedBudget })

	src, st, box, _ := newTestSource(t, time.Minute)
	ctx := context.Background()
	secret := "hook-secret-at-least-16"

	sourceDir, _ := newSourceRepo(t, composeFixture)
	appRow, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	setAppWebhookSecret(t, st, box, appRow.ID, secret)
	setAppSource(t, st, appRow.ID, fileURL(sourceDir), "main")

	// 慢拉源门闸：首个 job 占住 worker（在处理项），后续 job 滞留队列。
	fetchStarted := make(chan struct{}, 4)
	release := make(chan struct{})
	src.fetchFn = func(ctx context.Context, plan fetchPlan) error {
		fetchStarted <- struct{}{}
		<-release
		return runFetch(ctx, plan)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	src.StartWebhookWorker(runCtx)
	srv := httptest.NewServer(NewWebhookHandler(src))
	t.Cleanup(srv.Close)

	// 经真实 HTTP 面受理 3 个投递（受理审计 + 占坑路径全链路；sha 互异过
	// 幂等去重，单 worker 串行 → 第 1 个在处理、第 2/3 个在队列）。
	acceptDelivery := func(i int) {
		t.Helper()
		sha := fmt.Sprintf("%040x", 0x9000+i)
		body := pushBody("refs/heads/main", sha)
		resp, raw := postRaw(t, srv, "/v1/apps/my-api/webhooks/github", map[string]string{
			"Content-Type":        "application/json",
			"X-Hub-Signature-256": sign([]byte(secret), body),
			"X-GitHub-Delivery":   fmt.Sprintf("d-shutdown-%d", i),
		}, body)
		if resp.StatusCode != 202 {
			t.Fatalf("delivery %d accept status = %d (%s)", i, resp.StatusCode, raw)
		}
	}
	acceptDelivery(1)
	select {
	case <-fetchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not pick first job within 5s")
	}
	acceptDelivery(2)
	acceptDelivery(3)

	// 停机：ctx 取消进入排空；在处理项挂起（未放行），预算尽后队列剩余
	// 2 个 job 转披露。
	cancel()
	disclosed := waitForInterruptedAudits(t, st, 2)
	if len(disclosed) != 2 {
		t.Fatalf("interrupted audits = %d, want exactly 2（在处理项 d-shutdown-1 不得误披露）", len(disclosed))
	}
	joined := disclosed[0].DiffSummary + disclosed[1].DiffSummary
	if !strings.Contains(joined, "d-shutdown-2") || !strings.Contains(joined, "d-shutdown-3") {
		t.Fatalf("interrupted audits missing delivery ids: %s / %s",
			disclosed[0].DiffSummary, disclosed[1].DiffSummary)
	}
	for _, a := range disclosed {
		if a.Result != "error" {
			t.Fatalf("interrupted audit result = %s, want error", a.Result)
		}
	}

	// 放行在处理项 → 排空循环返回，Stop 等待点正常收口。
	close(release)
	if err := src.StopWebhookWorker(context.Background()); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	// 披露条数不再增长（在处理项完成不产生新披露）。
	if got := len(interruptedAudits(t, st)); got != 2 {
		t.Fatalf("interrupted audits after drain = %d, want 2", got)
	}
}

// TestWebhookShutdownEmptyQueueNoNoise 零噪音：空队列停机（无受理滞留）→
// 不落任何 app.webhook_interrupted 审计。
func TestWebhookShutdownEmptyQueueNoNoise(t *testing.T) {
	savedBudget := webhookDrainBudget
	webhookDrainBudget = 50 * time.Millisecond
	t.Cleanup(func() { webhookDrainBudget = savedBudget })

	src, st, _, _ := newTestSource(t, time.Minute)
	runCtx, cancel := context.WithCancel(context.Background())
	src.StartWebhookWorker(runCtx)
	cancel()
	if err := src.StopWebhookWorker(context.Background()); err != nil {
		t.Fatalf("worker stop: %v", err)
	}
	// 看门狗预算极短，等其触发窗口过后断言零噪音。
	time.Sleep(150 * time.Millisecond)
	if got := interruptedAudits(t, st); len(got) != 0 {
		t.Fatalf("empty-queue shutdown emitted %d interrupted audits (must be silent)", len(got))
	}
}
