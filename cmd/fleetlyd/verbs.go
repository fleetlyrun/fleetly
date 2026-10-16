package main

// verbs.go 是 fleetlyd 的动词面装配（commands 框架，与 fleetly CLI 同款
// 技术栈）：admin（离线维护组）与 relay（节点中继）两个顶层动词。空参/
// 纯旗标不进本面——那是 daemon 引导路径（main.go 的 lynx runner）；未知
// 首参由框架报 unknown verb 退 2 并附 help，staging 实证 2026-10-03 的
// 流浪 daemon 防线由分发器接管（旧 validateFirstArg 退役）。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	"github.com/lynx-go/lynx/buildinfo"
)

// verb 是 fleetlyd 动词的统一形态（fleetly CLI flaggedVerb 的精简版：
// 本面无全局 root bool flag——机器形态是 rewrap 的自有旗标 --json）。
type verb struct {
	name     string
	synopsis string
	usage    string
	setFlags func(fs *flag.FlagSet) // 可选：追加旗标声明
	run      func(ctx context.Context, env *commands.Environment, args []string) error
}

func (v *verb) Name() string     { return v.name }
func (v *verb) Synopsis() string { return v.synopsis }
func (v *verb) Usage() string    { return v.usage }

func (v *verb) SetFlags(fs *flag.FlagSet) {
	if v.setFlags != nil {
		v.setFlags(fs)
	}
}

func (v *verb) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return v.run(ctx, env, args)
}

// usageErr 用法错误（框架按 UsageError 类型退 2，与既有 admin/relay
// 契约一致：用法错 2、操作失败 1）。
func usageErr(usage, format string, a ...any) error {
	return &commands.UsageError{Usage: usage, Err: fmt.Errorf(format, a...)}
}

// newVerbApp 装配 fleetlyd 动词面。退出码走框架默认（0 成功/1 命令错/
// 2 用法错），FlagError 默认已包成 UsageError——无需定制钩子。
func newVerbApp(info buildinfo.BuildInfo) *commands.App {
	app := commands.New()
	app.HelpHeader = "fleetlyd - fleetly control-plane daemon (start with flags only)"
	app.VerbTitle = "commands:"
	version := info.Version
	if version == "" {
		version = "dev"
	}
	app.HelpFooter = fmt.Sprintf("fleetlyd %s (run without a subcommand to start the daemon; 'fleetlyd help <command>' for details)", version)
	app.Register(newVersionVerb(info), newAdminGroup(), newRelayVerb())
	return app
}

// newVersionVerb 是 `fleetlyd version`：打印版本三元组退 0（staging 两次
// 实证的版本探查正解——journalctl 之外的直接面；未知动词曾引导流浪
// daemon，该笔误形态自 841ca20 起由分发器拒绝，本动词把它转为合法出口）。
func newVersionVerb(info buildinfo.BuildInfo) *verb {
	return &verb{
		name:     "version",
		synopsis: "Print the fleetlyd version and exit",
		usage:    "version",
		run: func(_ context.Context, env *commands.Environment, args []string) error {
			if len(args) > 0 {
				return usageErr("version", "unexpected argument %q", args[0])
			}
			line := "fleetlyd " + info.Display()
			_, err := fmt.Fprintln(env.Stdout, line)
			return err
		},
	}
}
