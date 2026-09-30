// Package dockerbuild 实现 Builder Capability 的 dockerfile Provider
// （ADR-0019：Build 恒在控制面节点——BuildKit + 本机 daemon，缓存只在本
// 机，无独立 buildkitd）。连接路径与 buildx docker-driver 同源：经 daemon
// /session 端点 h2c hijack 建立 gRPC，buildkit client 在其上 Solve。
package dockerbuild

import (
	"context"
	"fmt"
	"net"
	"os"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Provider 是 dockerfile Builder Provider。
type Provider struct {
	cli *client.Client
	bk  *bkclient.Client
}

// 编译期契约断言。
var _ capability.Builder = (*Provider)(nil)

// New 构造 Provider：host 为 daemon 端点（空 = DOCKER_HOST / 默认套接字）。
func New(ctx context.Context, host string) (*Provider, error) {
	opts := []client.Opt{}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("dockerbuild: docker client: %w", err)
	}
	// daemon 内嵌 buildkit 的双通道（buildx docker-driver 同路径）：
	//   - /grpc：主 gRPC（Solve 等），WithContextDialer；
	//   - /session：session attach（local context 上传经此），WithSessionDialer。
	// URL 需绝对形态——DialHijack 直接构造 http.Request，相对路径会写出
	// 空 Host 被拒绝；scheme/host 仅占位，实际经 client 的 dialer 落到
	// daemon 端点。不引入独立 buildkitd（ADR-0019：本机 daemon）。
	dial := func(path string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, _ string) (net.Conn, error) {
			return cli.DialHijack(ctx, "http://docker"+path, "h2c", nil)
		}
	}
	bk, err := bkclient.New(ctx, "",
		bkclient.WithContextDialer(dial("/grpc")),
		bkclient.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return cli.DialHijack(ctx, "http://docker/session", proto, meta)
		}),
	)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("dockerbuild: buildkit client: %w", err)
	}
	return &Provider{cli: cli, bk: bk}, nil
}

// Close 释放底层连接。
func (p *Provider) Close() error {
	_ = p.bk.Close()
	return p.cli.Close()
}

// Describe 实现 Provider 契约三件套之一。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "dockerfile",
		Capability: capability.KindBuilder,
		Version:    "1",
		Notes: []string{
			"builds always run on the control-plane node with the local daemon (ADR-0019)",
			"build cache is node-local; multi-node cache distribution lands with the registry batch",
		},
	}
}

// Health 实现 Provider 契约三件套之一（降级矩阵：Builder 宕 → 镜像引用
// Source 的部署不受影响）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	if _, err := p.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
	}
	return capability.HealthReport{Healthy: true, Details: "local daemon buildkit reachable"}
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。
func init() {
	capability.RegisterFactory(capability.KindBuilder, "dockerfile", func(ctx context.Context) (capability.Provider, error) {
		return New(ctx, os.Getenv("DOCKER_HOST"))
	})
}
