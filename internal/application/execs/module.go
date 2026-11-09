package execs

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (e *Execs) Name() string { return "execs" }

// Loops implements contract.Module: exec sessions are event-driven, no loop.
func (e *Execs) Loops() []contract.LoopSpec { return nil }

var _ contract.Module = (*Execs)(nil)
