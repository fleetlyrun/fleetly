// fleetly 是 CLI 瘦客户端：只走 fleetlyd API，无本地特权操作（架构
// 文档 §1）。命令实现在 cmd 子包。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/cmd/fleetly/cmd"
	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// version/commit/date 由 mise build 的 ldflags 注入。
var version, commit, date string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	info := buildinfo.BuildInfo{Version: version, Commit: commit, Date: date}
	env := &commands.Environment{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(cmd.NewApp(info).Run(ctx, env, os.Args[1:]))
}
