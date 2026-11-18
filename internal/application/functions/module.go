package functions

import (
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

// Name implements contract.Module.
func (f *Functions) Name() string { return "functions" }

// Loops implements contract.Module：单环 function（收敛循环；ring 表登记
// 名与 engine construct.go 循环表一致——ringregistry 契约测试冻结）。
func (f *Functions) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "function", Step: f.stepFunction}}
}

var (
	// 核消费面编译期断言（ADR-0088 消费面纪律；dataservices 同款形态）。
	_ contract.Module    = (*Functions)(nil)
	_ contract.Functions = (*Functions)(nil)
	// Deps 与契约端口袋逐字段同形（组合根工厂经直接互转装配；字段名/类型
	// 任一漂移即编译红）。
	_ = contract.FunctionsPorts(Deps{})
)
