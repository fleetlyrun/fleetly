package engine

// 发布引擎主链路测试：状态机全链（真实 compose fixture + 假底座/假放置/
// 假时钟）——互斥、env 三层合并与 pending promote、失败分流两分支、首发
// scale=0、stop-first 停机账、观察窗判定、cancel 语义、重启恢复、L4 只
// 告警一次、场景矩阵错误码。

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// harness 是一只测试环境（store + box + 假底座/解析器/时钟）。
type harness struct {
	t        *testing.T
	store    *state.Store
	box      *secrets.Box
	sub      *fakeSubstrate
	resolver *fakeResolver
	images   *fakeImages
	clk      *fakeClock
	eng      *Engine
}

var testStart = time.Now().UTC().Truncate(time.Second)

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "fleetly.key"))
	if err != nil {
		t.Fatalf("ensure key: %v", err)
	}
	sub := newFakeSubstrate()
	res := &fakeResolver{store: st}
	images := &fakeImages{missing: map[string]bool{}}
	clk := newFakeClock(testStart)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := NewEngine(Config{}, st, sub, images, res, box, logger).WithClock(clk)
	return &harness{t: t, store: st, box: box, sub: sub, resolver: res, images: images, clk: clk, eng: eng}
}

// writeCompose 落一份 compose fixture 并返回绝对路径。
func (h *harness) writeCompose(content string) string {
	h.t.Helper()
	path := filepath.Join(h.t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatalf("write compose: %v", err)
	}
	return path
}

// enqueue 以 CLI 同构语义入队（deployment.queued 事件 + deployment.create
// 审计与建行同事务）。
func (h *harness) enqueue(composePath string) state.DeployRecord {
	h.t.Helper()
	ctx := context.Background()
	app, err := ensureAppForTest(ctx, h.store, "demo")
	if err != nil {
		h.t.Fatalf("ensure app: %v", err)
	}
	rec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID:       app.ID,
		AppName:     app.Name,
		Kind:        "deploy",
		SpecHash:    specHashOf(composePath),
		ComposePath: composePath,
	})
	if err != nil {
		h.t.Fatalf("create deployment: %v", err)
	}
	if err := h.store.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.AppendEvent(ctx, state.Event{Name: "deployment.queued", Subject: "deployment:" + rec.ID})
		return err
	}); err != nil {
		h.t.Fatalf("enqueue event: %v", err)
	}
	return rec
}

// runToTerminal 驱动 tick 至终态（上限 64 拍防死循环）。每拍推进假时钟
// 2s（真实时钟自行流动；2s×64=128s 覆盖观察窗且不触 300s 看门狗）。
func (h *harness) runToTerminal(rec state.DeployRecord) state.DeployRecord {
	h.t.Helper()
	ctx := context.Background()
	for i := 0; i < 64; i++ {
		row, err := h.store.GetDeployment(ctx, rec.ID)
		if err != nil {
			h.t.Fatalf("get deployment: %v", err)
		}
		if row.Status.Terminal() {
			return row
		}
		h.clk.Advance(2 * time.Second)
		h.eng.Tick(ctx)
	}
	h.t.Fatalf("deployment %s did not reach terminal state within 64 ticks", rec.ID)
	return state.DeployRecord{}
}

// events 返回至今的全部事件名序列。
func (h *harness) events() []string {
	h.t.Helper()
	rows, err := h.store.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		h.t.Fatalf("events: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, e := range rows {
		out = append(out, e.Name)
	}
	return out
}

