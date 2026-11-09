package dataservices

import (
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// Name implements contract.Module.
func (d *DataServices) Name() string { return "dataservices" }

// Loops implements contract.Module: one loop per data service kind.
func (d *DataServices) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{
		{Name: "database", Step: d.StepFor(dataservicetemplate.KindDatabase)},
		{Name: "pubsub", Step: d.StepFor(dataservicetemplate.KindPubSub)},
		{Name: "bucketstore", Step: d.StepFor(dataservicetemplate.KindBucketStore)},
	}
}

var _ contract.Module = (*DataServices)(nil)
