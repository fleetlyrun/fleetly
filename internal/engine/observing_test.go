package engine

// 观察窗成功终态固化的事务性测试（S18-A6）：revision 固化与终态 CAS
// 并入同一事务 + (app_id, desired_hash) 幂等——旧形态两事务间崩溃留下的
// 残行在重放（重启 reopenObserveWindow → 重跑窗口 → 再固化）时不产生
// 重复版本行。

import (
	"context"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// runToStatus 驱动 tick 至指定状态（终态先到即致命——调用方声明的中途态
// 不可达意味着状态机走偏）。
func (h *harness) runToStatus(t *testing.T, rec state.DeployRecord, want state.DeploymentStatus) state.DeployRecord {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 64; i++ {
		row, err := h.store.GetDeployment(ctx, rec.ID)
		if err != nil {
			t.Fatalf("get deployment: %v", err)
		}
		if row.Status == want {
			return row
		}
		if row.Status.Terminal() {
			t.Fatalf("deployment %s reached terminal %s before %s (%s)", rec.ID, row.Status, want, row.ErrorCode)
		}
		h.clk.Advance(2 * time.Second)
		h.eng.Tick(ctx)
	}
	t.Fatalf("deployment %s did not reach %s within 64 ticks", rec.ID, want)
	return state.DeployRecord{}
}

// TestSucceedRevisionIdempotentAfterCrashBetweenTx A6：模拟旧形态两事务间
// 崩溃（revision 已固化、终态 CAS 未落）——新形态重启 reopenObserveWindow
// 重跑窗口后再固化时复用同 hash 既有行，revisions 表同 desired_hash 只有
// 一行、seq 不递增、部署 revision_id 指向复用行。
func TestSucceedRevisionIdempotentAfterCrashBetweenTx(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rec := h.enqueue(h.writeCompose(composeV1))
	obs := h.runToStatus(t, rec, state.DeployObserving)

	// 崩溃残行：旧形态第一个事务的产物（同 desired_hash 已固化）。
	var crashedRevID string
	if err := h.store.InTx(ctx, func(tx *state.Tx) error {
		rev, err := tx.CreateRevision(ctx, state.RevisionWrite{
			AppID:             rec.AppID,
			ComposeNormalized: "{}",
			Overlay:           "{}",
			DesiredHash:       obs.DesiredHash,
		})
		if err != nil {
			return err
		}
		crashedRevID = rev.ID
		return nil
	}); err != nil {
		t.Fatalf("seed crashed revision: %v", err)
	}

	// 控制面重启：reopenObserveWindow 重开完整观察窗（§2.3）。
	h.eng.recoverInterrupted(ctx)

	// 窗口重跑 → 再固化（新形态：复用 + 同事务 CAS）。
	h.clk.Advance(2*time.Second + h.eng.cfg.ObserveWindow)
	h.eng.Tick(ctx)

	final, err := h.store.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if final.Status != state.DeploySucceeded {
		t.Fatalf("status = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	if final.RevisionID != crashedRevID {
		t.Fatalf("revision_id = %s, want reuse of crashed row %s", final.RevisionID, crashedRevID)
	}
	revs, err := h.store.ListRevisions(ctx, rec.AppID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	sameHash := 0
	for _, r := range revs {
		if r.DesiredHash == obs.DesiredHash {
			sameHash++
		}
	}
	if sameHash != 1 {
		t.Fatalf("revisions with same desired_hash = %d, want 1（重放不产生重复行）", sameHash)
	}
	if len(revs) != 1 || revs[0].Seq != 1 {
		t.Fatalf("revisions = %+v, want single seq-1 row（seq 不递增）", revs)
	}
	// 事件不重复（一次固化一次 succeeded）。
	if n := countEvents(t, h, "deployment.succeeded"); n != 1 {
		t.Fatalf("deployment.succeeded count = %d, want 1", n)
	}
}
