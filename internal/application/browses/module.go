package browses

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

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

// RoutesFingerprint implements contract.RouteContributor（批 2 生长点实化）：
// browse 会话集指纹（ephemeral 路由不经 route 行，会话增删不改行集指纹
// ——漏并则短路吞掉会话路由变更）。
func (b *Browses) RoutesFingerprint(ctx context.Context) string {
	return b.BrowseRoutesFingerprint()
}

// CapabilityRoutes implements contract.RouteContributor（批 2 生长点实化）：
// 会话的 ephemeral 双路由（entry 兑换入口 + ForwardAuth 门禁工具路由）。
func (b *Browses) CapabilityRoutes(ctx context.Context) []capability.Route {
	return b.BrowseCapabilityRoutes(ctx)
}

var _ contract.RouteContributor = (*Browses)(nil)

// _ 是 Deps 与 contract.BrowsePorts 的形状互转断言：字段名/类型/顺序
// 逐字段同形（组合根可直接 Deps(p) 转换——manageds 同款编译期钉死）。
var _ = Deps(contract.BrowsePorts{})
