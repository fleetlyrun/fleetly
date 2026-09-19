// fleetly CLI：fleetly 平台命令行（lynx-go/commands 动词注册，D21）。
//
// T2.18 CLI-over-SDK 改造：CLI 与 API 同源——全部平台动词只经 SDK（gRPC）
// 消费 fleetlyd（连接参数 --addr/--token，env 覆盖 FLEETLY_ADDR/
// FLEETLY_TOKEN），不再有任何直开 DB / 直连 docker / 直读密钥的路径。
// 纯本地解析保留在 validate/plan--baseline/diff（internal/compose 纯库）。
// 全动词支持 --json；退出码四态（S17-D3）：0=成功/无变化、1=错误、
// 2=有变化（仅 plan/diff）、64=用法错误。
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/lynx-go/commands"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
var version = "dev"

func main() {
	// 根 ctx 接信号（S17-D3）：Ctrl-C（os.Interrupt）/ SIGTERM 取消全部
	// 在途动词——流式（logs follow / events watch）与轮询等待（deploy /
	// build / rollback / deployments cancel）把取消判为干净退出（exit 0），
	// gRPC 流随 ctx 取消正常收尾，不再靠进程硬杀撕裂。Windows 上 SIGTERM
	// 常量定义存在但不可投递，注册无害；再次信号恢复默认终止行为。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(newApp().Run(ctx, env, os.Args[1:]))
}

// newApp 组装 CLI（测试同路径复用：直接对 App.Run 传参断言退出码，
// 不起子进程、不触碰 os.Args——lynx-go/commands 不解析进程参数，无
// fleetlyd Runner 的 os.Args/CWD 隔离问题）。
func newApp() *commands.App {
	app := commands.New()
	app.Register(
		&versionCmd{},
		&validateCmd{},
		&planCmd{},
		&diffCmd{},
		newAppsCmd(),
		&buildCmd{},
		newBuildsCmd(),
		&deployCmd{},
		newDeploymentsCmd(),
		&rollbackCmd{},
		newRevisionsCmd(),
		newDriftCmd(),
		newEnvCmd(),
		newLogsCmd(),
		newEventsCmd(),
		newTokensCmd(),
		newPlacementCmd(),
		newNodesCmd(),
		newDomainsCmd(),
		newIngressCmd(),
		newGitCmd(),
		newBackupsCmd(),
	)
	// 退出码四态（架构 §2.4 plan/apply 语义 + S17-D3）：0=无变化/成功、
	// 2=有变化（仅 plan/diff）、1=错误、64=用法错误（EX_USAGE 惯例）。
	app.ExitCode = exitCodeFor
	app.RenderError = renderCLIError
	app.HelpHeader = "fleetly — fleetly platform CLI (" + version + ")"
	app.VerbTitle = "verbs:"
	return app
}

// exitUsage 是用法类错误（未知动词/flag 解析失败/位置参数违规）的退出码
// （S17-D3）：64 = sysexits.h 的 EX_USAGE 惯例。与 plan/diff「检测到变化」
// 的 2 分离——Agent/脚本据此区分"有漂移"与"调用姿势错误"。
const exitUsage = 64

// exitCodeFor 是退出码裁决钩子：errChanges（有变化）→ 2；用法类 → 64；
// 其余 → 1。
func exitCodeFor(err error) int {
	if errors.Is(err, errChanges) {
		return 2
	}
	var unknown *commands.UnknownVerbError
	var usage *commands.UsageError
	if errors.As(err, &unknown) || errors.As(err, &usage) {
		return exitUsage
	}
	return commands.ExitError
}
