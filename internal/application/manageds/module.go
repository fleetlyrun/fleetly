package manageds

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (m *Manageds) Name() string { return "managed" }

// Loops implements contract.Module: the managed reconciler loop.
func (m *Manageds) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "managed", Step: m.Step}}
}

var _ contract.Module = (*Manageds)(nil)
