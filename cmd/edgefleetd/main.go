// edgefleetd 是 edgefleet 控制面守护进程。
// 本阶段（T0.1）为骨架：lynx NewRunner 承载生命周期，Wire 编译期装配
// boot.Bootstrap，仅暴露 lynx 框架内置健康端点（/healthz/liveness 与
// /healthz/readiness）；业务服务随后续阶段按 lynx.Service 逐个接入。
package main

import (
	"log"

	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
var version = "dev"

func main() {
	runner := lynx.NewRunner(func(app lynx.App) error {
		app.SetLogger(zap.MustNewLogger(app))
		boot, cleanup, err := wireBootstrap(app, app.Logger())
		if err != nil {
			log.Fatal(err)
		}
		// Wire 的 cleanup（释放 DI 底层资源）挂 OnPostStop：所有服务 Stop、
		// 总线关停之后才执行，自带 CleanupTimeout 预算。不要放 OnPreStop——
		// 它先于服务 Stop 执行，排水/关停期间在途请求还要用这些资源。
		app.OnPostStop(cleanup)
		boot.Apply(app)
		return nil
	},
		lynx.WithName("edgefleetd"),
		lynx.WithVersion(version),
		lynx.WithBindFlagsFunc(func(f *pflag.FlagSet) {
			// -c/--config、--config-type、--config-dir、--log-level 沿用框架默认。
			lynx.DefaultBindFlagsFunc(f)
			f.String("addr", defaultHTTPAddr, "http listen address (config key: addr)")
		}),
		lynx.WithBindConfigFunc(lynx.DefaultBindConfigFunc),
	)
	runner.Run()
}
