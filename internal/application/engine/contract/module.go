package contract

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Module is the minimal contract every application module implements. The
// engine runs one loop per module and calls Step on each tick.
type Module interface {
	Name() string
	Step(context.Context)
}

// Starter is an optional module lifecycle hook. The engine calls Start once
// before the module's loop begins.
type Starter interface {
	Start(context.Context) error
}

// Stopper is an optional module lifecycle hook. The engine calls Stop during
// graceful shutdown, after the module's loop has drained.
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
