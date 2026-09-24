package engine

// MoveApp 换名重部署编排原语的测试（v0.3 W2-S3，rbac-teams §3.4/§4.3；
// internal/state/apps_move_test.go 钉权威态归属语义，本文件钉编排面）：
//   - EnqueueMoveRedeploy：复用上个 succeeded 部署的 ComposePath+SpecHash
//     入队（新命名上下文的发布走既有管线）；无成功部署史 → ErrNoRedeploy
//     Source；审计 app.move_redeploy 同事务落。
//   - AwaitAppSwap：新命名上下文长驻服务就位（running 任务 ≥ 期望副本）→
//     nil；从未发布（可发现集为空）→ 预算耗尽超时错误。
//   - SweepMovedServices：旧命名上下文长驻服务摘除；在途 cron job 按
//     fleetly-cron- 前缀豁免（处置裁决 = 让在途 job 跑完，move.go 文件头）；
//     幂等（缺失视为成功）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// seedSucceededDeploy 驱动 demo 应用走完一次成功发布（服务带新命名上下文
// label 与 running 任务——AwaitAppSwap 的就位判据源）。
func seedSucceededDeploy(t *testing.T, h *harness) state.App {
	t.Helper()
	rec := h.enqueue(h.writeCompose(composeV1))
	if final := h.runToTerminal(rec); final.Status != state.DeploySucceeded {
		t.Fatalf("seed deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	app, err := h.store.GetAppByName(context.Background(), "demo")
	if err != nil {
		t.Fatalf("get demo app: %v", err)
	}
	return app
}

func TestEnqueueMoveRedeployReusesSucceededSource(t *testing.T) {
	h := newHarness(t)
	app := seedSucceededDeploy(t, h)
	source, err := h.store.ListAppDeployments(context.Background(), app.ID, 1)
	if err != nil || len(source) == 0 {
		t.Fatalf("list deployments: %v", err)
	}

	deployID, err := EnqueueMoveRedeploy(context.Background(), h.store, app.ID)
	if err != nil {
		t.Fatalf("EnqueueMoveRedeploy: %v", err)
	}
	row, err := h.store.GetDeployment(context.Background(), deployID)
	if err != nil {
		t.Fatalf("get move redeploy row: %v", err)
	}
	if row.Status != state.DeployQueued {
		t.Errorf("status = %s, want queued", row.Status)
	}
	if row.SpecHash != source[0].SpecHash || row.ComposePath != source[0].ComposePath {
		t.Errorf("redeploy row does not reuse the succeeded source (spec_hash/compose_path diverge)")
	}
	// 审计 app.move_redeploy 同事务（fail-closed——入队与审计一体）。
	audits, err := h.store.RecentAudits(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "app.move_redeploy" {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit app.move_redeploy not recorded by EnqueueMoveRedeploy")
	}
}

func TestEnqueueMoveRedeployWithoutSuccessHistory(t *testing.T) {
	h := newHarness(t)
	// 从未发布过的 app（无部署史）→ 无对象随迁，编排面显性拒绝。
	seeded := h.demoApp()
	if _, err := EnqueueMoveRedeploy(context.Background(), h.store, seeded.ID); !errors.Is(err, ErrNoRedeploySource) {
		t.Fatalf("err = %v, want ErrNoRedeploySource", err)
	}
}

func TestAwaitAppSwapReturnsWhenServicesRunning(t *testing.T) {
	h := newHarness(t)
	app := seedSucceededDeploy(t, h)
	// 真实时钟引擎（等待面轮询真实 500ms 周期——fake 时钟不流动会饿死超时
	// 判定；这里走成功路径，预算 5s 远宽于假底座的即时就位）。
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := NewEngine(Config{}, h.store, h.sub, h.images, h.resolver, h.box, logger)
	if err := eng.AwaitAppSwap(context.Background(), app.ID, 5*time.Second); err != nil {
		t.Fatalf("AwaitAppSwap: %v", err)
	}
}

func TestAwaitAppSwapTimesOutWithoutServices(t *testing.T) {
	h := newHarness(t)
	seeded := h.demoApp() // 从未发布——新命名上下文无可发现服务
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := NewEngine(Config{}, h.store, h.sub, h.images, h.resolver, h.box, logger)
	err := eng.AwaitAppSwap(context.Background(), seeded.ID, 50*time.Millisecond)
	if err == nil {
		t.Fatal("AwaitAppSwap = nil, want timeout error (no services in the new naming context)")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want timeout wording", err)
	}
}

func TestSweepMovedServicesExemptsCronJobsAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	app := seedSucceededDeploy(t, h)
	ctx := context.Background()

	// 旧命名上下文：改派前 slug（oldteam/oldprj）推导的旧名长驻服务与在途
	// cron job 服务——同挂旧限定形 fleetly.app label（SweepMovedServices 的
	// 发现选择器）。
	const oldTeam, oldPrj = "oldteam", "oldprj"
	oldQualified, err := naming.QualifiedName(oldTeam, oldPrj, app.Name)
	if err != nil {
		t.Fatalf("qualified name: %v", err)
	}
	oldSvc, err := naming.ServiceName(oldTeam, oldPrj, app.Name, "web")
	if err != nil {
		t.Fatalf("old service name: %v", err)
	}
	oldJob, err := naming.CronJobName(oldTeam, oldPrj, app.Name, "web", "deadbeef")
	if err != nil {
		t.Fatalf("old cron job name: %v", err)
	}
	labels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     oldQualified,
	}
	for _, name := range []string{oldSvc, oldJob} {
		if err := h.sub.ServiceCreate(ctx, ServiceSpec{Name: name, ServiceLabels: labels}); err != nil {
			t.Fatalf("seed old-context service %s: %v", name, err)
		}
	}

	removed, err := h.eng.SweepMovedServices(ctx, oldQualified)
	if err != nil {
		t.Fatalf("SweepMovedServices: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (long-running only)", removed)
	}
	if _, ok := h.sub.services[oldSvc]; ok {
		t.Error("old long-running service still present after sweep")
	}
	if _, ok := h.sub.services[oldJob]; !ok {
		t.Error("in-flight cron job service was swept (must be exempt: it finishes and is reaped by the scheduler)")
	}
	// 幂等：再扫一轮，缺失视为成功、零移除。
	removed, err = h.eng.SweepMovedServices(ctx, oldQualified)
	if err != nil || removed != 0 {
		t.Fatalf("second sweep = (%d, %v), want (0, nil)", removed, err)
	}
}
