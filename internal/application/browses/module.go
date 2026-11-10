package browses

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (b *Browses) Name() string { return "browses" }

// Loops implements contract.Module: the browse session loop.
func (b *Browses) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "browse", Step: b.Step}}
}

var _ contract.Module = (*Browses)(nil)

// _ 是 engine 去应用模块 import 第四批（2026-11-10）的核消费面断言：
// 组合根工厂经 contract.BrowseFactory 注入，Browses 必须满足全量 Browse
// 接口（配置/quota/注册/entry/grant/路由发布面）。
var _ contract.Browse = (*Browses)(nil)

// _ 是 Deps 与 contract.BrowsePorts 的形状互转断言：字段名/类型/顺序
// 逐字段同形（组合根可直接 Deps(p) 转换——manageds 同款编译期钉死）。
var _ = Deps(contract.BrowsePorts{})
