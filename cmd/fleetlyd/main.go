// fleetlyd 是 fleetly 控制面守护进程。
// 本阶段（T0.3）为骨架：lynx NewRunner 承载生命周期，Wire 编译期装配
// boot.Bootstrap；HTTP 面（默认 127.0.0.1:8420）挂 lynx 内置健康端点
// （/healthz/liveness 与 /healthz/readiness）与 grpc-gateway（REST /v1/**
// 反代本进程 gRPC）；gRPC 面（默认 127.0.0.1:8421）承载 server.v1 服务
// （SystemService）。业务服务随后续阶段按 lynx.Service 逐个接入。
package main

import (
	"log"
	"os"

	"github.com/lynx-go/lynx"
	"github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
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
		boot, cleanup, err := wireBootstrap(app, app.Logger())
		if err != nil {
			log.Fatal(err)
		}
		// Wire 的 cleanup（释放 DI 底层资源）挂 OnPostStop：所有服务 Stop、
		// 总线关停之后才执行，自带 CleanupTimeout 预算。不要放 OnPreStop——
		// 它先于服务 Stop 执行，排水/关停期间在途请求还要用这些资源。
		// MG-4（X-3，B6）：服务 Stop 顺序 = NewServices 注册顺序（入口面
		// → 写入者 → 资源层三段不变量，见 provides.go 的 NewServices 注释）；
		// store 连接池在此处（全部 Stop 之后）才释放。
		app.OnPostStop(cleanup)
		boot.Apply(app)
		return nil
	},
		lynx.WithName("fleetlyd"),
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
