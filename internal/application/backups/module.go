package backups

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (bk *Backups) Name() string { return "backups" }

// Loops implements contract.Module: the backup loop.
func (bk *Backups) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "backup", Step: bk.Step}}
}

var _ contract.Module = (*Backups)(nil)
