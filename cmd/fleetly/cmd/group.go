package cmd

// groupVerb 是动词组分发（commands 嵌套 Dispatch 模式：外层动词把剩余
// 参数交给内层 App.Dispatch——错误类型原生上抛，归档仓 SubDispatch 丢
// 错误类型的坑由 Dispatch 直返收口）。无子命令时列出子命令（人类/机器
// 双形态——组本身也是 --json 契约面）。

import (
	"context"
	"fmt"

	"github.com/lynx-go/commands"
)

func groupVerb(name, synopsis string, subs ...commands.Command) commands.Command {
	inner := commands.New()
	inner.HelpHeader = "fleetly " + name + " — " + synopsis
	inner.VerbTitle = "subcommands:"
	inner.Register(subs...)
	subNames := inner.Names()
	return &flaggedVerb{
		name:     name,
		synopsis: synopsis,
		usage:    name + " <subcommand> [flags] (run without arguments to list subcommands)",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) == 0 {
				if jsonOut {
					return writeJSON(env.Stdout, subNames)
				}
				for _, n := range subNames {
					_, _ = fmt.Fprintln(env.Stdout, n)
				}
				return nil
			}
			return inner.Dispatch(ctx, env, args)
		},
	}
}
