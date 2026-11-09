package contract

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Module is the minimal contract every application module implements. A
// module owns one or more named reconciliation loops; the engine registers
// each loop and runs it independently.
//
// A module may expose additional optional faces (Starter, Stopper,
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

// HealthChecker is an optional module health face. The engine aggregates the
// reports into the control-plane health surface.
type HealthChecker interface {
	Health() Health
}

// Observer is an optional module observation face. The engine broadcasts
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
