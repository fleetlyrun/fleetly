package bluegreen

// adjudicate_test.go 是蓝绿晋升/回退判据面的白盒测试（ADR-0087 P3a 批
// 第 7 单元：三态裁决表全行 + 窗/阈值边界 + 组不齐 + 代差——零 fake/编排
// 依赖 import，纯函数直测；语义源 = ADR-0048 决策 1/2 + ADR-0087 决策 7）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// gateBase 是就绪门表测的基准时刻（截止 12:00、当前 11:59:59——窗内未到）。
func gateBase() (deadline time.Time, now time.Time) {
	deadline = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	return deadline, deadline.Add(-time.Second)
}

// TestAdjudicateGateTable 钉就绪门三态裁决表全行（release/rollback 两调用
// 位共用）：就绪优先于超时、截止缺席保持、窗内保持、超时回退、边界拍。
func TestAdjudicateGateTable(t *testing.T) {
	deadline, inWindow := gateBase()
	past := deadline.Add(time.Second)
	atDeadline := deadline // 边界拍：now == deadline

	cases := []struct {
		name     string
		ready    bool
		deadline *time.Time
		now      time.Time
		want     Verdict
	}{
		{"ready promotes within the window", true, &deadline, inWindow, Promote},
		// 就绪优先于超时：迟到但就绪仍是晋升（release 位既有序：
		// releaseReady 判定先于超时判定）。
		{"ready promotes even past the deadline", true, &deadline, past, Promote},
		{"ready promotes with no deadline set", true, nil, inWindow, Promote},
		{"not ready within the window holds", false, &deadline, inWindow, Hold},
		{"not ready with no deadline holds", false, nil, inWindow, Hold},
		{"not ready past the deadline rolls back", false, &deadline, past, Rollback},
		// 边界：now == deadline = 未超（After 严格大于）——截止拍仍等观测。
		{"not ready exactly at the deadline holds", false, &deadline, atDeadline, Hold},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AdjudicateGate(tc.ready, tc.deadline, tc.now))
		})
	}
}

// TestAdjudicateObservationTable 钉观察窗裁决表全行（observe 调用位）：
// 咬合优先、窗截止缺席保持、窗内保持、到期收口晋升、边界拍。
func TestAdjudicateObservationTable(t *testing.T) {
	deadline, inWindow := gateBase()
	past := deadline.Add(time.Second)
	atDeadline := deadline // 边界拍：now == deadline = 到期（Before 严格小于）
	bite := "workload app-web stopped (gen 2): oom-killed"

	cases := []struct {
		name     string
		bite     string
		deadline *time.Time
		now      time.Time
		want     Verdict
	}{
		// 咬合优先于窗归属（engine 既有序：watchdogBite 判定先于窗判定）。
		{"a watchdog bite rolls back within the window", bite, &deadline, inWindow, Rollback},
		{"a watchdog bite rolls back with no deadline set", bite, nil, inWindow, Rollback},
		{"no bite within the window holds", "", &deadline, inWindow, Hold},
		{"no bite with no deadline holds", "", nil, inWindow, Hold},
		{"window expiry promotes (collection)", "", &deadline, past, Promote},
		// 边界：now == deadline = 到期——与就绪门闭边界互为镜像，不留死拍。
		{"exactly at the window deadline promotes", "", &deadline, atDeadline, Promote},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AdjudicateObservation(tc.bite, tc.deadline, tc.now))
		})
	}
}

// TestGroupReady 钉新代进程组齐备性全行：全就绪、空集 vacuously ready、
// 组不齐（未观测/非 running/代不匹配）逐面。
func TestGroupReady(t *testing.T) {
	const gen = uint64(3)
	running := func(id string, at uint64) Member {
		return Member{WorkloadID: id, Observed: true, State: capability.WorkloadRunning, Generation: at}
	}

	t.Run("all members running at the generation are ready", func(t *testing.T) {
		assert.True(t, GroupReady([]Member{running("app-web", gen), running("app-worker", gen)}, gen))
	})
	t.Run("empty group is vacuously ready", func(t *testing.T) {
		// 空集 = 本代零滚动（纯 scale / 全跳过，ADR-0064）。
		assert.True(t, GroupReady(nil, gen))
	})
	t.Run("an unobserved member keeps the gate closed", func(t *testing.T) {
		m := running("app-web", gen)
		unseen := Member{WorkloadID: "app-worker"}
		assert.False(t, GroupReady([]Member{m, unseen}, gen))
	})
	t.Run("a stopped member keeps the gate closed", func(t *testing.T) {
		m := running("app-web", gen)
		stopped := Member{WorkloadID: "app-worker", Observed: true, State: capability.WorkloadStopped, Generation: gen}
		assert.False(t, GroupReady([]Member{m, stopped}, gen))
	})
	t.Run("a pending member keeps the gate closed", func(t *testing.T) {
		// A2 回归（N0 修复批）：placement 落空停在 pending@当前代不得放行。
		m := running("app-web", gen)
		pending := Member{WorkloadID: "app-worker", Observed: true, State: capability.WorkloadPending, Generation: gen}
		assert.False(t, GroupReady([]Member{m, pending}, gen))
	})
	t.Run("a member running at a stale generation keeps the gate closed", func(t *testing.T) {
		// 双代窗两代并存：旧代 running@旧代不算新代就绪。
		m := running("app-web", gen)
		stale := running("app-web-old", gen-1)
		assert.False(t, GroupReady([]Member{m, stale}, gen))
	})
	t.Run("a single stale member alone keeps the gate closed", func(t *testing.T) {
		assert.False(t, GroupReady([]Member{running("app-web", gen-1)}, gen))
	})
}

