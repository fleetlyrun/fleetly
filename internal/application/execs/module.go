package execs

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (e *Execs) Name() string { return "execs" }

// Loops implements contract.Module: exec sessions are event-driven, no loop.
func (e *Execs) Loops() []contract.LoopSpec { return nil }

var _ contract.Module = (*Execs)(nil)

// _ 是 engine 去应用模块 import 第六批（2026-11-10）的核消费面断言：
// 组合根工厂经 contract.ExecFactory 注入，Execs 必须满足全量 Exec 接口
// （受理/附着/实例路由/中继 hub/凭证与回环中继面）。
var _ contract.Exec = (*Execs)(nil)

// Deps 与 contract.ExecPorts 的形状同构锚：两 struct 可互转即字段名/
// 顺序/类型全同——组合根工厂逐字段转发零漂移的编译期执法（字段增删改
// 不同批同步两侧即红；manageds/browses 同款）。
var (
	_ = contract.ExecPorts(Deps{})
	_ = Deps(contract.ExecPorts{})
)
