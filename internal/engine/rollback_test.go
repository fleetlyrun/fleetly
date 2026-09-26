package engine

// 回滚链路测试（T2.12）：kind=rollback 语义（快照重放 env 随快照、目标
// 固化为新版本、pending env 不被消费）、preflight 分支（镜像缺失 →
// E_IMAGE_UNAVAILABLE + W_ROLLBACK_IMAGE_RISK；不动底座）、
// E_ROLLBACK_NO_TARGET（未知/越界/跨 app）、回滚失败不再二次自动 +
// 收敛 opt-in 强制关闭、事件与审计。

import (
	"context"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// composeV2 与 composeV1 同 app、不同镜像 + env（回滚 v2→v1 的差异面）。
const composeV2WithEnv = `name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    environment:
      ROLLBACK_PROBE: v2-plaintext-value
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`

// rollbackFixture 铺 v1 → v2 两次成功部署，返回 v1/v2 终态行。
func rollbackFixture(t *testing.T, h *harness) (state.DeployRecord, state.DeployRecord) {
	t.Helper()
	pathV1 := h.writeCompose(composeV1)
	rec1 := h.enqueue(pathV1)
	if final := h.runToTerminal(rec1); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s (%s), want succeeded", final.Status, final.ErrorCode)
	} else {
		rec1 = final
	}
	rec2 := h.enqueue(h.writeCompose(composeV2WithEnv))
	if final := h.runToTerminal(rec2); final.Status != state.DeploySucceeded {
		t.Fatalf("v2 = %s (%s), want succeeded", final.Status, final.ErrorCode)
	} else {
		rec2 = final
	}
	return rec1, rec2
}

