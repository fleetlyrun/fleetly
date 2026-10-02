package builders

// static Builder Provider（ADR-0032）：产物目录 + 钉版 Caddy 包装——无
// 构建步（源码构建是 railpack 的地盘），把上传上下文的产物子目录整体
// COPY 进 caddy:2.11-alpine 并注入生成的 Caddyfile（:8080 + SPA 回退 +
// file_server）。生成的 Dockerfile 落 per-build overlay 临时目录（挂
// dockerfile mount），不写用户上下文（"解包是纯函数"不变式保持）。

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// staticServerImage 是钉版伺服镜像（major.minor + 变体档，ADR-0032 决策 6：
// zot 全 patch / postgres major+suite 之间的中间档）。
const staticServerImage = "caddy:2.11-alpine"

// staticListenPort 是包装镜像的监听端口（Caddyfile 与 Describe 面单源）。
const staticListenPort = 8080

// staticCaddyfile 是生成的 Caddyfile（SPA history-mode 回退是静态站点的
// 主流形态；不带自定义面——需要时用户自带 Dockerfile）。端口经常量
// 内插（:8080 字面量的唯一真源是 staticListenPort）。
var staticCaddyfile = fmt.Sprintf(`:%d {
	root * /srv
	try_files {path} /index.html
	file_server
}
`, staticListenPort)

// StaticProvider 是 static Builder Provider。
type StaticProvider struct {
	d *daemonClients
}

// 编译期契约断言。
var _ capability.Builder = (*StaticProvider)(nil)

// NewStatic 构造 Provider：host 为 daemon 端点。
func NewStatic(ctx context.Context, host string) (*StaticProvider, error) {
	d, err := newDaemonClients(ctx, host)
	if err != nil {
		return nil, err
	}
	return &StaticProvider{d: d}, nil
}

// Close 释放底层连接。
func (p *StaticProvider) Close() error { return p.d.Close() }

// Describe 实现 Provider 契约三件套之一。
func (p *StaticProvider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "static",
		Capability: capability.KindBuilder,
		Version:    "1",
		Notes: []string{
			fmt.Sprintf("serves an artifact directory with pinned %s (listens on :%d, SPA history-mode fallback included); it does not run a build step — build from source with the railpack builder", staticServerImage, staticListenPort),
		},
	}
}

// Health 实现 Provider 契约三件套之一。
func (p *StaticProvider) Health(ctx context.Context) capability.HealthReport {
	if err := p.d.ping(ctx); err != nil {
		return capability.HealthReport{Healthy: false, Details: "docker daemon unreachable: " + err.Error()}
	}
	return capability.HealthReport{Healthy: true, Details: "local daemon buildkit reachable"}
}

// Build 执行一次包装构建：overlay 目录写生成的 Dockerfile（Caddyfile 经
// heredoc COPY 注入 /etc/caddy/Caddyfile——dockerfile 1.4 heredoc，不写
// 用户上下文、不需要额外 named context）→ dockerfile.v0 Solve → 共享
// 推送链。
func (p *StaticProvider) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	if req.Static == nil {
		return capability.BuildResult{}, fmt.Errorf("static: request carries no static strategy payload (routing mismatch)")
	}
	outputDir, err := cleanStaticOutputDir(req.Static.OutputDir)
	if err != nil {
		return capability.BuildResult{}, err
	}
	if req.ContextDir == "" {
		return capability.BuildResult{}, fmt.Errorf("static: build context directory is required")
	}
	if w == nil {
		return capability.BuildResult{}, fmt.Errorf("static: a log writer is required")
	}

	overlayDir, err := os.MkdirTemp("", "fleetly-static-"+req.BuildID+"-")
	if err != nil {
		return capability.BuildResult{}, fmt.Errorf("static: overlay dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(overlayDir) }() //nolint:errcheck // 临时目录清理失败不致命
	dockerfile := staticDockerfile(outputDir)
	if err := os.WriteFile(path.Join(overlayDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		return capability.BuildResult{}, fmt.Errorf("static: write generated Dockerfile: %w", err)
	}

	digest, err := p.d.solveAndPush(ctx, req, solveRequest{
		ContextDir:    req.ContextDir,
		DockerfileDir: overlayDir,
		Filename:      "Dockerfile",
		Frontend:      "dockerfile.v0",
		FrontendAttrs: map[string]string{"filename": "Dockerfile"},
	}, w)
	if err != nil {
		return capability.BuildResult{}, err
	}
	return capability.BuildResult{Digest: digest}, nil
}

// staticDockerfile 生成包装 Dockerfile（Caddyfile 层在前——生成物常量，
// 产物层在后提高缓存命中）。
func staticDockerfile(outputDir string) string {
	return "FROM " + staticServerImage + "\n" +
		"COPY <<'EOF' /etc/caddy/Caddyfile\n" + staticCaddyfile + "EOF\n" +
		"COPY " + outputDir + "/ /srv/\n"
}

// cleanStaticOutputDir 归一并复验产物目录（叶子校验在受理面已执法；此处
// 防御纵深——Revision 冻结体可能先于校验面存在）。
func cleanStaticOutputDir(outputDir string) (string, error) {
	if outputDir == "" {
		return ".", nil
	}
	clean := path.Clean(outputDir)
	if path.IsAbs(outputDir) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(outputDir, "\\") {
		return "", fmt.Errorf("static: output_dir %q must stay inside the build context", outputDir)
	}
	return clean, nil
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。
func init() {
	capability.RegisterFactory(capability.KindBuilder, "static", func(ctx context.Context) (capability.Provider, error) {
		return NewStatic(ctx, os.Getenv("DOCKER_HOST"))
	})
}
