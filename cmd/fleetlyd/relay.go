package main

// relay 子命令（F3.2，ADR-0049）：节点中继——在节点上经
// EnrollKit.RelayCommand 装载的 busybox 载体容器内运行（manager 节点是
// 进程内回环中继，不经本入口）。只读节点本地 docker 套接字 + 出站拨号
// 控制面 gateway：无配置文件、无状态库、无平台安装物。Provider 经注册表
// 装配（blank import 已触发 swarm 工厂注册；未来 Runtime 换届时
// RelayCommand 由新 Provider 生成、本入口零改动）。

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// runRelay 执行中继主循环；返回进程退出码（admin 同款收口）。
func runRelay(args []string) int {
	fs := pflag.NewFlagSet("fleetlyd relay", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manager := fs.String("manager", "", "control-plane gateway base URL (e.g. http://10.0.0.3:9081; required)")
	joinToken := fs.String("join-token", "", "swarm join token (cluster-membership credential; required)")
	runtimeName := fs.String("runtime", "swarm", "runtime provider name (as enrolled by the platform)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manager == "" || *joinToken == "" {
		fmt.Fprintln(os.Stderr, "fleetlyd relay: --manager and --join-token are required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	provider, err := capability.Build(ctx, capability.KindRuntime, *runtimeName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fleetlyd relay: runtime provider %q unavailable: %v\n", *runtimeName, err)
		return 1
	}
	exec, ok := provider.(capability.RuntimeExec)
	if !ok {
		fmt.Fprintf(os.Stderr, "fleetlyd relay: runtime provider %q has no exec sub-face\n", *runtimeName)
		return 1
	}
	if err := exec.RunNodeRelay(ctx, capability.NodeRelayOptions{
		GatewayURL: *manager,
		JoinToken:  func(context.Context) (string, error) { return *joinToken, nil },
		Version: func() string {
			if version != "" {
				return version
			}
			return "dev"
		}(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "fleetlyd relay: %v\n", err)
		return 1
	}
	return 0
}
