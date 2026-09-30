// fleetlyd 是 fleetly 控制面守护进程：装配见 internal/assembly（唯一 wire
// 站点），本文件只保留 lynx runner 装配与版本注入。
package main

import (
	"log"
	"time"

	"github.com/lynx-go/lynx"
	lynxzap "github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// version/commit/date 由 mise build 的 ldflags 注入。
var version, commit, date string

// setupApp 组装依赖图：cleanup（wire 聚合的资源清理）挂 OnPostStop——
// 全部服务 Stop、排水完成后、Run 返回前逆序执行（lynx v1.10 语义），
// 不得提前到 OnPreStop（排水期在途请求仍需底层连接）。
func setupApp(app lynx.App) error {
	info := buildinfo.BuildInfo{Version: version, Commit: commit, Date: date}
	bootstrap, cleanup, err := assembly.Bootstrap(app, info)
	if err != nil {
		return err
	}
	app.OnPostStop(cleanup)
	bootstrap.Apply(app)
	return nil
}

func main() {
	runner := lynx.NewRunner(setupApp,
		lynx.WithName("Fleetly"),
		lynx.WithVersion(version),
		lynx.WithLoggerProvider(lynxzap.NewLogger),
		lynx.WithBindFlagsFunc(func(f *pflag.FlagSet) {
			f.String("config-dir", "", "config file search path, default working directory")
			// 默认值必须为空（lynx 级别键契约）：非空默认会在每次启动被
			// 翻译进规范键 logging.level，配置文件里的级别永远失效。
			f.String("log-level", "", "log level, default info")
		}),
		lynx.WithBindConfigFunc(config.NewBindConfigFunc()),
		// 排水窗口：生产默认 30s（LB 摘流 + 在途请求收口）。
		lynx.WithDrainTimeout(30*time.Second),
		lynx.WithShutdownTimeout(30*time.Second),
	)

	if err := runner.RunE(); err != nil {
		log.Fatalln(err)
	}
}
