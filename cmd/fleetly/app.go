// fleetly CLI：fleetly 平台命令行（lynx-go/commands 动词注册，D21）。
//
// T2-2 阶段命令面：validate/plan/diff——compose 受控子集校验、归一化与
// 归一化差异（internal/compose）。plan/apply 的真实执行依赖引擎层
// （T2.10），本期 plan/diff 只产出基于归一化差异的计划；DB 基线（上一
// revision 快照）随引擎票接入，当前以 --baseline 另一份 compose 文件或
// 空基线（首部署语义）替代。
package main

import (
	"context"
	"errors"
	"os"

	"github.com/lynx-go/commands"
)

// version 经构建 -ldflags "-X main.version=..." 注入；未注入时为 dev。
var version = "dev"

func main() {
	env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(newApp().Run(context.Background(), env, os.Args[1:]))
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
		&buildCmd{},
		newBuildsCmd(),
		&deployCmd{},
		newDeploymentsCmd(),
		&rollbackCmd{},
		newRevisionsCmd(),
		newDriftCmd(),
		newEnvCmd(),
		newPlacementCmd(),
		newNodesCmd(),
	)
	// 三态退出码（架构 §2.4 plan/apply 语义）：0=无变化/成功、2=有变化、
	// 1=错误。用法类错误（未知动词/flag 解析失败）沿用框架约定退出 2。
	app.ExitCode = exitCodeFor
	app.RenderError = renderCLIError
	app.HelpHeader = "fleetly — fleetly platform CLI (" + version + ")"
	app.VerbTitle = "verbs:"
	return app
}

// exitCodeFor 是退出码裁决钩子：errChanges（有变化）→ 2；用法类 → 2；
// 其余 → 1。
func exitCodeFor(err error) int {
	if errors.Is(err, errChanges) {
		return 2
	}
	var unknown *commands.UnknownVerbError
	var usage *commands.UsageError
	if errors.As(err, &unknown) || errors.As(err, &usage) {
		return commands.ExitUsage
	}
	return commands.ExitError
}
