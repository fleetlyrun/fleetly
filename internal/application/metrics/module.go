package metrics

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (m *Metrics) Name() string { return "metrics" }

// Loops implements contract.Module: the scrape/alert loop.
func (m *Metrics) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "metrics", Step: m.Step}}
}

var _ contract.Module = (*Metrics)(nil)
