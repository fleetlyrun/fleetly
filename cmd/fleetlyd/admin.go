package main

// admin.go 是 fleetlyd 的离线维护面（`fleetlyd admin <verb>`，commands 框
// 架分发）：停机窗口操作，不经 lynx runner 与 wire 装配——数据根被守护
// 进程持有时禁止执行（SQLite 单写者 + KEK 轮换窗口）。动词集保持极小，
// 逻辑在 internal/admin；输出人类形态默认 + --json（双形态惯例）。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/admin"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// newAdminGroup 装配 admin 动词组（嵌套 Dispatch：内层未命中原生上抛
// UnknownVerbError，退出码 2 契约不丢）。无子命令时列出子命令——与
// fleetly CLI 动词组同款形态。
func newAdminGroup() commands.Command {
	inner := commands.New()
	inner.HelpHeader = "fleetlyd admin - offline maintenance commands (stop fleetlyd first; run on the control-plane node)"
	inner.VerbTitle = "subcommands:"
	inner.Register(newRewrapVerb())
	subNames := inner.Names()
	return &verb{
		name:     "admin",
		synopsis: "offline maintenance commands (stop fleetlyd first; run on the control-plane node)",
		usage:    "admin <subcommand> [flags] (run without a subcommand to list subcommands)",
		run: func(ctx context.Context, env *commands.Environment, args []string) error {
			if len(args) == 0 {
				for _, n := range subNames {
					_, _ = fmt.Fprintln(env.Stdout, n)
				}
				return nil
			}
			return inner.Dispatch(ctx, env, args)
		},
	}
}

// rewrapVerb 是 KEK 轮换的收口动词：全量重封存储侧加密行；无 --execute
// 时是 dry run（解封验证 + 报告，不落库）。
type rewrapVerb struct {
	dataRoot *string
	execute  *bool
	asJSON   *bool
}

func newRewrapVerb() commands.Command { return &rewrapVerb{} }

func (v *rewrapVerb) Name() string { return "rewrap" }

func (v *rewrapVerb) Synopsis() string {
	return "re-seal every stored encrypted row with the active master key (dry run without --execute)"
}

func (v *rewrapVerb) Usage() string {
	return "admin rewrap [--data-root DIR] [--execute] [--json]"
}

func (v *rewrapVerb) SetFlags(fs *flag.FlagSet) {
	v.dataRoot = fs.String("data-root", config.DefaultDataRoot, "platform data root holding fleetly.db and keys/")
	v.execute = fs.Bool("execute", false, "write the rewrapped rows (default is a dry run)")
	v.asJSON = fs.Bool("json", false, "print the report as JSON")
}

// Run 执行 rewrap；用法错误经 UsageError 退 2，操作失败原样返回退 1。
func (v *rewrapVerb) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) > 0 {
		return usageErr(v.Usage(), "unexpected argument %q", args[0])
	}
	cipher, err := material.LoadExistingCipher(*v.dataRoot)
	if err != nil {
		return err
	}
	db, err := state.Open(ctx, filepath.Join(*v.dataRoot, "fleetly.db"), state.WallClock())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // 停机窗口内的只读收尾，关闭错误无处置面

	rep, err := admin.RewrapSecrets(ctx, db, cipher, !*v.execute)
	if *v.asJSON && rep != nil {
		if out, jerr := json.MarshalIndent(rep, "", "  "); jerr == nil {
			_, _ = fmt.Fprintln(env.Stdout, string(out))
		}
	}
	printRewrapReport(env.Stdout, rep, *v.execute)
	return err
}

// printRewrapReport 输出人类形态报告（--json 的结构化面已先行打印；
// 四列契约见 staging runbook 的 KEK 轮换操作序）。
func printRewrapReport(w io.Writer, rep *admin.RewrapReport, executed bool) {
	if rep == nil {
		return
	}
	action := "to rewrap"
	if executed {
		action = "rewrapped"
	}
	_, _ = fmt.Fprintf(w, "secrets: %d total, %d %s, %d already current, %d failed\n",
		rep.SecretsTotal, rep.SecretsRewrapped, action, rep.SecretsCurrent, countFailures(rep, "secret"))
	_, _ = fmt.Fprintf(w, "hooks:   %d total, %d %s, %d already current, %d failed\n",
		rep.HooksTotal, rep.HooksRewrapped, action, rep.HooksCurrent, countFailures(rep, "hook"))
	for _, f := range rep.Failures {
		_, _ = fmt.Fprintf(w, "failed %s %s: %s\n", f.Kind, f.Key, f.Err)
	}
	if executed {
		_, _ = fmt.Fprintln(w, "rewrap complete")
	} else {
		_, _ = fmt.Fprintln(w, "dry run: nothing was written; re-run with --execute to write")
	}
}

// countFailures 按行类别统计失败条数（报告的 failed 列）。
func countFailures(rep *admin.RewrapReport, kind string) int {
	n := 0
	for _, f := range rep.Failures {
		if f.Kind == kind {
			n++
		}
	}
	return n
}
