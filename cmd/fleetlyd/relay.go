package main

// relay 动词（F3.2，ADR-0049；H10 改节点个体凭证）：节点中继——在节点上
// 经 EnrollNode 定向材料（RuntimeRelayMaterial.NodeRelayCommand）生成的
// 装载脚本里，由 busybox 载体容器运行（manager 节点是进程内回环中继，
// 不经本入口）。只读节点本地 docker 套接字 + 出站拨号控制面 gateway：无
// 配置文件、无状态库、无平台安装物。Provider 经注册表装配（blank import
// 已触发 swarm 工厂注册；未来 Runtime 换届时中继材料由新 Provider 生成、
// 本入口零改动）。commands 框架分发；旗标形态与脚本生成命令钉死
//（exec_script_test golden：`fleetlyd relay --manager URL --relay-credential
// CREDENTIAL`）。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lynx-go/commands"
	"github.com/lynx-go/lynx/buildinfo"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// relayVerb 是节点中继入口动词。信号自持（NotifyContext 在 Run 内建，
// 不依赖调用方传入的 ctx 形态——脚本生成的裸进程直接起服）。
type relayVerb struct {
	manager         *string
	relayCredential *string
	runtimeName     *string
}

func newRelayVerb() commands.Command { return &relayVerb{} }

func (v *relayVerb) Name() string { return "relay" }

func (v *relayVerb) Synopsis() string {
	return "node relay for enrolled nodes (dials the control-plane gateway; runs in the node carrier container)"
}

func (v *relayVerb) Usage() string {
	return "relay --manager URL --relay-credential CREDENTIAL [--runtime name]"
}

func (v *relayVerb) SetFlags(fs *flag.FlagSet) {
	v.manager = fs.String("manager", "", "control-plane gateway base URL (e.g. http://10.0.0.3:9081; required)")
	v.relayCredential = fs.String("relay-credential", "", "node relay credential issued by `fleetly nodes relay` (fltrelay_...; required)")
	v.runtimeName = fs.String("runtime", "swarm", "runtime provider name (as enrolled by the platform)")
}

// Run 执行中继主循环；缺必填旗标经 UsageError 退 2，运行失败退 1。
func (v *relayVerb) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) > 0 {
		return usageErr(v.Usage(), "unexpected argument %q", args[0])
	}
	if *v.manager == "" || *v.relayCredential == "" {
		return usageErr(v.Usage(), "--manager and --relay-credential are required")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	provider, err := capability.Build(ctx, capability.KindRuntime, *v.runtimeName)
	if err != nil {
		return fmt.Errorf("runtime provider %q unavailable: %w", *v.runtimeName, err)
	}
	exec, ok := provider.(capability.RuntimeExec)
	if !ok {
		return fmt.Errorf("runtime provider %q has no exec subface", *v.runtimeName)
	}
	// worker 形态返回启动注入的固定节点个体凭证（重铸后旧值 401，待重跑
	// 新脚本；carrier 参数在此形态不使用——凭证与节点身份在平台侧绑定）。
	return exec.RunNodeRelay(ctx, capability.NodeRelayOptions{
		GatewayURL: *v.manager,
		Credential: func(context.Context, string) (string, error) { return *v.relayCredential, nil },
		Version: func() string {
			if v := buildinfo.Version; v != "" {
				return v
			}
			return "dev"
		}(),
	})
}
