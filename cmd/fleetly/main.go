// fleetly CLI 进程入口：只保留信号挂接与版本注入（-ldflags
// "-X main.version=..."）。全部命令定义与动词装配在子包 cmd（NewApp
// 单点，版本经参数传入）。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/cmd/fleetly/cmd"
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
	os.Exit(cmd.NewApp(version).Run(ctx, env, os.Args[1:]))
}
