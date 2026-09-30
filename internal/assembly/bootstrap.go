// Package assembly 是 fleetlyd 的组合根（唯一 wire 站点）：Provider 注册、
// 服务与拦截器装配、authz fail-closed 断言。cmd/fleetlyd 只保留薄入口与
// 版本注入；进程内装配测试（apitest）与 golden CLI 夹具复用本包。
//
// 命名说明：包名 assembly 而非 runtime——Runtime 是冻结词汇（编排器
// Capability，见 CONTEXT.md），装配层不得占用。
package assembly

import (
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// Bootstrap 组装 fleetlyd 全部依赖并返回 boot.Bootstrap（hook/服务批量
// 挂载物）与 cleanup（wire 聚合的资源清理，调用方挂 OnPostStop——排水期
// 在途请求仍需底层资源，不得提前执行）。
func Bootstrap(app lynx.App, info buildinfo.BuildInfo) (*boot.Bootstrap, func(), error) {
	return wireBootstrap(app, info)
}
