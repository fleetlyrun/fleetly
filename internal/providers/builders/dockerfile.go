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
			"cache mounts (--mount=type=cache) are namespaced per app push target; layer cache stays shared (content-addressed)",
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

// dockerfileFrontendAttrs 组装 dockerfile.v0 前端属性（用户 build-args +
// 平台裁决面）。
func dockerfileFrontendAttrs(req capability.BuildRequest, dockerfile string) map[string]string {
	attrs := map[string]string{"filename": dockerfile}
	for k, v := range req.Args {
		attrs["build-arg:"+k] = v
	}
	// BUILDKIT_CACHE_MOUNT_NS 是 dockerfile.v0 的 cache mount 命名空间
	// （buildkit dockerfile2llb 对全部 --mount=type=cache 的 cacheID 加
	// 前缀 <ns>/<id>）。buildkit 缓存节点本地共享（ADR-0019：构建恒在控
	// 制面节点），无命名空间时 cache mount 是跨 App 共享读写面（一个 App
	// 的 Dockerfile 可读到另一 App 写入的同 target 缓存）。前缀取推送目标
	// repo 前缀——与 railpack 轨的 cache-key 同粒度（per-App、跨 revision
	// 稳定；BuildID 与 r<seq> tag 都不稳定）。平台键后置覆写用户 Args：
	// 命名空间是平台裁决，用户同名 build-arg 不得漂移。空 Target（入口
	// 校验前的防御路径）不设键，行为同历史。
	if ns := targetRepoPrefix(req.Target); ns != "" {
		attrs["build-arg:BUILDKIT_CACHE_MOUNT_NS"] = ns
	}
	return attrs
}

// Build 执行一次构建：Dockerfile 相对 ContextDir（空 = "Dockerfile"）。
func (p *DockerfileProvider) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	digest, err := p.d.solveAndPush(ctx, req, solveRequest{
		ContextDir:    req.ContextDir,
		DockerfileDir: req.ContextDir,
		Filename:      dockerfile,
		Frontend:      "dockerfile.v0",
		FrontendAttrs: dockerfileFrontendAttrs(req, dockerfile),
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
