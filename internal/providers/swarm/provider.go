// Package swarm 实现 Runtime Capability 的 swarm Provider：载体命名/标记
// 全部私有（fleetly.* 标记 + 命名公式），平台永不解析。
//
// 真机实证坑（归档仓 Docker29 多节点经验，实现必读）：
//   - swarm 不应用 Hosts；nft 规则可能杀 DNAT——Route 流量一律经 Edge
//     （traefik）而非端口发布；本 Provider 不发布宿主端口。
//   - digest 不落 tag：Ensure 收到的镜像引用由平台解析；预拉场景按 tag
//     直拉（digest-pull save/load 丢 tag）。
//   - 调度约束必须走节点 label 公式（fleetly.node.id==<平台节点 ID>），
//     不用 node.hostname/ID 直引用——跨 Runtime 节点 ID 永不复用。
//   - musl/glibc 混部镜像按用户责任；平台不翻译。
package swarm

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Provider 是 swarm Runtime Provider。
type Provider struct {
	cli *client.Client

	// 可注入的最小 docker 面缝（hermetic 单测注入 fake，不依赖真 daemon；
	// nil = 真实现直连 cli）。只覆盖需要无 daemon 测试的窄路径：
	//   - listContainers/openContainerLog：日志合流（Q-4/P1-15）。
	//   - networkInspect/secretInspect：材料 create-or-get 分诊（Q-20）。
	// 缝契约与真实现一致（如日志流读端必须在 ctx 取消时解除阻塞）。
	listContainers   func(ctx context.Context, ns capability.NamespaceRef) ([]container.Summary, error)
	openContainerLog func(ctx context.Context, containerID string, opts client.ContainerLogsOptions) (io.ReadCloser, error)
	networkInspect   func(ctx context.Context, name string) error
	secretInspect    func(ctx context.Context, name string) (client.SecretInspectResult, error)
}

// 编译期契约断言：核心面 + 三个子面，共四个面（F0.19 全契约；C-10 补
// Inspector 面——缺此断言则接口改名时静默降级 gen-only 无红灯）。
var (
	_ capability.Runtime          = (*Provider)(nil)
	_ capability.RuntimeLogs      = (*Provider)(nil)
	_ capability.RuntimeAdmin     = (*Provider)(nil)
	_ capability.RuntimeInspector = (*Provider)(nil)
)

// New 构造 Provider：host 为 daemon 端点（空 = DOCKER_HOST / 默认套接字）。
func New(ctx context.Context, host string) (*Provider, error) {
	opts := []client.Opt{}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("swarm provider: docker client: %w", err)
	}
	return &Provider{cli: cli}, nil
}

// Close 释放底层连接。
func (p *Provider) Close() error { return p.cli.Close() }

// Describe 实现 Provider 契约三件套之一。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "swarm",
		Capability: capability.KindRuntime,
		Version:    "1",
		Notes: []string{
			// 能力发现端点的诚实边界声明（架构 §10）。
			"network isolation is soft: per-project overlay without NetworkPolicy; egress:none is weak (outbound not blocked)",
			"workload identity is carried by fleetly.* labels; platform node IDs never reuse",
			// 镜像假设声明（N1 审查 P1-11）：shell 探针方言假定镜像自带
			// busybox 兼容的 nc/wget；无 shell 工具的镜像走 exec 探针。
			"tcp/http probes assume the image ships busybox-compatible nc/wget; distroless images should declare an exec probe",
		},
	}
}

// Health 实现 Provider 契约三件套之一（降级矩阵驱动：Runtime 宕 = 平台
// 不可部署，已运行 Workload 不受影响）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	if _, err := p.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
	}
	info, err := p.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker info failed: " + err.Error()}
	}
	if info.Info.Swarm.NodeID == "" {
		return capability.HealthReport{Healthy: false, Details: "docker daemon is not in swarm mode; run 'docker swarm init' or use fleetly install"}
	}
	return capability.HealthReport{Healthy: true, Details: "swarm control plane reachable"}
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）；daemon 端点经标准
// DOCKER_HOST 环境变量覆盖（容器形态 bind docker.sock 为默认形态）。
func init() {
	capability.RegisterFactory(capability.KindRuntime, "swarm", func(ctx context.Context) (capability.Provider, error) {
		return New(ctx, os.Getenv("DOCKER_HOST"))
	})
}
