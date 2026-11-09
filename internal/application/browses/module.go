package browses

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (b *Browses) Name() string { return "browses" }

// Loops implements contract.Module: the browse session loop.
func (b *Browses) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "browse", Step: b.Step}}
}

var _ contract.Module = (*Browses)(nil)
