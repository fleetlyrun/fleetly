package intake

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (in *Intake) Name() string { return "intake" }

// Loops implements contract.Module: the build loop.
func (in *Intake) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "build", Step: in.Step}}
}

var _ contract.Module = (*Intake)(nil)

// _ 是 engine 去应用模块 import 第七批（2026-11-10）的核消费面断言：
// 组合根工厂经 contract.IntakeFactory 注入，Intake 必须满足全量 Intake
// 接口（构建驱动/材料/卷钉住/Registry 内容/git 拉取/build 日志/孤儿清扫）。
var _ contract.Intake = (*Intake)(nil)

// Deps 与 contract.IntakePorts 的形状同构锚：两 struct 可互转即字段名/
// 顺序/类型全同——组合根工厂逐字段转发零漂移的编译期执法（字段增删改
// 不同批同步两侧即红；manageds/browses/execs 同款）。
var (
	_ = contract.IntakePorts(Deps{})
	_ = Deps(contract.IntakePorts{})
)
