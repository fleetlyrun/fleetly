package manageds

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (m *Manageds) Name() string { return "managed" }

// Loops implements contract.Module: the managed reconciler loop.
func (m *Manageds) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "managed", Step: m.Step}}
}

var _ contract.Module = (*Manageds)(nil)

// 受管域应用模块的核契约面（engine 去应用模块 import 第三批，2026-11-10）：
// 核只持 contract.Managed 接口，装配器经 engine.Deps.ManagedFactory 注入。
var _ contract.Managed = (*Manageds)(nil)

// Deps 与 contract.ManagedPorts 的形状同构锚：两 struct 可互转即字段名/
// 顺序/类型全同——组合根工厂逐字段转发零漂移的编译期执法（字段增删改
// 不同批同步两侧即红）。
var (
	_ = contract.ManagedPorts(Deps{})
	_ = Deps(contract.ManagedPorts{})
)