func TestRollbackReplaysSnapshotWithEnv(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1, v2 := rollbackFixture(t, h)

	// rollback 前置：置一条 pending 平台 env（回滚不消费 pending——
	// env 随快照，promote 只属于显式部署）。
	app, err := h.store.GetAppByName(ctx, "demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if _, err := h.store.SetAppEnv(ctx, app.ID, "PLATFORM_PENDING", "p", "platform", "human"); err != nil {
		t.Fatalf("set pending env: %v", err)
	}

	rec, err := EnqueueRollback(ctx, h.store, RollbackInput{
		AppName:          "demo",
		TargetRevisionID: v1.RevisionID,
		Actor:            "human",
	})
	if err != nil {
		t.Fatalf("enqueue rollback: %v", err)
	}
	if rec.Kind != kindRollback || rec.RecoveryOf != v2.ID {
		t.Fatalf("rollback row kind=%s recovery_of=%s, want rollback/%s", rec.Kind, rec.RecoveryOf, v2.ID)
	}
	final := h.runToTerminal(rec)

	// 完整健康门 + 观察窗后的成功终态。
	if final.Status != state.DeploySucceeded || final.Kind != kindRollback {
		t.Fatalf("rollback = %s (%s) kind=%s, want succeeded/rollback", final.Status, final.ErrorCode, final.Kind)
	}
	if final.FirstHealthyAt.IsZero() {
		t.Fatal("rollback succeeded without switching (first_healthy_at empty)")
	}
	// 服务实况回 v1 spec：镜像回退 + env 键集回退。
	svc := h.sub.services[h.svc("web")]
	v1Specs := decodeForTest(t, h, v1)
	if svc.spec.Image != v1Specs[0].Image {
		t.Fatalf("service image = %s, want v1 %s", svc.spec.Image, v1Specs[0].Image)
	}
	for _, kv := range svc.spec.Env {
		if strings.HasPrefix(kv, "ROLLBACK_PROBE=") {
			t.Fatalf("v2 env survived rollback: %s (env rolls back with the snapshot)", kv)
		}
	}
	// 目标版本固化为新 revision（回滚 = 一次成功部署；哈希与 v1 一致——
	// 同内容重放哈希稳定）。
	rows, err := h.store.ListRevisions(ctx, app.ID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(rows) != 3 || rows[0].ID != final.RevisionID {
		t.Fatalf("revisions = %d rows newest=%s, want 3 rows newest=%s", len(rows), rows[0].ID, final.RevisionID)
	}
	if rows[0].DesiredHash != v1.DesiredHash {
		t.Fatalf("rollback revision hash = %s, want v1 %s (replay hash stable)", rows[0].DesiredHash, v1.DesiredHash)
	}
	// pending env 未被回滚消费。
	pending, err := h.store.GetAppEnv(ctx, app.ID, "PLATFORM_PENDING")
	if err != nil || pending.Status != state.EnvStatusPending {
		t.Fatalf("pending env after rollback = %+v (%v), want pending (rollback does not promote)", pending, err)
	}
	// 事件链 + 审计（actor=human 透传）。
	names := h.events()
	for _, want := range []string{"deployment.rollback_started", "deployment.rollback_finished", "deployment.succeeded"} {
		if !hasEvent(names, want) {
			t.Fatalf("events missing %s: %v", want, names)
		}
	}
	audits, err := h.store.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	foundActorAudit := false
	for _, a := range audits {
		if a.Action == "deployment.rollback" && a.Actor == "human" {
			foundActorAudit = true
		}
	}
	if !foundActorAudit {
		t.Fatal("no deployment.rollback audit with actor=human")
	}
	// app 派生状态 running。
	got, err := h.eng.AppDerivedState(ctx, app.ID)
	if err != nil || got != DerivedRunning {
		t.Fatalf("app state = %s (%v), want running", got, err)
	}
}

func TestRollbackDefaultTargetAndNoTarget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1, v2 := rollbackFixture(t, h)

	// 缺省 --to：最新 revision（v2 的）。
	rec, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo"})
	if err != nil {
		t.Fatalf("default-target rollback: %v", err)
	}
	specs := decodeForTest(t, h, rec)
	v2Specs := decodeForTest(t, h, v2)
	if specs[0].Image != v2Specs[0].Image {
		t.Fatalf("default target = %s, want v2 image %s", specs[0].Image, v2Specs[0].Image)
	}
	if rec.RecoveryOf != v2.ID {
		t.Fatalf("recovery_of = %s, want v2 %s (reverts the current revision)", rec.RecoveryOf, v2.ID)
	}

	// 未知 revision。
	if _, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo", TargetRevisionID: "nope"}); err == nil {
		t.Fatal("unknown revision accepted")
	} else if ae := appErrCodeOf(t, err); ae != "E_ROLLBACK_NO_TARGET" {
		t.Fatalf("unknown revision code = %s, want E_ROLLBACK_NO_TARGET", ae)
	}
	// 未知 app。
	if _, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "ghost"}); err == nil {
		t.Fatal("unknown app accepted")
	} else if ae := appErrCodeOf(t, err); ae != "E_ROLLBACK_NO_TARGET" {
		t.Fatalf("unknown app code = %s, want E_ROLLBACK_NO_TARGET", ae)
	}
	// 越界：seed 6 个 revision 把 v1/v2 的挤出保留窗。
	app, _ := h.store.GetAppByName(ctx, "demo")
	createRevisionsForTest(t, h, app.ID, 6)
	if _, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo", TargetRevisionID: v1.RevisionID}); err == nil {
		t.Fatal("out-of-window revision accepted")
	} else if ae := appErrCodeOf(t, err); ae != "E_ROLLBACK_NO_TARGET" {
		t.Fatalf("out-of-window code = %s, want E_ROLLBACK_NO_TARGET", ae)
	}
}

