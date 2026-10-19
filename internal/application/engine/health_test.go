package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noInspectorRuntime 用同名异签名方法遮蔽内嵌的 Inspector 实现（duck-typing
// 面缺席形态：第三 Runtime / 编译期选装 Provider）。
type noInspectorRuntime struct{ *fakeRuntime }

func (*noInspectorRuntime) InspectWorkloads() {}

// R02-M7：环 panic 后进程不崩，但死亡必须可查询（Done 被 supervisor 消费
// → ring dead 记账），并出现在健康组件面（GetStatus components 的数据源）。
func TestRingPanicMarksRingDeadAndHealthVisible(t *testing.T) {
	e, _, _ := newTestEngine(t)
	injected := false
	for i := range e.rings {
		if e.rings[i].name == "deployment" {
			e.rings[i].step = func(context.Context) { panic("injected ring panic") }
			injected = true
		}
	}
	require.True(t, injected, "fixture: deployment ring must be registered")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for len(e.DeadRings()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, []string{"deployment"}, e.DeadRings(),
		"a panicked ring is marked dead and queryable (no longer silent)")

	rows := e.HealthComponents()
	var ringRow, convergenceRow *HealthComponent
	for i := range rows {
		switch {
		case rows[i].Details == "ring=deployment dead":
			ringRow = &rows[i]
		case rows[i].Name == ConvergenceHealthComponent:
			convergenceRow = &rows[i]
		}
	}
	require.NotNil(t, ringRow, "health components must carry the dead ring as a stable row: %+v", rows)
	assert.False(t, ringRow.Healthy)
	assert.Equal(t, "engine ring deployment", ringRow.Name)
	require.NotNil(t, convergenceRow, "convergence capability row is always present in the health face")
	assert.True(t, convergenceRow.Healthy, "the test runtime exposes the inspector; convergence capability is intact")
	assert.Contains(t, convergenceRow.Details, "posture=all")
	assert.Contains(t, convergenceRow.Details, "inspector=present")
}

// R16-L3：Inspector 子面缺席是收敛能力缺口（spec 漂移/gen 偏离/缺席检测
// 全盲）——健康组件面必须如实标记（doctor 的 convergence capability 行
// 由此渲染）。
func TestHealthComponentReportsMissingInspector(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.runtime = &noInspectorRuntime{fakeRuntime: e.runtime.(*fakeRuntime)}

	var convergenceRow *HealthComponent
	rows := e.HealthComponents()
	for i := range rows {
		if rows[i].Name == ConvergenceHealthComponent {
			convergenceRow = &rows[i]
		}
	}
	require.NotNil(t, convergenceRow)
	assert.False(t, convergenceRow.Healthy, "a missing inspector makes default convergence blind")
	assert.Contains(t, convergenceRow.Details, "inspector=missing")
	assert.True(t, strings.Contains(convergenceRow.Details, "posture="), "details carry the posture for diagnosis")
}

// 正常路径零影响：未发生 panic 的健康引擎无 ring dead 行、无死亡环。
func TestHealthyEngineHasNoDeadRings(t *testing.T) {
	e, _, _ := newTestEngine(t)
	assert.Empty(t, e.DeadRings())
	for _, row := range e.HealthComponents() {
		assert.NotContains(t, row.Details, " dead", "a healthy engine must not report any dead ring")
	}
}
