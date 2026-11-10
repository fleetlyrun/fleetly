package diagnosis

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (d *Diagnosis) Name() string { return "diagnosis" }

// Loops implements contract.Module: diagnosis is read-time, no loop.
func (d *Diagnosis) Loops() []contract.LoopSpec { return nil }

var _ contract.Module = (*Diagnosis)(nil)

// Diagnosis 是核消费面契约（engine 去应用模块 import 试点，2026-11-10：
// engine 只持 contract.Diagnosis 接口，具体装配器经组合根工厂注入）。
var _ contract.Diagnosis = (*Diagnosis)(nil)

// Deps 与核侧端口袋同形（组合根工厂把 contract.DiagnosisPorts 映射为
// Deps；字段漂移即编译红——契约叶子与应用模块装配面单源保鲜）。
var _ = contract.DiagnosisPorts(Deps{})
