package engine

// fanoutorder_test.go 钉「单条观测的扇出序」（批 2 生长点实化验收锚）：
// 载体观测的扇出各段（metrics 评估）先于 WakeOnObserve 环唤醒——「观测评估
// 先于唤醒」的现序由 wakeOnObservation 的调用点位置执法（observ.go 扇出尾）；
// 唤醒环集由声明属性执法（表驱动逐环）。唤醒信号取 Loop 的 kick 通道排队态
//（环 goroutine 未启动的单元形态下 Kick 仍入队——可观测、无并发消费）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// wakeProbeMetrics 是 metrics 评估段探针（contract.Metrics 的单方法桩）：
// 评估被调时即刻检查 deployment 环的 kick 通道——评估先于唤醒的机械证明
// （此刻通道必须尚无排队唤醒）。
type wakeProbeMetrics struct {
	contract.Metrics
	e *Engine
}

func (m wakeProbeMetrics) EvaluateCrashLoopNotify(_ context.Context, _, _ capability.WorkloadEvent, _ bool) {
	select {
	case <-m.e.rings[0].loop.currentGeneration().kick:
		m.e.log.Error("TEST: deployment ring was woken BEFORE metrics evaluation — fan-out order broken")
	default:
	}
}

// kickPending 读环的 kick 通道排队态（非阻塞；单元形态无消费 goroutine）。
func kickPending(l *Loop) bool {
	select {
	case <-l.currentGeneration().kick:
		return true
	default:
		return false
	}
}

// TestObservationFanoutEvaluatesBeforeWake：单条观测的扇出——metrics 评估段
// 先于 deployment 环唤醒（现序不变锚：crash-loop 投递沿依赖先于收敛拍评估）；
// 唤醒落位由声明属性执法（deployment 置位、task 环零置位不被观测唤醒）。
func TestObservationFanoutEvaluatesBeforeWake(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.metrics = wakeProbeMetrics{e: e}

	e.handleObservation(context.Background(), capability.WorkloadEvent{
		WorkloadID: "w-1", Generation: 1, State: capability.WorkloadRunning,
	})

	assert.True(t, kickPending(e.rings[0].loop),
		"the deployment ring is woken at the fan-out tail (WakeOnObserve attribute)")
	assert.False(t, kickPending(e.rings[2].loop),
		"the task ring carries no WakeOnObserve attribute and must not be woken by observations")
}

// TestWakeOnObserveAttributeIsDeclarative：声明属性的装配面——模块环自
// LoopSpec.WakeOnObserve 装配唤醒属性（两相挂载后半相），内核环在表字面量
// 声明；本锚钉装配结果：deployment 环置位、其余环清零（现网唯一声明者）。
func TestWakeOnObserveAttributeIsDeclarative(t *testing.T) {
	e, _, _ := newTestEngine(t)
	woken := 0
	for i := range e.rings {
		if e.rings[i].wakeOnObserve {
			woken++
			assert.Equal(t, "deployment", e.rings[i].name,
				"the deployment ring is the only observation-woken ring (fan-out tail order)")
		}
	}
	assert.Equal(t, 1, woken, "exactly one ring carries the WakeOnObserve attribute")
}

// 编译期锚：探针桩满足契约（防接口演化漂移）。
var _ contract.Metrics = wakeProbeMetrics{}
