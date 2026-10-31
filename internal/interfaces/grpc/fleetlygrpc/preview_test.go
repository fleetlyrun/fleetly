package fleetlygrpc

// rollout 判定的内容等价回归（修复批 F3）：workloadContentEqual 的指针
// 字段（Healthcheck/Resources/Materials——capability.Workload 是内部
// capability 结构非 proto 消息）必须解引用判等——fmt "%+v" 对指针打印
// 地址，带探针/资源上限的载体恒不等（恒 REPLACE，审查 B 探针实证）。
// nil↔非 nil 不等、双 nil 等、指向值深度比较。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TestRolloutOfProbeBearingWorkloads：等价内容 + 双侧非 nil 探针（各自
// 指向、值相等——不共享指针，逼出解引用比较）→ ZERO_TOUCH；仅副本异 →
// SCALE_ONLY；探针值异/单侧缺席/资源上限异 → REPLACE。
func TestRolloutOfProbeBearingWorkloads(t *testing.T) {
	probe := func(path string) *capability.Healthcheck {
		return &capability.Healthcheck{HTTPPath: path, HTTPPort: 8080, Interval: 5 * time.Second}
	}
	resources := func(millis int64) *capability.Resources {
		return &capability.Resources{CPUMillis: millis, MemoryMB: 256}
	}
	base := capability.Workload{
		ID: "w1", Process: "web", Image: "nginx:1.27",
		Healthcheck: probe("/healthz"), Resources: resources(500),
	}
	changed := deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_CHANGED

	// 等价内容（指针字段各自指向、值相等）：ZERO_TOUCH（修复前恒 REPLACE）。
	same := base
	same.Healthcheck = probe("/healthz")
	same.Resources = resources(500)
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_ZERO_TOUCH, rolloutOf(changed, same, &base),
		"equal probe-bearing content must be ZERO_TOUCH (pointer fields compare by pointee)")

	// 仅副本异：SCALE_ONLY。
	scaled := same
	scaled.Replicas = 3
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_SCALE_ONLY, rolloutOf(changed, scaled, &base),
		"replica-only change on probe-bearing content must be SCALE_ONLY")

	// 探针值异：REPLACE。
	otherProbe := same
	otherProbe.Healthcheck = probe("/health2")
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE, rolloutOf(changed, otherProbe, &base),
		"a changed probe value is a content change")

	// 探针单侧缺席（nil↔非 nil）：REPLACE。
	noProbe := same
	noProbe.Healthcheck = nil
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE, rolloutOf(changed, noProbe, &base),
		"nil vs non-nil probe is a content change")

	// 资源上限异：REPLACE（Resources 同为指针字段）。
	otherRes := same
	otherRes.Resources = resources(1000)
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE, rolloutOf(changed, otherRes, &base),
		"a changed resources limit is a content change")
}

// TestWorkloadContentEqualNilPointerPairs：双 nil 指针等（无探针/无上限
// 的既有形态不受本修复影响）；副本与代戳仍豁免。
func TestWorkloadContentEqualNilPointerPairs(t *testing.T) {
	a := capability.Workload{ID: "w1", Process: "web", Image: "nginx:1.27"}
	b := a
	b.Replicas, b.Generation = 9, 9 // 豁免面：副本/代戳不进内容比较
	assert.True(t, workloadContentEqual(a, b), "nil pointer pairs with equal scalars are equal")

	b.Image = "nginx:1.31"
	assert.False(t, workloadContentEqual(a, b), "scalar differences still compare")
}