func hasEvent(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// ensureAppForTest / specHashOf 是测试侧的 CLI 同构助手（compose.Load 校验
// 后取 spec_hash；应用不存在则创建）。
func ensureAppForTest(ctx context.Context, st *state.Store, name string) (state.App, error) {
	if app, err := st.GetAppByName(ctx, name); err == nil {
		return app, nil
	} else if err != state.ErrAppNotFound {
		return state.App{}, err
	}
	return st.CreateApp(ctx, "", name)
}

func specHashOf(path string) string {
	// 与 CLI enqueue 同构：compose.Load 的归一化哈希。
	spec, _, err := compose.Load(context.Background(), path)
	if err != nil {
		panic(err)
	}
	return spec.SpecHash
}

// placementUnavailable / placementGone 构造放置前哨错误（场景 15/16）。
func placementUnavailable() error {
	return apperr.New("E_PLACEMENT_NODE_UNAVAILABLE", "绑定节点非 ready（测试注入）")
}

func placementGone() error {
	return apperr.New("E_PLACEMENT_NODE_GONE", "绑定节点已移除（测试注入）")
}

// compose fixtures（受控子集内的最小形态；alpine + 真 healthcheck 语义由
// 假底座承载，实机验证用真实 daemon）。
const composeV1 = `name: demo
services:
  web:
    image: alpine:3
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`

func TestFirstDeploySucceeds(t *testing.T) {
	h := newHarness(t)
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	final := h.runToTerminal(rec)

	if final.Status != state.DeploySucceeded {
		t.Fatalf("status = %s error=%s, want succeeded", final.Status, final.ErrorCode)
	}
	if final.RevisionID == "" {
		t.Fatal("revision_id empty on success")
	}
	// 服务存在 + managed label + 前缀命名 + fleetly.app 容器 label。
	svc, err := h.sub.ServiceInspect(context.Background(), "fleetly-demo-web")
	if err != nil {
		t.Fatalf("service inspect: %v", err)
	}
	if svc.Labels[state.LabelManaged] != "true" || svc.Labels[state.LabelApp] != "demo" ||
		svc.Labels[state.LabelProcess] != "web" {
		t.Fatalf("service labels = %v, want managed/app/process set", svc.Labels)
	}
	if svc.Labels[state.LabelDeployment] != rec.ID {
		t.Fatalf("deployment label = %s, want %s", svc.Labels[state.LabelDeployment], rec.ID)
	}
	if svc.DesiredHash == "" {
		t.Fatal("desired-hash label empty")
	}
	// 事件链完整。
	names := h.events()
	for _, want := range []string{"deployment.queued", "deployment.release_started",
		"deployment.healthy", "deployment.switched", "deployment.observe_started",
		"deployment.succeeded"} {
		if !hasEvent(names, want) {
			t.Fatalf("events missing %s: %v", want, names)
		}
	}
	// app 派生状态 = running。
	got, err := h.eng.AppDerivedState(context.Background(), rec.AppID)
	if err != nil {
		t.Fatalf("derived state: %v", err)
	}
	if got != DerivedRunning {
		t.Fatalf("app state = %s, want running", got)
	}
}

func TestDeployMutexSecondStaysQueued(t *testing.T) {
	h := newHarness(t)
	path := h.writeCompose(composeV1)
	first := h.enqueue(path)
	second := h.enqueue(path)

	ctx := context.Background()
	h.eng.Tick(ctx) // 第一条应被启动，第二条必须保持 queued

	row1, _ := h.store.GetDeployment(ctx, first.ID)
	row2, _ := h.store.GetDeployment(ctx, second.ID)
	if row1.Status == state.DeployQueued {
		t.Fatalf("first deployment still queued: %s", row1.Status)
	}
	if row2.Status != state.DeployQueued {
		t.Fatalf("second deployment status = %s, want queued（互斥等待）", row2.Status)
	}
}

func TestFailureUnswitchedRestoresPreviousVersion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// v1：成功部署（有效版本在位）。
	pathV1 := h.writeCompose(composeV1)
	rec1 := h.enqueue(pathV1)
	if final := h.runToTerminal(rec1); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	v1Image := h.sub.services["fleetly-demo-web"].spec.Image
	v1Running := h.runningTaskIDs("fleetly-demo-web", v1Image)
	if len(v1Running) == 0 {
		t.Fatal("no running v1 tasks after success")
	}

	// v2：健康门永不通过（改 image 触发更新 + paused-health 行为）。
	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "false"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePausedHealth)
	rec2 := h.enqueue(pathV2)
	final := h.runToTerminal(rec2)

	if final.Status != state.DeployFailed || final.ErrorCode != "E_HEALTH_TIMEOUT" {
		t.Fatalf("v2 deploy = %s (%s), want failed E_HEALTH_TIMEOUT", final.Status, final.ErrorCode)
	}
	if final.Recovery != state.RecoveryRestore {
		t.Fatalf("recovery = %q, want restore（未切流同记录归位）", final.Recovery)
	}
	if !final.FirstHealthyAt.IsZero() {
		t.Fatal("first_healthy_at set on unswitched failure")
	}
	// 归位重放：最后有效 spec 被重新应用（ServiceUpdate 调用带 v1 镜像）。
	found := false
	for _, u := range h.sub.updates {
		if u[0] == "fleetly-demo-web" && u[1] == v1Image {
			found = true
		}
	}
	if !found {
		t.Fatalf("no restore update with v1 image %s: %v", v1Image, h.sub.updates)
	}
	// 归位零任务替换（同内容重放：v1 运行任务 id 不变、零新增运行任务，
	// Spike B2 同构断言）。
	after := h.runningTaskIDs("fleetly-demo-web", v1Image)
	if !equalSets(after, v1Running) {
		t.Fatalf("running v1 tasks changed after restore: %v -> %v", v1Running, after)
	}
	// 旧版本持续服务（failed 任务在列但 running 旧任务仍在）。
	names := h.events()
	if !hasEvent(names, "deployment.failed") {
		t.Fatalf("events missing deployment.failed: %v", names)
	}
	// app 派生状态：旧版本仍在服务、无 verdict → running。
	got, _ := h.eng.AppDerivedState(ctx, rec2.AppID)
	if got != DerivedRunning {
		t.Fatalf("app state = %s, want running", got)
	}
}