func TestRollbackPreflightImageMissing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1, v2 := rollbackFixture(t, h)
	v1Specs := decodeForTest(t, h, v1)
	// 目标镜像在本机缺失（v0.1 无 registry：镜像被清理）。
	h.images.missing[v1Specs[0].Image] = true

	rec, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo", TargetRevisionID: v1.RevisionID})
	if err != nil {
		t.Fatalf("enqueue rollback: %v", err)
	}
	final := h.runToTerminal(rec)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_IMAGE_UNAVAILABLE" {
		t.Fatalf("rollback = %s (%s), want failed E_IMAGE_UNAVAILABLE", final.Status, final.ErrorCode)
	}
	if !hasEvent(h.events(), "deployment.rollback_failed") {
		t.Fatal("missing deployment.rollback_failed (a preflight failure is also a rollback failure)")
	}
	// 不动底座：服务实况仍是 v2 镜像。
	v2Specs := decodeForTest(t, h, v2)
	if got := h.sub.services[h.svc("web")].spec.Image; got != v2Specs[0].Image {
		t.Fatalf("service image = %s, want untouched v2 %s", got, v2Specs[0].Image)
	}
	// preflight 失败不关收敛 opt-in（app 未被动过）。
	on, err := h.store.GetAppDriftConverge(ctx, v2.AppID)
	if err != nil || on {
		t.Fatalf("drift converge = %v (%v), want false (preflight failure does not force-disable)", on, err)
	}
	// E_IMAGE_UNAVAILABLE 信封携带 W_ROLLBACK_IMAGE_RISK（经
	// build.PreflightImage 的 context.warning 透传）。
	pfErr := h.eng.preflightRollback(ctx, v2, v1Specs)
	if pfErr == nil {
		t.Fatal("preflight passed with missing image")
	}
	ae := appErrOf(pfErr, "")
	if ae == nil || ae.Code() != "E_IMAGE_UNAVAILABLE" {
		t.Fatalf("preflight code = %v, want E_IMAGE_UNAVAILABLE", ae)
	}
	if got := ae.Envelope().GetContext()["warning"]; got != "W_ROLLBACK_IMAGE_RISK" {
		t.Fatalf("preflight warning context = %q, want W_ROLLBACK_IMAGE_RISK", got)
	}
}

func TestRollbackFailureNoSecondAutoAndConvergeForceOff(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1, v2 := rollbackFixture(t, h)

	// 收敛 opt-in 预先开启（回滚失败必须强制关闭直至人工重置）。
	if err := h.store.InTx(ctx, func(tx *state.Tx) error {
		return tx.SetAppDriftConverge(ctx, v2.AppID, true)
	}); err != nil {
		t.Fatalf("enable converge: %v", err)
	}

	v1Specs := decodeForTest(t, h, v1)
	h.sub.setMode(h.svc("web"), modePausedHealth) // 回放更新失败（pause 冻结）
	rec, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo", TargetRevisionID: v1.RevisionID})
	if err != nil {
		t.Fatalf("enqueue rollback: %v", err)
	}
	final := h.runToTerminal(rec)

	if final.Status != state.DeployFailed || final.ErrorCode != "E_ROLLBACK_FAILED" {
		t.Fatalf("rollback = %s (%s), want failed E_ROLLBACK_FAILED", final.Status, final.ErrorCode)
	}
	if final.Recovery != "" {
		t.Fatalf("recovery = %s, want empty (rollback failure gets no second automatic recovery)", final.Recovery)
	}
	// 不再二次自动：失败后没有任何进一步的 ServiceUpdate（updates 的最后
	// 一次即失败的那次回放，目标 v1 镜像）。
	updates := h.sub.updates
	if len(updates) == 0 || updates[len(updates)-1][1] != v1Specs[0].Image {
		t.Fatalf("last update = %v, want the failed v1 replay", updates[len(updates)-1])
	}
	// 收敛 opt-in 强制关闭 + 禁用审计。
	on, err := h.store.GetAppDriftConverge(ctx, v2.AppID)
	if err != nil || on {
		t.Fatalf("drift converge = %v (%v), want force-disabled", on, err)
	}
	audits, err := h.store.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	disabledAudit := false
	for _, a := range audits {
		if a.Action == "reconcile.drift_converge_disabled" && a.Actor == "system" {
			disabledAudit = true
		}
	}
	if !disabledAudit {
		t.Fatal("no reconcile.drift_converge_disabled audit")
	}
	if !hasEvent(h.events(), "deployment.rollback_failed") {
		t.Fatal("missing deployment.rollback_failed")
	}
	// 人工重置入口可用（enable 再置位）。
	if err := h.eng.SetDriftConverge(ctx, "demo", true, "human"); err != nil {
		t.Fatalf("manual reset: %v", err)
	}
	if on, _ := h.store.GetAppDriftConverge(ctx, v2.AppID); !on {
		t.Fatal("manual reset did not re-enable")
	}
}

