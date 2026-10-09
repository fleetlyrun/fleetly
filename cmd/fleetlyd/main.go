// fleetlyd 是 fleetly 控制面守护进程：装配见 internal/assembly（唯一 wire
// 站点），本文件只保留动词面分发（admin/relay，commands 框架——见
// verbs.go）、lynx runner 装配与版本注入。
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/lynx-go/commands"
	"github.com/lynx-go/lynx"
	lynxzap "github.com/lynx-go/lynx/contrib/zap"
	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
	"github.com/fleetlyrun/fleetly/internal/config"

	// 编译期 Provider 注册（blank import 触发工厂自注册；架构 §2：
	// providers 只准经注册表间接装配，全仓唯此一处）。builders 是 Builder
	// 家族单包三 Provider（ADR-0032）；k3s 与 swarm 双 Runtime 在册候选，
	// config runtime.provider 装配期恰选一（ADR-0052 决策 1）。
	_ "github.com/fleetlyrun/fleetly/internal/providers/builders"
	_ "github.com/fleetlyrun/fleetly/internal/providers/k3s"
	_ "github.com/fleetlyrun/fleetly/internal/providers/localobjectstore"
	_ "github.com/fleetlyrun/fleetly/internal/providers/s3objectstore"
	_ "github.com/fleetlyrun/fleetly/internal/providers/swarm"
	_ "github.com/fleetlyrun/fleetly/internal/providers/traefik"
	_ "github.com/fleetlyrun/fleetly/internal/providers/victorialogs"
	_ "github.com/fleetlyrun/fleetly/internal/providers/victoriametrics"
	_ "github.com/fleetlyrun/fleetly/internal/providers/zot"
)

// version/commit/date 由 mise build 的 ldflags 注入。
var version, commit, date string

// hasVerbArgs 判定是否进动词面：首参存在且非旗标形态（不带 "-" 前缀，
// 含 "--"）。旗标值（`--config-dir /path` 的 /path）永不落首位。
func hasVerbArgs(args []string) bool {
	return len(args) > 0 && !strings.HasPrefix(args[0], "-")
}

// displayVersion 返回人读版本串（ldflags 未注入时 dev 兜底）。
func displayVersion() string {
	if version == "" {
		return "dev"
	}
	return version
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
	// 动词面（commands 框架，fleetly CLI 同款）：非旗标首参进 admin/relay
	// 分发——admin 离线维护面不进 lynx runner 与 wire 装配（数据根被守护
	// 进程持有时禁止维护操作，见 admin.go）；relay 是节点载体容器内的前台
	// 进程（见 relay.go）。未知首参报 unknown verb 退 2 并附 help——staging
	// 实证 2026-10-03：`fleetlyd version` 一类笔误曾绕过参数校验、以默认配
	// 置引导流浪 daemon（建库、铸 bootstrap token、抢端口）；该防线现由
	// 分发器接管。空参/纯旗标（含 `--`）落回下方 daemon 引导路径。
	if hasVerbArgs(os.Args[1:]) {
		env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
		os.Exit(newVerbApp(displayVersion()).Run(context.Background(), env, os.Args[1:]))
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
