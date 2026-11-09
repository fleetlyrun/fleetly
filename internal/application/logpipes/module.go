package logpipes

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (s *Logpipes) Name() string { return "logpipes" }

// Loops implements contract.Module: the log ingestion loop.
func (s *Logpipes) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "logging", Step: s.Step}}
}

var _ contract.Module = (*Logpipes)(nil)
