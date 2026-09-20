// Package runtime 是 fleetlyd 的服务组装层（composition root）：在 lynx
// App 之上，Wire 编译期装配出 boot.Bootstrap——配置解析与缺省回落
//（config.go）、资源构造与释放（provides.go 的 provider 集）、入口面
//（HTTP gateway/gRPC/git SSH）、写入者与资源层的服务壳（*_service.go，
// 注册序即停止序的三段不变量见 NewServices 注释），以及 schema-version
// 之外的全部守护进程形态。进程入口细节（flags、Runner 生命周期宿主、
// cleanup 挂 OnPostStop、schema-version 子命令分派）留在 cmd/fleetlyd；
// 本包只面向 lynx.App 组装，不感知进程级细节。
package runtime

import (
	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/boot"
)

// Version 是注入 wire 图的守护进程版本（具名类型，避免裸 string 在依赖
// 图中撞型）。值来自 cmd/fleetlyd 构建期注入的 main.version，经 Bootstrap
// 参数传入；backup manifest 的 binary_version 与 SystemService.Status 消费。
type Version string

// Bootstrap 组装控制面全部组件（wireBootstrap 的 Wire 编译期装配形态）。
// version 是守护进程版本（构建期 ldflags 注入，见 Version 注释）。
//
// 返回的 cleanup 释放 DI 底层资源（store 连接池、底座客户端等）——调用方
// 挂 OnPostStop（晚于全部服务 Stop；排水期在途请求仍要用这些资源，挂载
// 语义见 cmd/fleetlyd main.go 注释）。
func Bootstrap(app lynx.App, version string) (*boot.Bootstrap, func(), error) {
	return wireBootstrap(app, app.Logger(), Version(version))
}
