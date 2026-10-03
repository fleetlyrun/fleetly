// fleetlyd 是 fleetly 控制面守护进程：装配见 internal/assembly（唯一 wire
// 站点），本文件只保留 lynx runner 装配与版本注入。
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/lynx-go/lynx"
	lynxzap "github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/config"

	// 编译期 Provider 注册（blank import 触发工厂自注册；架构 §2：
	// providers 只准经注册表间接装配，全仓唯此一处）。builders 是 Builder
	// 家族单包三 Provider（ADR-0032）。
	_ "github.com/fleetlyrun/fleetly/internal/providers/builders"
	_ "github.com/fleetlyrun/fleetly/internal/providers/localobjectstore"
	_ "github.com/fleetlyrun/fleetly/internal/providers/swarm"
	_ "github.com/fleetlyrun/fleetly/internal/providers/traefik"
	_ "github.com/fleetlyrun/fleetly/internal/providers/zot"
)

// version/commit/date 由 mise build 的 ldflags 注入。
var version, commit, date string

// validateFirstArg 拒绝非旗标首参：fleetlyd 除 admin 外不收任何子命令，
// 而未知位置参数会被 runner 静默吞掉并直接引导 daemon（默认配置 = 流浪
// 数据根 + bootstrap token + 抢端口，staging 实证 2026-10-03）。只查首参
// ——旗标值（`--config-dir /path` 的 /path）永不落首位。
func validateFirstArg(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "admin" {
		return nil
	}
	return fmt.Errorf("unknown argument %q: fleetlyd takes no subcommands besides \"admin\" and no positional arguments; a stray word here would boot a daemon with default config", args[0])
}

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
	// admin 离线维护面（停机窗口子命令）：不进 lynx runner——数据根被
	// 守护进程持有时禁止维护操作（见 admin.go）。
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(runAdmin(os.Args[2:]))
	}
	// 未知非旗标首参守卫（staging 实证 2026-10-03：`fleetlyd version` 一类
	// 笔误会绕过参数校验、以默认配置引导一个流浪 daemon——建库、铸
	// bootstrap token、抢端口）。只查首参：旗标值位置参数（如
	// `--config-dir /path` 的 /path）永不落首位，不受影响。
	if err := validateFirstArg(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
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