// TestWatchdogBite 钉 L2 看门狗咬合判定：stopped@当前代咬合（字面逐字节）、
// 旧代 stopped 不咬合（F0.12）、非 stopped 不咬合、空消息形态。
func TestWatchdogBite(t *testing.T) {
	const gen = uint64(5)

	t.Run("stopped at the current generation bites with the stable literal", func(t *testing.T) {
		ev := capability.WorkloadEvent{
			WorkloadID: "app-web", State: capability.WorkloadStopped,
			Generation: capability.Generation(gen), Message: "exit 137",
		}
		assert.Equal(t, "workload app-web stopped (gen 5): exit 137", WatchdogBite(ev, gen))
	})
	t.Run("stopped at a stale generation does not bite", func(t *testing.T) {
		ev := capability.WorkloadEvent{
			WorkloadID: "app-web", State: capability.WorkloadStopped,
			Generation: capability.Generation(gen - 1), Message: "late event",
		}
		assert.Empty(t, WatchdogBite(ev, gen))
	})
	t.Run("running at the current generation does not bite", func(t *testing.T) {
		ev := capability.WorkloadEvent{
			WorkloadID: "app-web", State: capability.WorkloadRunning,
			Generation: capability.Generation(gen),
		}
		assert.Empty(t, WatchdogBite(ev, gen))
	})
	t.Run("unobserved zero-value event does not bite", func(t *testing.T) {
		assert.Empty(t, WatchdogBite(capability.WorkloadEvent{}, gen))
	})
	t.Run("stopped with an empty message keeps the literal shape", func(t *testing.T) {
		ev := capability.WorkloadEvent{
			WorkloadID: "app-web", State: capability.WorkloadStopped,
			Generation: capability.Generation(gen),
		}
		assert.Equal(t, "workload app-web stopped (gen 5): ", WatchdogBite(ev, gen))
	})
}

// TestBaselineGeneration 钉双代窗基线锚解析全行：行锚优先、首次部署无锚、
// 实取缺席无锚、行锚与最新成功行对不上不采、匹配回落实取。
func TestBaselineGeneration(t *testing.T) {
	row := func(fromGen uint64, fromRev string) *deployment.Deployment {
		return &deployment.Deployment{FromGeneration: fromGen, FromRevision: fromRev}
	}
	latest := &deployment.Deployment{ToRevision: "rev-1", Generation: 7}

	cases := []struct {
		name   string
		row    *deployment.Deployment
		latest *deployment.Deployment
		want   uint64
	}{
		{"the row-carried anchor wins without a lookup", row(4, "rev-1"), nil, 4},
		{"the row-carried anchor wins even against a mismatched latest", row(4, "rev-9"), latest, 4},
		{"a first deployment (no from revision) has no baseline", row(0, ""), latest, 0},
		{"a missing latest succeeded row yields no baseline", row(0, "rev-1"), nil, 0},
		{"a latest row targeting another revision is not adopted", row(0, "rev-2"), latest, 0},
		{"a matching latest row contributes its generation", row(0, "rev-1"), latest, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, BaselineGeneration(tc.row, tc.latest))
		})
	}
}

// TestServingGen 钉在服代推导全行：observing = 本代（切流），其余在途
// （含 failed 待回滚/rolling-back）= 基线代（切回）。
func TestServingGen(t *testing.T) {
	const gen, fromGen = uint64(6), uint64(5)
	cases := []struct {
		name  string
		state deployment.State
		want  uint64
	}{
		{"observing serves its own generation", deployment.StateObserving, gen},
		{"releasing still serves the baseline", deployment.StateReleasing, fromGen},
		{"failed (awaiting rollback) serves the baseline", deployment.StateFailed, fromGen},
		{"rolling-back serves the baseline", deployment.StateRollingBack, fromGen},
		{"preparing serves the baseline", deployment.StatePreparing, fromGen},
		{"building serves the baseline", deployment.StateBuilding, fromGen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &deployment.Deployment{State: tc.state, Generation: gen, FromGeneration: fromGen}
			assert.Equal(t, tc.want, ServingGen(d))
		})
	}
}

// TestVerdictString 钉裁决字面（测试/日志可读面的稳定词汇）。
func TestVerdictString(t *testing.T) {
	require.Equal(t, "hold", Hold.String())
	require.Equal(t, "promote", Promote.String())
	require.Equal(t, "rollback", Rollback.String())
}
