// fleetlyd 是 fleetly 控制面守护进程：装配见 internal/assembly（唯一 wire
// 站点），本文件只保留动词面分发（admin/relay，commands 框架——见
// verbs.go）、lynx runner 装配与版本注入。
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lynxland/commands"
	"github.com/lynxland/lynx"
	lynxzap "github.com/lynxland/lynx/contrib/zap"
	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/application/platformlog"
	"github.com/fleetlyrun/fleetly/internal/assembly"
	"github.com/fleetlyrun/fleetly/internal/config"

	// 编译期 Provider 注册（blank import 触发工厂自注册；架构 §2：
	// providers 只准经注册表间接装配，全仓唯此一处）。builders 是 Builder
	// 家族单包三 Provider（ADR-0032）；k3s 与 swarm 双 Runtime 在册候选，
	// config runtime.provider 装配期恰选一（ADR-0052 决策 1）。
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/builders"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/k3s"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/localobjectstore"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/s3objectstore"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/swarm"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/traefik"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/victorialogs"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/victoriametrics"
	_ "github.com/fleetlyrun/fleetly/internal/infrastructure/providers/zot"
)

// hasVerbArgs 判定是否进动词面：首参存在且非旗标形态（不带 "-" 前缀，
// 含 "--"）。旗标值（`--config-dir /path` 的 /path）永不落首位。
func hasVerbArgs(args []string) bool {
	return len(args) > 0 && !strings.HasPrefix(args[0], "-")
}

// versionFlagArgs 判定命令行是否携带 --version/-version。全数组扫描（`--`
// 终止符后停——其后的全是位置参数）：旗标值与动词位置参数无法在不识旗标
// 语义的前提下区分，而 --version 不在任何动词面的旗标集里（admin/relay
// 各自的旗标面会拒它）——全局拦截无占有冲突，语义是"带了此旗标 = 版本
// 探查"。lynx runner 不认识该旗标且空参/纯旗标一律落 daemon 引导——
// staging 两次实证 `fleetlyd --version` 会以默认配置引导流浪 daemon
// （建库、抢 9080 失败后退出），故在 runner 之前拦截。
func versionFlagArgs(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--version" || a == "-version" {
			return true
		}
	}
	return false
}

// daemonLogMirror 是控制面日志镜像单例（loggerProvider 创建——先于 wire
// 装配存在；setupApp 挂 OnPostStop 排空落盘）。nil = 镜像不可用（数据根
// 不可写等，daemon 主径不受影响——best-effort 契约，ADR-0091）。
var daemonLogMirror *platformlog.Mirror

// loggerProvider 组装主 logger：lynxzap（stdout/journald，行为与既往
// 零变化）+ 控制面日志镜像 tee（数据根 logs/fleetlyd.log 自轮转，服务端
// 直读——ADR-0091）。镜像失败不阻断引导（stdout 主径照常）。
func loggerProvider(ctx lynx.AppContext) (*slog.Logger, error) {
	l, err := lynxzap.NewLogger(ctx)
	if err != nil {
		return nil, err
	}
	dataRoot := ctx.Config().GetString("data.root")
	if dataRoot == "" {
		dataRoot = config.DefaultDataRoot
	}
	meta := lynx.Meta(ctx.Context())
	fixed := map[string]string{}
	if meta.Name != "" {
		fixed["service.name"] = meta.Name
	}
	if meta.Version != "" {
		fixed["service.version"] = meta.Version
	}
	if meta.ID != "" {
		fixed["service.id"] = meta.ID
	}
	m, err := platformlog.OpenMirror(dataRoot, fixed)
	if err != nil {
		// 镜像开不起来：主径照常（此处尚无可用 logger 报告详情——镜像
		// 是诊断增强面，不是可用性依赖）。
		return l, nil
	}
	daemonLogMirror = m
	return slog.New(m.TeeHandler(l.Handler())), nil
}

// closeDaemonLogMirror 排空镜像队列并落盘（OnPostStop 钩子形态）。
func closeDaemonLogMirror() {
	if daemonLogMirror != nil {
		_ = daemonLogMirror.Close()
	}
}

// setupApp 组装依赖图：cleanup（wire 聚合的资源清理）挂 OnPostStop——
// 全部服务 Stop、排水完成后、Run 返回前逆序执行（lynx v1.10 语义），
// 不得提前到 OnPreStop（排水期在途请求仍需底层连接）。镜像排空先于
// cleanup 注册（逆序 = 最后执行：cleanup 期间的日志行也入镜像）。
func setupApp(app lynx.App) error {
	app.OnPostStop(closeDaemonLogMirror)
	info := lynx.BuildInfo()
	bootstrap, cleanup, err := assembly.Bootstrap(app, info)
	if err != nil {
		return err
	}
	app.OnPostStop(cleanup)
	bootstrap.Apply(app)
	return nil
}

func main() {
	info := lynx.BuildInfo()
	// 版本探查旗标（--version/-version）在任何引导路径之前拦截：打印退 0，
	// 绝不触配置/数据根（流浪 daemon 防线的旗标半边；动词半边是 version
	// 子命令）。
	if versionFlagArgs(os.Args[1:]) {
		line := "fleetlyd " + info.Display()
		fmt.Println(line)
		return
	}
	// 动词面（commands 框架，fleetly CLI 同款）：非旗标首参进 admin/relay/
	// version 分发——admin 离线维护面不进 lynx runner 与 wire 装配（数据根
	// 被守护进程持有时禁止维护操作，见 admin.go）；relay 是节点载体容器内
	// 的前台进程（见 relay.go）。未知首参报 unknown verb 退 2 并附 help——
	// staging 实证 2026-10-03：`fleetlyd version` 一类笔误曾绕过参数校验、
	// 以默认配置引导流浪 daemon（建库、铸 bootstrap token、抢端口）；该防
	// 线现由分发器接管，version 已是合法动词。空参/纯旗标（含 `--`）落回
	// 下方 daemon 引导路径。
	if hasVerbArgs(os.Args[1:]) {
		env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
		os.Exit(newVerbApp(info).Run(context.Background(), env, os.Args[1:]))
	}
	runner := lynx.NewRunner(setupApp,
		lynx.WithName("Fleetly"),
		lynx.WithLoggerProvider(loggerProvider),
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
