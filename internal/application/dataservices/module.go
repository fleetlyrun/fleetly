package dataservices

import (
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

// Name implements contract.Module.
func (d *DataServices) Name() string { return "dataservices" }

// Loops implements contract.Module: one loop per data service kind.
func (d *DataServices) Loops() []contract.LoopSpec {
	loops := make([]contract.LoopSpec, 0, len(d.kindSteps))
	for _, ks := range d.kindSteps {
		loops = append(loops, contract.LoopSpec{Name: ks.kind, Step: d.StepFor(ks.kind)})
	}
	return loops
}

var (
	// 核消费面编译期断言（engine 去应用模块 import 第九批，2026-11-10）。
	_ contract.Module      = (*DataServices)(nil)
	_ contract.DataService = (*DataServices)(nil)
	// Deps 与契约端口袋逐字段同形（组合根工厂经直接互转装配；字段名/类型/
	// 顺序任一漂移即编译红）。
	_ = contract.DataServicePorts(Deps{})
)
