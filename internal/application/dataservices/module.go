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
		{Name: "database", Step: d.StepFor(dataservicetemplate.FamilyDatabase)},
		{Name: "pubsub", Step: d.StepFor(dataservicetemplate.FamilyPubSub)},
		{Name: "bucketstore", Step: d.StepFor(dataservicetemplate.FamilyBucketStore)},
	}
}

var _ contract.Module = (*DataServices)(nil)