func TestFirstDeployFailureScalesToZero(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	h.sub.setMode("fleetly-demo-web", modePausedStart)
	rec := h.enqueue(path)
	final := h.runToTerminal(rec)

	if final.Status != state.DeployFailed || final.ErrorCode != "E_TASK_START_FAILED" {
		t.Fatalf("deploy = %s (%s), want failed E_TASK_START_FAILED", final.Status, final.ErrorCode)
	}
	if !final.SubstrateHalted {
		t.Fatal("substrate_halted not set（首发失败 scale=0 保留现场）")
	}
	svc, err := h.sub.ServiceInspect(ctx, "fleetly-demo-web")
	if err != nil {
		t.Fatalf("service should exist (scale=0 保留现场): %v", err)
	}
	if svc.Replicas != 0 {
		t.Fatalf("replicas = %d, want 0", svc.Replicas)
	}
	if !hasEvent(h.events(), "deployment.substrate_halted") {
		t.Fatal("missing deployment.substrate_halted event")
	}
	got, _ := h.eng.AppDerivedState(ctx, rec.AppID)
	if got != DerivedDown {
		t.Fatalf("app state = %s, want down（无有效版本）", got)
	}
}

func TestObserveCrashLoopFailsUnstable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	// 推进到切流（releasing）。
	for i := 0; i < 16; i++ {
		row, _ := h.store.GetDeployment(ctx, rec.ID)
		if row.Status == state.DeployObserving {
			break
		}
		h.eng.Tick(ctx)
	}
	// 注入崩溃循环（≥2 次退出）。
	h.sub.crashNewRunning("fleetly-demo-web", 2, h.clk.Now())
	final := h.runToTerminal(rec)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_OBSERVE_CRASH_LOOP" {
		t.Fatalf("deploy = %s (%s), want failed E_OBSERVE_CRASH_LOOP", final.Status, final.ErrorCode)
	}
	if final.Verdict != state.VerdictUnstable {
		t.Fatalf("verdict = %q, want unstable", final.Verdict)
	}
	got, _ := h.eng.AppDerivedState(ctx, rec.AppID)
	if got != DerivedDegraded {
		t.Fatalf("app state = %s, want degraded", got)
	}
	names := h.events()
	if !hasEvent(names, "app.degraded") {
		t.Fatalf("missing app.degraded: %v", names)
	}
}

func TestObserveWindowPassesAfterFreshWindow(t *testing.T) {
	h := newHarness(t)
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	row := h.runToTerminal(rec)
	if row.Status != state.DeploySucceeded {
		t.Fatalf("status = %s (%s)", row.Status, row.ErrorCode)
	}
	// 观察窗确实走满默认 60s（fake clock 推进量 > observe window）。
	if h.clk.Now().Sub(testStart) < 60*time.Second {
		t.Fatalf("clock advanced only %s, expect >= observe window", h.clk.Now().Sub(testStart))
	}
}

func TestObserveSingleCrashSelfHealsWarns(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	for i := 0; i < 16; i++ {
		row, _ := h.store.GetDeployment(ctx, rec.ID)
		if row.Status == state.DeployObserving {
			break
		}
		h.eng.Tick(ctx)
	}
	// 单次退出（Swarm 重启自愈：运行任务保留、失败记录在列）。
	h.sub.crashNewRunning("fleetly-demo-web", 1, h.clk.Now())
	final := h.runToTerminal(rec)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("status = %s (%s), want succeeded（警告通过）", final.Status, final.ErrorCode)
	}
	if final.Flags&state.DeployFlagInstabilityWarning == 0 {
		t.Fatal("instability warning flag not set")
	}
	if !hasEvent(h.events(), "deployment.warning") {
		t.Fatal("missing deployment.warning event")
	}
}

