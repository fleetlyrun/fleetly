package main

// admin.go 是 fleetlyd 的离线维护面壳（`fleetlyd admin <verb>`）：停机窗
// 口操作，不经 lynx runner 与 wire 装配——数据根被守护进程持有时禁止执
// 行（SQLite 单写者 + KEK 轮换窗口）。动词集保持极小，逻辑在
// internal/admin；输出人类形态默认 + --json（双形态惯例）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/admin"
	"github.com/fleetlyrun/fleetly/internal/config"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
)

const adminUsage = `usage: fleetlyd admin <command> [flags]

Offline maintenance commands (stop fleetlyd first; run on the control-plane node):

  rewrap    re-seal every stored encrypted row with the active master key:
            all secrets (including database credentials and tombstoned rows)
            and git hook webhook secrets. Retired keys (keys/master-*.agekey)
            are loaded for decryption only.

  rewrap flags:
    --data-root DIR   platform data root holding fleetly.db and keys/
                      (default "%s"; staging: /var/lib/fleetly)
    --execute         write the rewrapped rows; without this flag the
                      command is a dry run that only verifies and reports
    --json            print the report as JSON
`

// runAdmin 执行 admin 子命令；用法错误自行退出（exit 2），操作失败经返
// 回值走 main 的统一退出路径。
func runAdmin(args []string) int {
	if len(args) == 0 || args[0] != "rewrap" {
		fmt.Fprintf(os.Stderr, adminUsage, config.DefaultDataRoot)
		if len(args) > 0 {
			fmt.Fprintf(os.Stderr, "unknown admin command %q\n", args[0])
		}
		return 2
	}
	fs := pflag.NewFlagSet("fleetlyd admin rewrap", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dataRoot := fs.String("data-root", config.DefaultDataRoot, "platform data root holding fleetly.db and keys/")
	execute := fs.Bool("execute", false, "write the rewrapped rows (default is a dry run)")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2 // pflag 已向 stderr 输出错误与用法
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	ctx := context.Background()
	cipher, err := material.LoadExistingCipher(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	db, err := state.Open(ctx, filepath.Join(*dataRoot, "fleetly.db"), state.WallClock())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close() //nolint:errcheck // 停机窗口内的只读收尾，关闭错误无处置面

	rep, err := admin.RewrapSecrets(ctx, db, cipher, !*execute)
	if *asJSON && rep != nil {
		if out, jerr := json.MarshalIndent(rep, "", "  "); jerr == nil {
			fmt.Println(string(out))
		}
	}
	printRewrapReport(rep, *execute)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// printRewrapReport 输出人类形态报告（--json 的结构化面已先行打印）。
func printRewrapReport(rep *admin.RewrapReport, executed bool) {
	if rep == nil {
		return
	}
	verb := "to rewrap"
	if executed {
		verb = "rewrapped"
	}
	fmt.Printf("secrets: %d total, %d %s, %d already current, %d failed\n",
		rep.SecretsTotal, rep.SecretsRewrapped, verb, rep.SecretsCurrent, countFailures(rep, "secret"))
	fmt.Printf("hooks:   %d total, %d %s, %d already current, %d failed\n",
		rep.HooksTotal, rep.HooksRewrapped, verb, rep.HooksCurrent, countFailures(rep, "hook"))
	for _, f := range rep.Failures {
		fmt.Printf("failed %s %s: %s\n", f.Kind, f.Key, f.Err)
	}
	if executed {
		fmt.Println("rewrap complete")
	} else {
		fmt.Println("dry run: nothing was written; re-run with --execute to write")
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
