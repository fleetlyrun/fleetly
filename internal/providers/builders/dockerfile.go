package builders

// dockerfile Builder Provider（ADR-0019 起的原始构建面；ADR-0032 收编进
// builders 家族包）：用户上下文内的 Dockerfile → dockerfile.v0 前端构建
// → 共享 solveAndPush 链。

import (
	"context"
	"os"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// DockerfileProvider 是 dockerfile Builder Provider。
type DockerfileProvider struct {
	d *daemonClients
}

// 编译期契约断言。
var _ capability.Builder = (*DockerfileProvider)(nil)

// NewDockerfile 构造 Provider：host 为 daemon 端点（空 = DOCKER_HOST /
// 默认套接字）。
func NewDockerfile(ctx context.Context, host string) (*DockerfileProvider, error) {
	d, err := newDaemonClients(ctx, host)
	if err != nil {
		return nil, err
	}
	return &DockerfileProvider{d: d}, nil
}

// Close 释放底层连接。
func (p *DockerfileProvider) Close() error { return p.d.Close() }

// Describe 实现 Provider 契约三件套之一。
func (p *DockerfileProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "dockerfile",
		Capability: capability.KindBuilder,
		Version:    "1",
		Notes: []string{
			"builds always run on the control-plane node with the local daemon (ADR-0019)",
			"built images are pushed to the managed registry and dispatched by digest (ADR-0019 appendix B)",
			"build cache is node-local; multi-node cache distribution lands with the builder-expansion batch",
		},
	}
}

// Health 实现 Provider 契约三件套之一（降级矩阵：Builder 宕 → 镜像引用
// Source 的部署不受影响）。
func (p *DockerfileProvider) Health(ctx context.Context) capability.HealthReport {
	if err := p.d.ping(ctx); err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
	}
	return capability.HealthReport{Healthy: true, Details: "local daemon buildkit reachable"}
}

// Build 执行一次构建：Dockerfile 相对 ContextDir（空 = "Dockerfile"）。
func (p *DockerfileProvider) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	attrs := map[string]string{"filename": dockerfile}
	for k, v := range req.Args {
		attrs["build-arg:"+k] = v
	}
	digest, err := p.d.solveAndPush(ctx, req, solveRequest{
		ContextDir:    req.ContextDir,
		DockerfileDir: req.ContextDir,
		Filename:      dockerfile,
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
	}, w)
	if err != nil {
		return capability.BuildResult{}, err
	}
	return capability.BuildResult{Digest: digest}, nil
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。
func init() {
	capability.RegisterFactory(capability.KindBuilder, "dockerfile", func(ctx context.Context) (capability.Provider, error) {
		return NewDockerfile(ctx, os.Getenv("DOCKER_HOST"))
	})
}
