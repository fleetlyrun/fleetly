package diagnosis

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (d *Diagnosis) Name() string { return "diagnosis" }

// Loops implements contract.Module: diagnosis is read-time, no loop.
func (d *Diagnosis) Loops() []contract.LoopSpec { return nil }

var _ contract.Module = (*Diagnosis)(nil)