func TestCancelUnswitchedRestoresThenCancels(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pathV1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s", final.Status)
	}
	v1Image := h.sub.services["fleetly-demo-web"].spec.Image

	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePending) // 迟迟未切流
	rec2 := h.enqueue(pathV2)
	for i := 0; i < 8; i++ {
		row, _ := h.store.GetDeployment(ctx, rec2.ID)
		if row.Status == state.DeployReleasing {
			break
		}
		h.eng.Tick(ctx)
	}
	eng := h.eng
	if err := eng.CancelRequest(ctx, mustGet(h, rec2.ID)); err != nil {
		t.Fatalf("cancel request: %v", err)
	}
	final := h.runToTerminal(rec2)
	if final.Status != state.DeployCancelled {
		t.Fatalf("status = %s (%s), want cancelled", final.Status, final.ErrorCode)
	}
	if final.Recovery != state.RecoveryRestore {
		t.Fatalf("recovery = %q, want restore", final.Recovery)
	}
	found := false
	for _, u := range h.sub.updates {
		if u[1] == v1Image {
			found = true
		}
	}
	if !found {
		t.Fatal("cancel did not restore previous version before terminal")
	}
	if !hasEvent(h.events(), "deployment.cancelled") {
		t.Fatal("missing deployment.cancelled event")
	}
}

func TestCancelRejectedAfterSwitch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	for i := 0; i < 16; i++ {
		row, _ := h.store.GetDeployment(ctx, rec.ID)
		if row.Status == state.DeployObserving {
			break
		}
		h.eng.Tick(ctx)
	}
	err := h.eng.CancelRequest(ctx, mustGet(h, rec.ID))
	if err == nil {
		t.Fatal("cancel after switch accepted, want 409")
	}
	var ae *apperr.Error
	if !asAppErr(err, &ae) || ae == nil {
		t.Fatalf("cancel error not apperr: %v", err)
	}
	if ae.Code() != "E_STATE_VERSION_CONFLICT" {
		t.Fatalf("cancel code = %s, want E_STATE_VERSION_CONFLICT (409)", ae.Code())
	}
}

func TestWatchdogPendingTimeoutRestores(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pathV1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s", final.Status)
	}
	v1Image := h.sub.services["fleetly-demo-web"].spec.Image

	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePending)
	rec2 := h.enqueue(pathV2)
	for i := 0; i < 8 && !h.releasing(rec2.ID); i++ {
		h.eng.Tick(ctx)
	}
	// 推进时钟越过看门狗 deadline（300s）。
	h.clk.Advance(301 * time.Second)
	final := h.runToTerminal(rec2)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_SCHEDULER_PENDING_TIMEOUT" {
		t.Fatalf("deploy = %s (%s), want failed E_SCHEDULER_PENDING_TIMEOUT", final.Status, final.ErrorCode)
	}
	if final.Recovery != state.RecoveryRestore {
		t.Fatalf("recovery = %q, want restore", final.Recovery)
	}
	found := false
	for _, u := range h.sub.updates {
		if u[1] == v1Image {
			found = true
		}
	}
	if !found {
		t.Fatal("watchdog timeout did not restore previous version")
	}
}

