package apitest_test

// R12-M7 / S17-c4：apitest 假 Runtime 的回执与观测面——全栈夹具（真 handler
// + 真 engine + 真 SQLite）此前恒返回空 EnsureReport、Addresses 恒 nil、
// Watch 恒空，ADR-0064 的"内容等价未变载体保留旧代次 → L1 门按回执 gen
// 等待"与 ADR-0082 的"滚动停摆 → 收敛"在全栈层结构性不可达。
//
// 本文件的两条用例分别钉两个形态：
//   - 内容等价：同一 spec 的二代部署，假底座回执旧 wire gen，L1 门按旧 gen
//     放行（不再等一个永远不会到来的 running@部署gen）；配套对照用例证明
//     空回执时 L1 仍等新代——新能力不是"默认全绿"。
//   - 滚动停摆：InspectWorkloads 上报 RolloutStalled 后，漂移扫描产生
//     rollout 收敛信号并发出 workload.converged，期间无 converge_failed。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// convergedEvents 返回某 App 的 workload.converged 事件（全量，含早期）。
func convergedEvents(t *testing.T, h *apitest.Harness, appID string) []outbox.Event {
	t.Helper()
	evs, err := h.Services.OutboxEvents.ListAfter(context.Background(), 0, 500, "")
	require.NoError(t, err)
	var out []outbox.Event
	for _, ev := range evs {
		if ev.Name == "workload.converged" && ev.Aggregate == "app" && ev.AggregateID == appID {
			out = append(out, ev)
		}
	}
	return out
}

// driveUntilSucceeded 手动驱动部署拍直至 succeeded（不注入 running 观测；
// 依赖的正是假底座回执的 wire gen 归因）。
func driveUntilSucceeded(t *testing.T, h *apitest.Harness, depID string) string {
	t.Helper()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	state := ""
	for i := 0; i < 40; i++ {
		h.Drive(context.Background())
		got, err := dc.GetDeployment(h.Ctx(), &deliveryv1.GetDeploymentRequest{Id: depID})
		require.NoError(t, err)
		state = got.GetDeployment().GetState()
		if state == "succeeded" {
			return state
		}
		h.Clock.Advance(120 * time.Second)
		h.Drive(context.Background())
		got, err = dc.GetDeployment(h.Ctx(), &deliveryv1.GetDeploymentRequest{Id: depID})
		require.NoError(t, err)
		state = got.GetDeployment().GetState()
		if state == "succeeded" {
			return state
		}
	}
	return state
}

// TestContentEquivalentRedeployKeepsWireGen：内容等价重下发回旧 gen，L1 门
// 按回执旧代放行（ADR-0064 决策 4 的全栈形态）。用例不注入任何 running@2
// 观测——若引擎按调用 gen 记账，部署永远卡在 releasing（对照用例钉死）。
func TestContentEquivalentRedeployKeepsWireGen(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := waitFixtureApp(t, h)

	// 先开内容等价回执：首部署建立逐载体内容账本（gen 1），二代才能命中。
	h.Runtime.SetEnsureContentEquivalence(true)
	_ = deployToSucceeded(t, h, appID)

	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	second, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, second.GetDeployment().GetGeneration(),
		"fixture: the second identical deploy is a new generation")

	state := driveUntilSucceeded(t, h, second.GetDeployment().GetId())
	require.Equal(t, "succeeded", state,
		"content-equivalent receipt must let L1 pass on the wire gen — a deployment-gen wait would never see running@2")

	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	require.EqualValues(t, 2, last.Gen, "the second Ensure call is at the deployment gen")
	require.Equal(t, capability.Generation(1), last.Report.Gens[appID+"-web"],
		"the fake must report the carrier's retained wire gen (content-equivalent skip)")
	assert.Equal(t, capability.Generation(1), last.Report.WireGen(last.Spec["web"], 2),
		"EnsureReport.WireGen is the attribution contract the engine consumes")
	assert.NotContains(t, last.Report.Changed, appID+"-web",
		"a content-equivalent skip is not a material change (ADR-0064)")
}

// TestEmptyEnsureReceiptKeepsL1Waiting 是对照/防腐用例：关闭内容等价回执时
// 二代部署的 L1 门仍按部署 gen 等待——假底座的默认语义没有被新能力改写，
// 上面那条用例的绿只可能来自真实回执。
func TestEmptyEnsureReceiptKeepsL1Waiting(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	appID := waitFixtureApp(t, h)
	_ = deployToSucceeded(t, h, appID)

	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	second, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, second.GetDeployment().GetGeneration())

	// 三拍手动驱动：queued → preparing → releasing（不注入 running@2）。
	state := ""
	for i := 0; i < 3; i++ {
		h.Drive(context.Background())
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: second.GetDeployment().GetId()})
		require.NoError(t, err)
		state = got.GetDeployment().GetState()
	}
	assert.Equal(t, "releasing", state,
		"with the default empty EnsureReport the L1 gate must still wait for running@deployment-gen (the fake must not silently become content-equivalent for everyone)")
}

// TestRolloutStalledTriggersConvergence：Inspector 上报滚动停摆 → driftScan
// 产生 rollout 信号 → 收敛动作重发 Ensure + workload.converged 事件；期间
// 无 workload.converge_failed。漂移节拍注入到测试尺度（生产缺省 30s）。
func TestRolloutStalledTriggersConvergence(t *testing.T) {
	h := apitest.NewWithOptions(t, func(o *engine.Options) {
		o.Ring.DriftScanInterval = 50 * time.Millisecond
		o.Ring.Tick = 20 * time.Millisecond
	})
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "stalled").GetId(), "web").GetId()

	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	dep, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})
	require.NoError(t, err)
	gen := dep.GetDeployment().GetGeneration()
	depID := dep.GetDeployment().GetId()

	// 自动驱动夹具：报告 running@gen 直至 observing，再推观察窗到 succeeded。
	require.Eventually(t, func() bool {
		h.Runtime.ReportRunning(appID+"-web", capability.Generation(gen))
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 10*time.Second, 10*time.Millisecond, "deployment did not reach observing")
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: depID})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 10*time.Second, 10*time.Millisecond, "deployment did not reach succeeded")

	require.Empty(t, convergedEvents(t, h, appID), "fixture: no convergence event before the injected stall")
	ensuresBefore := len(h.Runtime.Calls())

	// 注入停摆观测：滚动停在中间态、spec 面一致（Ensure 已被接受）。
	h.Runtime.SetRolloutStalled(appID+"-web", true)
	require.Eventually(t, func() bool {
		return len(convergedEvents(t, h, appID)) > 0
	}, 10*time.Second, 20*time.Millisecond, "a stalled rollout must trigger a convergence event (ADR-0082 row 4)")
	assert.Greater(t, len(h.Runtime.Calls()), ensuresBefore,
		"the convergence action must re-issue Ensure (the stalled carrier resume path)")

	// 期间无 converge_failed（失败/冷却链未误触发）。
	evs, err := h.Services.OutboxEvents.ListAfter(context.Background(), 0, 500, "")
	require.NoError(t, err)
	for _, ev := range evs {
		assert.NotEqual(t, "workload.converge_failed", ev.Name,
			"a recoverable rollout stall must not enter the cooling/failure chain")
	}
}
