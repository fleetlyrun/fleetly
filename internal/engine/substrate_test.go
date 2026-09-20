package engine

// 运行期存在性对账测试（T0-V2.2/R2）：service 缺失 → 事件落库 + 派生态
// 修正（running → down）+ 持续缺失节流（不重复发事件）+ 服务恢复后可再报
//（非永久静音）；service 存在（含外部 scale=0）→ 零事件；非 running 派生
// 态不是候选；substrate 瞬态错误 → 零事件、有日志、不被当成缺失；tick
// duty 的时间闸频控。

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// captureHandler 捕获日志记录（对账 duty「瞬态错误只进日志」断言用）。
type captureHandler struct {
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

// countEventsByName 统计至今某事件名的落库条数。
func countEventsByName(t *testing.T, h *harness, name string) int {
	t.Helper()
	got := 0
	for _, n := range h.events() {
		if n == name {
			got++
		}
	}
	return got
}

// derivedStateOf 读应用派生状态缓存（测试断言面）。
func derivedStateOf(t *testing.T, h *harness, appID string) string {
	t.Helper()
	v, err := h.store.GetAppDerivedState(context.Background(), appID)
	if err != nil {
		t.Fatalf("read derived state: %v", err)
	}
	return v
}

// flipDerivedState 手工翻转派生状态（模拟 refreshDerivedState 按 DB 事实
// 重推导的并发写——节流断言的再置位面）。
func flipDerivedState(t *testing.T, h *harness, appID, expected, next string) {
	t.Helper()
	err := h.store.InTx(context.Background(), func(tx *state.Tx) error {
		return tx.SetAppDerivedState(context.Background(), appID, expected, next)
	})
	if err != nil {
		t.Fatalf("flip derived state %s -> %s: %v", expected, next, err)
	}
}

// deployDemoSucceeded 是对账测试的前置：demo 应用首发成功（派生态 =
// running，service fleetly-demo-web 在底座存在）。
func deployDemoSucceeded(t *testing.T, h *harness) state.App {
	t.Helper()
	ctx := context.Background()
	if final := h.runToTerminal(h.enqueue(h.writeCompose(composeV1))); final.Status != state.DeploySucceeded {
		t.Fatalf("deploy = %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	app, err := h.store.GetAppByName(ctx, "demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "running" {
		t.Fatalf("precondition: derived state = %q, want running", derived)
	}
	return app
}

// TestSubstrateReconMissingServiceDisclosesCorrectsAndThrottles 核心路径：
// 外部 docker service rm → app.substrate_missing 事件 + 派生态 running →
// down；持续缺失（即使派生态被重推导回 running）不重复发事件（节流）；
// 服务恢复 → 记忆清零，再次缺失可再报（非永久静音）。
func TestSubstrateReconMissingServiceDisclosesCorrectsAndThrottles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app := deployDemoSucceeded(t, h)

	// 外部 docker service rm：期望服务整体缺失。
	if err := h.sub.ServiceRemove(ctx, "fleetly-demo-web"); err != nil {
		t.Fatalf("remove service: %v", err)
	}

	h.eng.SubstrateRecon(ctx)

	if got := countEventsByName(t, h, "app.substrate_missing"); got != 1 {
		t.Fatalf("app.substrate_missing events = %d, want exactly 1 after first scan", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "down" {
		t.Fatalf("derived state = %q, want down (honest correction)", derived)
	}

	// 节流：派生态被其他路径重推导回 running（refreshDerivedState 只看 DB
	// 事实，会翻转回来）后再次扫描——缺失存续不得事件风暴。
	flipDerivedState(t, h, app.ID, "down", "running")
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 1 {
		t.Fatalf("app.substrate_missing events = %d after rescans, want still 1 (throttle broken)", got)
	}

	// 服务恢复（重新存在即收敛：判据是存在性）→ 记忆清零、零事件。
	if err := h.sub.ServiceCreate(ctx, ServiceSpec{Name: "fleetly-demo-web", Image: "alpine:3", Replicas: 1}); err != nil {
		t.Fatalf("recreate service: %v", err)
	}
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 1 {
		t.Fatalf("unexpected extra events on recovery: %d", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "running" {
		t.Fatalf("derived state = %q after recovery, want running (untouched)", derived)
	}

	// 再次缺失 → 可再报（非永久静音），派生态再次修正。
	if err := h.sub.ServiceRemove(ctx, "fleetly-demo-web"); err != nil {
		t.Fatalf("remove service again: %v", err)
	}
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 2 {
		t.Fatalf("app.substrate_missing events = %d, want 2 (memory cleared on recovery)", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "down" {
		t.Fatalf("derived state = %q after second absence, want down", derived)
	}
}

// TestSubstrateReconSilentWhenServiceExists 反向保护：service 存在（健康
// 与外部 scale=0 形态）→ 零事件、派生态不动——判据是存在性，不是副本数
// （paused/保留现场应用 service 仍在，不得误报）。
func TestSubstrateReconSilentWhenServiceExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app := deployDemoSucceeded(t, h)

	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 0 {
		t.Fatalf("false missing on healthy baseline: %d events", got)
	}

	// 外部 docker service scale --replicas=0：service 仍在、副本归零。
	h.sub.mutateExternal("fleetly-demo-web", func(spec *ServiceSpec) { spec.Replicas = 0 })
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 0 {
		t.Fatalf("replicas=0 misjudged as missing (criterion must be service existence): %d events", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "running" {
		t.Fatalf("derived state = %q, want running (untouched)", derived)
	}

	// 非 running 派生态不是候选（degraded/blocked/down 各有归属路径）。
	flipDerivedState(t, h, app.ID, "running", "degraded")
	if err := h.sub.ServiceRemove(ctx, "fleetly-demo-web"); err != nil {
		t.Fatalf("remove service: %v", err)
	}
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 0 {
		t.Fatalf("non-running app wrongly reconciled: %d events", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "degraded" {
		t.Fatalf("derived state = %q, want degraded (untouched)", derived)
	}
}

// TestSubstrateReconSubstrateErrorIsNotMissing 平台故障保护：substrate
// API 错误（超时/不可达）≠ 服务缺失——零事件、派生态不动、错误只进日志；
// 错误恢复后对账正常工作（记忆未被污染，真实缺失仍可检出）。
func TestSubstrateReconSubstrateErrorIsNotMissing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	app := deployDemoSucceeded(t, h)

	cap := &captureHandler{}
	h.eng.log = slog.New(cap)

	h.sub.failInspectErr = errors.New("docker daemon timeout (injected)")
	h.eng.SubstrateRecon(ctx)

	if got := countEventsByName(t, h, "app.substrate_missing"); got != 0 {
		t.Fatalf("substrate error misjudged as missing: %d events", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "running" {
		t.Fatalf("derived state = %q, want running (error must not correct the view)", derived)
	}
	found := false
	for _, m := range cap.msgs {
		if m == "engine: substrate recon inspect failed (transient, not counted as missing)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("substrate error swallowed silently, want a Warn log; got %v", cap.msgs)
	}

	// 错误恢复 + 真实缺失：下一拍正常检出（瞬态错误未被当成缺失，也未
	// 污染节流记忆）。
	h.sub.failInspectErr = nil
	if err := h.sub.ServiceRemove(ctx, "fleetly-demo-web"); err != nil {
		t.Fatalf("remove service: %v", err)
	}
	h.eng.SubstrateRecon(ctx)
	if got := countEventsByName(t, h, "app.substrate_missing"); got != 1 {
		t.Fatalf("app.substrate_missing events = %d, want 1 after error cleared", got)
	}
	if derived := derivedStateOf(t, h, app.ID); derived != "down" {
		t.Fatalf("derived state = %q, want down after real absence detected", derived)
	}
}

// TestSubstrateReconTimeGateSkipsBeats tick duty 的时间闸频控：闸内拍子不
// 触达底座（ServiceInspect 计数不增长），闸过恢复扫描。
func TestSubstrateReconTimeGateSkipsBeats(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deployDemoSucceeded(t, h)

	h.eng.substrateRecon(ctx, false) // 首拍即扫（deleteScanNextAt 同语义：重启后立即扫一拍）
	afterFirst := h.sub.inspectCalls

	h.eng.substrateRecon(ctx, false) // 时钟未动：闸内跳过，不触底座
	if h.sub.inspectCalls != afterFirst {
		t.Fatalf("gated beat touched the substrate (inspect calls %d -> %d, frequency control broken)",
			afterFirst, h.sub.inspectCalls)
	}

	h.clk.Advance(substrateReconInterval + time.Second)
	h.eng.substrateRecon(ctx, false)
	if h.sub.inspectCalls <= afterFirst {
		t.Fatalf("gate never reopened (inspect calls still %d)", h.sub.inspectCalls)
	}
}