func TestBlockedWaitingPausesWatchdogAndResumes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pathV1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s", final.Status)
	}
	// v2 部署中绑定节点 DOWN（场景 15）。
	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePending)
	rec2 := h.enqueue(pathV2)
	for i := 0; i < 8 && !h.releasing(rec2.ID); i++ {
		h.eng.Tick(ctx)
	}
	h.resolver.preflightErrs = []error{placementUnavailable()}
	h.eng.Tick(ctx)
	row, _ := h.store.GetDeployment(ctx, rec2.ID)
	if row.Phase != state.PhaseBlockedWaiting {
		t.Fatalf("phase = %q, want blocked_waiting", row.Phase)
	}
	deadline := row.WatchdogDeadlineAt

	// blocked 期间时钟大幅推进（10min > 300s 看门狗）：看门狗暂停、不判死。
	h.clk.Advance(10 * time.Minute)
	h.eng.Tick(ctx)
	row, _ = h.store.GetDeployment(ctx, rec2.ID)
	if row.Status != state.DeployReleasing {
		t.Fatalf("watchdog fired during blocked_waiting: %s (%s)", row.Status, row.ErrorCode)
	}
	if !hasEvent(h.events(), "deployment.recovery_blocked") {
		t.Fatal("missing deployment.recovery_blocked event")
	}
	if !hasEvent(h.events(), "placement.blocked") {
		t.Fatal("missing placement.blocked event")
	}

	// 节点恢复：退出 blocked_waiting、看门狗重新起算（重置）。
	h.eng.Tick(ctx)
	row, _ = h.store.GetDeployment(ctx, rec2.ID)
	if row.Phase != "" {
		t.Fatalf("phase = %q, want cleared", row.Phase)
	}
	if !row.WatchdogDeadlineAt.After(deadline) {
		t.Fatalf("deadline not re-armed after resume: %v -> %v", deadline, row.WatchdogDeadlineAt)
	}
	if !hasEvent(h.events(), "placement.recovered") {
		t.Fatal("missing placement.recovered event")
	}
}

func TestBoundNodeRemovedFailsDeployment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pathV1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s", final.Status)
	}
	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePending)
	rec2 := h.enqueue(pathV2)
	for i := 0; i < 8 && !h.releasing(rec2.ID); i++ {
		h.eng.Tick(ctx)
	}
	h.resolver.preflightErrs = []error{placementGone()}
	final := h.runToTerminal(rec2)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_PLACEMENT_NODE_GONE" {
		t.Fatalf("deploy = %s (%s), want failed E_PLACEMENT_NODE_GONE", final.Status, final.ErrorCode)
	}
}

func TestRestartRecoveryClassifiesReleasing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pathV1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s", final.Status)
	}
	v1Image := h.sub.services["fleetly-demo-web"].spec.Image

	pathV2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:4
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	h.sub.setMode("fleetly-demo-web", modePausedHealth)
	rec2 := h.enqueue(pathV2)
	for i := 0; i < 8 && !h.releasing(rec2.ID); i++ {
		h.eng.Tick(ctx)
	}
	// 控制面重启：新引擎实例（同 store/底座）执行启动扫描分类恢复。
	eng2 := NewEngine(Config{}, h.store, h.sub, h.images, h.resolver, h.box, slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(h.clk)
	eng2.recoverInterrupted(ctx)
	final := h.runToTerminalWith(rec2, eng2)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_HEALTH_TIMEOUT" {
		t.Fatalf("recovered deploy = %s (%s), want classified failure E_HEALTH_TIMEOUT", final.Status, final.ErrorCode)
	}
	if final.Recovery != state.RecoveryRestore {
		t.Fatalf("recovery = %q, want restore", final.Recovery)
	}
	found := false
	for _, u := range h.sub.updates {
		if u[1] == v1Image {
			found = true
		}
	}
	if !found {
		t.Fatal("recovery classification did not restore previous version")
	}
}

func TestRestartRecoveryReopensObserveWindow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	for i := 0; i < 16 && !h.observing(rec.ID); i++ {
		h.eng.Tick(ctx)
	}
	before, _ := h.store.GetDeployment(ctx, rec.ID)
	// 时钟前进（重启恢复发生在原观察窗开启之后）。
	h.clk.Advance(5 * time.Second)
	// 控制面重启：健康 → 重开完整观察窗。
	eng2 := NewEngine(Config{}, h.store, h.sub, h.images, h.resolver, h.box, slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(h.clk)
	eng2.recoverInterrupted(ctx)
	after, _ := h.store.GetDeployment(ctx, rec.ID)
	if !after.ObserveStartedAt.After(before.ObserveStartedAt) {
		t.Fatalf("observe window not reopened: %v -> %v", before.ObserveStartedAt, after.ObserveStartedAt)
	}
	final := h.runToTerminalWith(rec, eng2)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("status = %s (%s), want succeeded after reopened window", final.Status, final.ErrorCode)
	}
}

