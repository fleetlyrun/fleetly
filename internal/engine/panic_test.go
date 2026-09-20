package engine

// S18-A9：tick panic 隔离测试——单条部署的推进路径 panic（底座注入）只
// 失败该条（E_RUNTIME_UNAVAILABLE 终态 + 引擎推进 panic 已捕获），引擎
// tick 存活：同拍其他部署照常推进、后续 tick 正常收敛。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// enqueueApp 以指定 app 名入队（harness.enqueue 固定 demo；A9 需要两个
// app 绕开同 app 互斥）。
func (h *harness) enqueueApp(t *testing.T, composePath, appName string) state.DeployRecord {
	t.Helper()
	ctx := context.Background()
	app, err := ensureAppForTest(ctx, h.store, appName)
	if err != nil {
		t.Fatalf("ensure app %s: %v", appName, err)
	}
	rec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID:       app.ID,
		AppName:     app.Name,
		Kind:        "deploy",
		SpecHash:    specHashOf(composePath),
		ComposePath: composePath,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return rec
}

// TestTickPanicIsolatedPerDeployment A9：观察窗推进中底座 TaskList panic
// → 该部署失败终态（E_RUNTIME_UNAVAILABLE），另一部署同拍/后续拍正常
// 推进——tick 循环存活（panic 未外溢为进程崩溃即测试通过的前提）。
func TestTickPanicIsolatedPerDeployment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	composeOther := strings.Replace(composeV1, "name: demo", "name: other", 1)
	recA := h.enqueueApp(t, h.writeCompose(composeV1), "demo")
	recB := h.enqueueApp(t, h.writeCompose(composeOther), "other")

	// A 推进到 observing 后注入毒点：TaskList（观察窗信号源）panic。
	h.runToStatus(t, recA, state.DeployObserving)
	h.sub.panicOnTaskList("fleetly-demo-web")

	// 同一拍：A panic → 失败终态；B 正常推进（不因前者中断）。
	h.clk.Advance(2 * time.Second)
	h.eng.Tick(ctx)

	rowA, err := h.store.GetDeployment(ctx, recA.ID)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if rowA.Status != state.DeployFailed {
		t.Fatalf("A status = %s, want failed (panic fallback terminal state)", rowA.Status)
	}
	if rowA.ErrorCode != "E_RUNTIME_UNAVAILABLE" {
		t.Fatalf("A error_code = %q, want E_RUNTIME_UNAVAILABLE", rowA.ErrorCode)
	}
	rowB, err := h.store.GetDeployment(ctx, recB.ID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if rowB.Status.Terminal() && rowB.Status == state.DeployFailed {
		t.Fatalf("B must not fail because of A's panic (status %s)", rowB.Status)
	}

	// 引擎存活：后续 tick 把 B 正常推进到成功终态。
	if final := h.runToTerminal(recB); final.Status != state.DeploySucceeded {
		t.Fatalf("B final = %s (%s), want succeeded (tick survived)", final.Status, final.ErrorCode)
	}
	// A 的失败事件披露（deployment.failed，code=E_RUNTIME_UNAVAILABLE）。
	found := false
	for _, ev := range mustEvents(t, h) {
		if ev.Name == "deployment.failed" && ev.Subject == "deployment:"+recA.ID {
			if strings.Contains(ev.Payload, "E_RUNTIME_UNAVAILABLE") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("A failure event with E_RUNTIME_UNAVAILABLE missing")
	}
}