func TestRollbackObserveWindowFailureAlsoClosesConverge(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	v1, v2 := rollbackFixture(t, h)
	if err := h.store.InTx(ctx, func(tx *state.Tx) error {
		return tx.SetAppDriftConverge(ctx, v2.AppID, true)
	}); err != nil {
		t.Fatalf("enable converge: %v", err)
	}
	rec, err := EnqueueRollback(ctx, h.store, RollbackInput{AppName: "demo", TargetRevisionID: v1.RevisionID})
	if err != nil {
		t.Fatalf("enqueue rollback: %v", err)
	}
	// 推进到观察窗后注入崩溃循环（切流后的回滚失败）。
	for i := 0; i < 16; i++ {
		row, _ := h.store.GetDeployment(ctx, rec.ID)
		if row.Status == state.DeployObserving {
			break
		}
		h.eng.Tick(ctx)
	}
	h.sub.crashNewRunning(h.svc("web"), 2, h.clk.Now())
	final := h.runToTerminal(rec)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_ROLLBACK_FAILED" {
		t.Fatalf("rollback = %s (%s), want failed E_ROLLBACK_FAILED (post-switch failure still counts as a rollback failure)", final.Status, final.ErrorCode)
	}
	if final.Verdict != state.VerdictUnstable {
		t.Fatalf("verdict = %q, want unstable", final.Verdict)
	}
	if on, _ := h.store.GetAppDriftConverge(ctx, v2.AppID); on {
		t.Fatal("drift converge not force-disabled after switched rollback failure")
	}
	got, _ := h.eng.AppDerivedState(ctx, v2.AppID)
	if got != DerivedDegraded {
		t.Fatalf("app state = %s, want degraded", got)
	}
}

// ── 测试助手 ────────────────────────────────────────────────────────────────

// decodeForTest 解密并解码部署行期望态快照（测试面）。
func decodeForTest(t *testing.T, h *harness, rec state.DeployRecord) []ServiceSpec {
	t.Helper()
	specs, err := h.eng.decodeSpecs(rec)
	if err != nil {
		t.Fatalf("decode specs of %s: %v", rec.ID, err)
	}
	return specs
}

// createRevisionsForTest 直接固化 n 个 revision（保留窗越界用例；n ≤ 6，
// 名字取 abcdef 字母序）。
func createRevisionsForTest(t *testing.T, h *harness, appID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		letter := "abcdef"[i : i+1]
		if err := h.store.InTx(context.Background(), func(tx *state.Tx) error {
			_, err := tx.CreateRevision(context.Background(), state.RevisionWrite{
				AppID:             appID,
				ComposeNormalized: "{}",
				Overlay:           "{}",
				DesiredHash:       "seeded-" + letter,
			})
			return err
		}); err != nil {
			t.Fatalf("seed revision: %v", err)
		}
	}
}

// appErrCodeOf 提取错误的注册表错误码（断言辅助）。
func appErrCodeOf(t *testing.T, err error) string {
	t.Helper()
	ae := appErrOf(err, "")
	if ae == nil {
		t.Fatalf("error %v carries no apperr envelope", err)
	}
	return ae.Code()
}