func TestReconcileRemovesDroppedService(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 双服务部署。
	path2 := h.writeCompose(`name: demo
services:
  web:
    image: alpine:3
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
  worker:
    image: alpine:3
    command: ["sleep", "infinity"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      timeout: 1s
      retries: 2
      start_period: 1s
`)
	if final := h.runToTerminal(h.enqueue(path2)); final.Status != state.DeploySucceeded {
		t.Fatalf("two-service deploy = %s (%s)", final.Status, final.ErrorCode)
	}
	if _, err := h.sub.ServiceInspect(ctx, "fleetly-demo-worker"); err != nil {
		t.Fatalf("worker service missing: %v", err)
	}
	// 移除 worker 再部署 → 对账删除（省略=删除）。
	path1 := h.writeCompose(composeV1)
	if final := h.runToTerminal(h.enqueue(path1)); final.Status != state.DeploySucceeded {
		t.Fatalf("reduced deploy = %s (%s)", final.Status, final.ErrorCode)
	}
	if _, err := h.sub.ServiceInspect(ctx, "fleetly-demo-worker"); err != ErrServiceNotFound {
		t.Fatalf("worker service should be removed, got %v", err)
	}
	if len(h.sub.removed) == 0 || h.sub.removed[len(h.sub.removed)-1] != "fleetly-demo-worker" {
		t.Fatalf("removed = %v, want fleetly-demo-worker", h.sub.removed)
	}
}

func TestEnvPendingPromotedOnSuccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 平台 env pending（密文入库——CLI 同构）。
	app, err := ensureAppForTest(ctx, h.store, "demo")
	if err != nil {
		t.Fatalf("ensure app: %v", err)
	}
	plain := []byte("v=1")
	ct, err := h.box.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := h.store.SetAppEnv(ctx, app.ID, "DEMO_TOKEN", string(ct), "platform"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	path := h.writeCompose(composeV1)
	final := h.runToTerminal(h.enqueue(path))
	if final.Status != state.DeploySucceeded {
		t.Fatalf("deploy = %s (%s)", final.Status, final.ErrorCode)
	}
	// pending → effective。
	row, err := h.store.GetAppEnv(ctx, app.ID, "DEMO_TOKEN")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if row.Status != state.EnvStatusEffective {
		t.Fatalf("env status = %s, want effective（随部署生效）", row.Status)
	}
	// 合并结果注入容器 env（key 在列；值不进断言输出）。
	env := h.sub.services["fleetly-demo-web"].spec.Env
	found := false
	for _, kv := range env {
		if len(kv) > 10 && kv[:10] == "DEMO_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DEMO_TOKEN not injected into service env: %d entries", len(env))
	}
}

func TestStopFirstDowntimeAccounted(t *testing.T) {
	h := newHarness(t)
	// 有卷服务：order 强制 stop-first（配置在 fixture 中声明命名卷）。
	pathV1 := h.writeCompose(`name: demo
services:
  db:
    image: alpine:3
    command: ["sleep", "infinity"]
    volumes:
      - data:/data
volumes:
  data:
`)
	if final := h.runToTerminal(h.enqueue(pathV1)); final.Status != state.DeploySucceeded {
		t.Fatalf("v1 = %s (%s)", final.Status, final.ErrorCode)
	}
	spec := h.sub.services["fleetly-demo-db"].spec
	if spec.UpdateOrder != "stop-first" {
		t.Fatalf("volume service order = %q, want stop-first（平台强制）", spec.UpdateOrder)
	}
	v1Image := spec.Image

	pathV2 := h.writeCompose(`name: demo
services:
  db:
    image: alpine:4
    command: ["sleep", "infinity"]
    volumes:
      - data:/data
volumes:
  data:
`)
	h.sub.setMode("fleetly-demo-db", modePausedStart)
	rec2 := h.enqueue(pathV2)
	final := h.runToTerminal(rec2)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_TASK_START_FAILED" {
		t.Fatalf("deploy = %s (%s), want failed E_TASK_START_FAILED", final.Status, final.ErrorCode)
	}
	if final.Recovery != state.RecoveryRestore {
		t.Fatalf("recovery = %q, want restore（stop-first 强制归位）", final.Recovery)
	}
	// 停机账（§2.6 如实累计）：起止时间戳齐备，ms 与起止差一致。假时钟单
	// tick 内不流动 → 差值为 0 合法；实机 stop-first 归位 10–12s 量级
	//（Spike B），ms 如实为正。
	if final.DowntimeStartedAt.IsZero() || final.DowntimeEndedAt.IsZero() {
		t.Fatal("downtime timestamps missing")
	}
	want := final.DowntimeEndedAt.Sub(final.DowntimeStartedAt).Milliseconds()
	if final.DowntimeMS != want {
		t.Fatalf("downtime_ms = %d, want %d（= ended-started，如实累计）", final.DowntimeMS, want)
	}
	found := false
	for _, u := range h.sub.updates {
		if u[1] == v1Image {
			found = true
		}
	}
	if !found {
		t.Fatal("stop-first failure did not force-restore previous version")
	}
}

func TestPostWindowUnstableAlertsOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := h.writeCompose(composeV1)
	rec := h.enqueue(path)
	if final := h.runToTerminal(rec); final.Status != state.DeploySucceeded {
		t.Fatalf("deploy = %s", final.Status)
	}
	// 窗后崩溃（L4）：注入 failed 且时间戳晚于窗末。
	h.clk.Advance(30 * time.Second)
	h.sub.crashNewRunning("fleetly-demo-web", 1, h.clk.Now())
	h.eng.Tick(ctx)
	got, _ := h.eng.AppDerivedState(ctx, rec.AppID)
	if got != DerivedDegraded {
		t.Fatalf("app state = %s, want degraded（窗后不稳定）", got)
	}
	names := h.events()
	if !hasEvent(names, "app.instability_detected") || !hasEvent(names, "deployment.warning") {
		t.Fatalf("missing post-window events: %v", names)
	}
	// 只告警一次：再次巡检不重复。
	before := len(h.events())
	h.eng.Tick(ctx)
	if after := len(h.events()); after != before {
		t.Fatalf("post-window alert repeated: %d -> %d", before, after)
	}
}

func TestImageMissingFailsFast(t *testing.T) {
	h := newHarness(t)
	path := h.writeCompose(composeV1)
	h.images.missing["alpine:3"] = true
	rec := h.enqueue(path)
	final := h.runToTerminal(rec)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_IMAGE_PULL_FAILED" {
		t.Fatalf("deploy = %s (%s), want failed E_IMAGE_PULL_FAILED", final.Status, final.ErrorCode)
	}
	// 不动底座：无服务创建。
	if len(h.sub.services) != 0 {
		t.Fatalf("substrate touched on preflight failure: %v", h.sub.services)
	}
}

func TestBuildMissingFailsWithBuildCode(t *testing.T) {
	h := newHarness(t)
	path := h.writeCompose(`name: demo
services:
  web:
    build:
      context: .
    command: ["sleep", "infinity"]
`)
	rec := h.enqueue(path)
	final := h.runToTerminal(rec)
	if final.Status != state.DeployFailed || final.ErrorCode != "E_BUILD_FAILED" {
		t.Fatalf("deploy = %s (%s), want failed E_BUILD_FAILED（无匹配构建）", final.Status, final.ErrorCode)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func (h *harness) releasing(id string) bool {
	row, err := h.store.GetDeployment(context.Background(), id)
	return err == nil && row.Status == state.DeployReleasing
}

func (h *harness) observing(id string) bool {
	row, err := h.store.GetDeployment(context.Background(), id)
	return err == nil && row.Status == state.DeployObserving
}

func (h *harness) runToTerminalWith(rec state.DeployRecord, eng *Engine) state.DeployRecord {
	h.t.Helper()
	ctx := context.Background()
	for i := 0; i < 64; i++ {
		row, err := h.store.GetDeployment(ctx, rec.ID)
		if err != nil {
			h.t.Fatalf("get deployment: %v", err)
		}
		if row.Status.Terminal() {
			return row
		}
		h.clk.Advance(2 * time.Second)
		eng.Tick(ctx)
	}
	h.t.Fatalf("deployment %s did not reach terminal state within 64 ticks", rec.ID)
	return state.DeployRecord{}
}

func mustGet(h *harness, id string) state.DeployRecord {
	h.t.Helper()
	rec, err := h.store.GetDeployment(context.Background(), id)
	if err != nil {
		h.t.Fatalf("get deployment %s: %v", id, err)
	}
	return rec
}

// runningTaskIDs 返回指定镜像的运行中任务 ID 集合。
func (h *harness) runningTaskIDs(service, image string) map[string]bool {
	h.t.Helper()
	tasks, err := h.sub.TaskList(context.Background(), service)
	if err != nil {
		h.t.Fatalf("task list: %v", err)
	}
	out := map[string]bool{}
	for _, t := range tasks {
		if t.Image == image && t.State == "running" && t.DesiredState == "running" {
			out[t.ID] = true
		}
	}
	return out
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
