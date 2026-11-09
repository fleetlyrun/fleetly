package intake

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (in *Intake) Name() string { return "intake" }

// Loops implements contract.Module: the build loop.
func (in *Intake) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "build", Step: in.Step}}
}

var _ contract.Module = (*Intake)(nil)
