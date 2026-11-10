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
// step runs on the engine's single-writer loop goroutine.
type LoopSpec struct {
	Name string
	Step func(context.Context)
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
// reports into the control-plane health surface.
type HealthChecker interface {
	Health() Health
}

// Observer is an optional module observation extension. The engine broadcasts
// workload events to every observer; each module filters the workloads it
// owns. This keeps the engine free of module-specific ownership maps.
type Observer interface {
	Observe(context.Context, capability.WorkloadEvent)
}

// Health is a module health report. Healthy=false must carry a human-readable
// Details string; the engine surfaces it through GetStatus and doctor.
type Health struct {
	Healthy bool
	Details string
}
