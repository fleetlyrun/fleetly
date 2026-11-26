package contract

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Module is the minimal contract every application module implements. A
// module owns one or more named reconciliation loops; the engine registers
// each loop and runs it independently.
//
// A module may expose additional optional extensions (Starter, Stopper,
// HealthChecker, Observer). The engine discovers them by type assertion, so
// modules without background goroutines or health checks stay small.
type Module interface {
	Name() string
	Loops() []LoopSpec
}

// LoopSpec is one named reconciliation loop contributed by a module. The name
// must be unique across the engine (it is the health/failure-domain key); the
// step runs on the engine's single-writer loop goroutine. WakeOnObserve 是环
// 的声明唤醒属性（批 2 生长点实化）：置位的环在每条非 Run 载体观测的扇出
// 末尾由内核统一唤醒（观测评估先于唤醒的现序不变）——模块不再经裸 Kick
// 端口自定唤醒时机。
type LoopSpec struct {
	Name string
	Step func(context.Context)
	// WakeOnObserve 置位 = 载体观测扇出末尾由内核唤醒本环。
	WakeOnObserve bool
}

// LoopSpecOf returns the full named loop spec of a module. A nil module
// (optional module not wired by the composition root) yields a no-op spec;
// a non-nil module that does not declare the loop panics — a missing binding
// is a construction-time programming error, not a runtime degradation.
func LoopSpecOf(m Module, name string) LoopSpec {
	if m == nil {
		return LoopSpec{Name: name, Step: func(context.Context) {}}
	}
	for _, loop := range m.Loops() {
		if loop.Name == name {
			return loop
		}
	}
	panic("contract: module " + m.Name() + " has no loop " + name)
}

// Kicker 是环唤醒的窄句柄（批 2 生长点实化）：模块经 Kicker(name) 只拿到
// Kick 一面——Loop 的 Run/Reset/Done 与代次机制结构性不可达，模块不能自起
// 或重置循环。
type Kicker interface {
	Kick()
}

// StepOf returns the named loop step of a module. A nil module (optional
// module not wired by the composition root) yields a no-op step so its ring
// stays registered with the frozen DriveOnce order and health surface; a
// non-nil module that does not declare the loop panics — a missing binding is
// a construction-time programming error, not a runtime degradation.
func StepOf(m Module, name string) func(context.Context) {
	if m == nil {
		return func(context.Context) {}
	}
	for _, loop := range m.Loops() {
		if loop.Name == name {
			return loop.Step
		}
	}
	panic("contract: module " + m.Name() + " has no loop " + name)
}

// Starter is an optional module lifecycle hook. The engine calls Start once
// before the module's loops begin.
type Starter interface {
	Start(context.Context) error
}

// Stopper is an optional module lifecycle hook. The engine calls Stop during
// graceful shutdown, after the module's loops have drained.
type Stopper interface {
	Stop(context.Context) error
}

// HealthChecker is an optional module health extension. The engine aggregates the
// reports into the control-plane health surface (每模块一至多行——行序随装配
// 面；Healthy=false 必须带 Details）。
type HealthChecker interface {
	Health() []Health
}

// RouteContributor is an optional module route extension. The engine merges
// every mounted contributor's capability routes into each publish beat and
// folds their fingerprints into the publish short-circuit signature（指纹漏
// 并 = 短路吞掉该面路由变更）。
type RouteContributor interface {
	RoutesFingerprint(ctx context.Context) string
	CapabilityRoutes(ctx context.Context) []capability.Route
}

// Observer is an optional module observation extension. The engine broadcasts
// workload events to every observer; each module filters the workloads it
// owns. This keeps the engine free of module-specific ownership maps.
type Observer interface {
	Observe(context.Context, capability.WorkloadEvent)
}

// Health is a module health report row. Name 稳定（健康面行名，如
// "managed proxy"）；Healthy=false must carry a human-readable Details
// string; the engine surfaces it through GetStatus and doctor.
type Health struct {
	Name    string
	Healthy bool
	Details string
}
