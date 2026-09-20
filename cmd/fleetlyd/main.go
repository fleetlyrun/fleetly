// fleetlyd 是 fleetly 控制面守护进程。进程入口在此：lynx NewRunner 承载
// 生命周期与 flags，服务组装全部在 internal/runtime（Wire 编译期装配
// boot.Bootstrap：HTTP 面默认 127.0.0.1:8420 挂 lynx 内置健康端点与
// grpc-gateway REST /v1/**；gRPC 面默认 127.0.0.1:8421 承载 server.v1
// 服务）。本文件保持薄入口——组装细节的单一事实源在 internal/runtime。
package main

import (
	"log"
	"os"

	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/runtime"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
// 注入点留在 main 包（release.yml 与 deploy/e2e 脚本统一引用），经
// runtime.Bootstrap 参数进入组装层（backup manifest 与 SystemService
// Status 消费）。
var version = "dev"

func main() {
	// F5（S20）：只读运维子命令 schema-version 在 lynx runner 之前分派——
	// 不启动任何服务、不写库（详见 schema_version.go）。回退编排用它比对
	// DB schema 版本与二进制支持上限（高版本守卫触发前的可行动路径）。
	if len(os.Args) > 1 && os.Args[1] == "schema-version" {
		os.Exit(runSchemaVersion(os.Args[2:]))
	}
	runner := lynx.NewRunner(func(app lynx.App) error {
		app.SetLogger(zap.MustNewLogger(app))
		boot, cleanup, err := runtime.Bootstrap(app, version)
		if err != nil {
			log.Fatal(err)
		}
		// Wire 的 cleanup（释放 DI 底层资源）挂 OnPostStop：所有服务 Stop、
		// 总线关停之后才执行，自带 CleanupTimeout 预算。不要放 OnPreStop——
		// 它先于服务 Stop 执行，排水/关停期间在途请求还要用这些资源。
		// MG-4（X-3，B6）：服务 Stop 顺序 = NewServices 注册顺序（入口面
		// → 写入者 → 资源层三段不变量，见 internal/runtime provides.go 的
		// NewServices 注释）；store 连接池在此处（全部 Stop 之后）才释放。
		app.OnPostStop(cleanup)
		boot.Apply(app)
		return nil
	},
		lynx.WithName("fleetlyd"),
		lynx.WithVersion(version),
		lynx.WithBindFlagsFunc(func(f *pflag.FlagSet) {
			// -c/--config、--config-type、--config-dir、--log-level 沿用框架默认。
			lynx.DefaultBindFlagsFunc(f)
			f.String("addr", runtime.DefaultHTTPAddr, "http listen address (config key: addr)")
		}),
		lynx.WithBindConfigFunc(lynx.DefaultBindConfigFunc),
	)
	runner.Run()
}
