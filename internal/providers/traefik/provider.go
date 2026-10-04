// Package traefik 实现 Edge Capability 的 traefik Provider：受管自宿
// （ADR-0004 首实例——以普通 Workload 形态跑在 Runtime 上，经通用
// managedprovider reconciler 部署），配置经 HTTP provider 拉取端点下发
// （控制面是真源，强制全量配置防裸 {} 清空——旧 spike 教训）。LE 证书走
// traefik 原生 ACME HTTP-01（2026-09-30 裁决；平台 Certificate 面做观测
// 记录），默认 staging CA 防误触发生产配额。
package traefik

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Image 是受管 traefik 镜像（钉版；升级经 Platform 升级序，ADR-0015）。
const Image = "traefik:v3.5.4"

// acmeVolumeID 是受管 ACME 存储卷的平台 ID。fleetly- 前缀与用户 Volume
// 名空间隔离（C5：用户卷名 "edge-acme" 不得撞上受管卷——载体名公式只按
// VolumeID 拼，前缀即边界）。
const acmeVolumeID = "fleetly-edge-acme"

// Provider 是 traefik Edge Provider。
type Provider struct {
	// configEndpoint 是控制面 HTTP provider 拉取端点（受管实例的
	// --providers.http.endpoint 值）。
	configEndpoint string
	acmeEmail      string
	// authToken 是拉取端点共享令牌（空 = 端点无认证现状；非空经
	// --providers.http.headers 同头携带，ADR-0036 N2 兑现）。
	authToken string

	mu     sync.RWMutex
	schema []byte // 最近发布的全量动态配置（拉取端点快照）
}

// 编译期契约断言：Edge 端口 + 受管形态声明。
var (
	_ capability.Edge    = (*Provider)(nil)
	_ capability.Managed = (*Provider)(nil)
)

// New 构造 Provider。configEndpoint 形如 http://fleetlyd:9082/edge/config；
// authToken 非空时受管实例以 X-Fleetly-Edge-Token 头携带（端点侧常量时间
// 比对——空串维持无认证现状，升级零扰动）。
func New(configEndpoint, acmeEmail, authToken string) (*Provider, error) {
	if configEndpoint == "" {
		return nil, fmt.Errorf("traefik provider: config endpoint is required")
	}
	if acmeEmail == "" {
		acmeEmail = "fleetly@localhost"
	}
	return &Provider{
		configEndpoint: configEndpoint,
		acmeEmail:      acmeEmail,
		authToken:      authToken,
		schema:         emptyDynamicConfig(),
	}, nil
}

// Describe 实现 Provider 契约三件套之一（Managed=true：部署形态经
// ManagedWorkloads 声明，由通用 reconciler 部署）。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "traefik",
		Capability: capability.KindEdge,
		Version:    "1",
		Managed:    true,
		Notes: []string{
			"routes are served from the control-plane HTTP provider endpoint (control plane is the source of truth)",
			"existing routes keep serving while the Edge workload is down; route changes fail explicitly (degradation matrix)",
		},
	}
}

// Health 实现 Provider 契约三件套之一。受管 Workload 存活即配置面可用
// （拉取端点在本进程——进程活着配置就在）。
func (p *Provider) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true, Details: "config endpoint served by this control plane"}
}

// PublishRoutes 更新全量动态配置快照（幂等；traefik 按 pollInterval 拉取）。
// 发布前预检（P9）：生成物不过 schema 级校验 → 整快照拒绝、旧快照继续
// 服务——与 traefik 侧拒载语义（真机实证：整份丢弃 + last-known-good
// 服务 + 零日志）对齐，且把 5s poll 窗口与静默面在控制面侧提前消除。
func (p *Provider) PublishRoutes(_ context.Context, routes []capability.Route) error {
	schema, err := buildDynamicConfig(routes)
	if err != nil {
		return err
	}
	if err := validateDynamicConfig(schema); err != nil {
		return err
	}
	p.mu.Lock()
	p.schema = schema
	p.mu.Unlock()
	return nil
}

// ConfigSnapshot 返回当前全量配置快照（HTTP provider 拉取端点消费，
// capability.ConfigSource 子面）。
func (p *Provider) ConfigSnapshot() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]byte(nil), p.schema...)
}

// IssueCertificate：LE 由 traefik 原生 ACME 处理（router 标注 tls.domains
// 后 traefik 自动申请/续期）；本面返回受管实例的解析器声明即视为受理。
// 观测细节（经 traefik API 读证书状态）随 Console/证书页批次接入。
func (p *Provider) IssueCertificate(context.Context, capability.CertificateRequest, capability.ChallengeWriter) (capability.CertificateStatus, error) {
	return capability.CertificateStatus{}, nil
}

// ManagedNamespace 返回平台系统隔离域（与用户 Project 分离）。
func (p *Provider) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "edge"}
}

// ManagedWorkloads 声明受管部署形态（通用 reconciler 经 Runtime Ensure
// 下发；发布 80/443 是 Edge 部署形态的一部分）。Networks 由 reconciler
// 组装时合并活跃 Project 网络（跨网后端可达性）。
func (p *Provider) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{{
		ID:      "fleetly-edge-traefik",
		Process: "traefik",
		Image:   Image,
		Command: func() []string {
			cmd := []string{
				"traefik",
				"--entryPoints.web.address=:80",
				"--entryPoints.websecure.address=:443",
				// HTTP provider：控制面是配置真源（poll 拉取全量配置）。
				"--providers.http.endpoint=" + p.configEndpoint,
				"--providers.http.pollInterval=5s",
				// LE HTTP-01（traefik 原生；staging CA 防误触发生产配额，
				// 生产 CA 随安装引导批切换）。
				"--certificatesresolvers.le.acme.email=" + p.acmeEmail,
				"--certificatesresolvers.le.acme.storage=/acme/acme.json",
				"--certificatesresolvers.le.acme.caserver=https://acme-staging-v02.api.letsencrypt.org/directory",
				"--certificatesresolvers.le.acme.httpchallenge=true",
				"--certificatesresolvers.le.acme.httpchallenge.entrypoint=web",
				// 观测面（api 只读 dashboard，随 Console 批次决定暴露）。
				"--api.dashboard=false",
			}
			if p.authToken != "" {
				// 端点共享令牌（ADR-0036 N2 兑现）：traefik http provider
				// 的自定义头通道；令牌变更 = 载体 spec 变更 = 一次滚动
				// 替换（opt-in 动作窗口，操作者自知）。
				cmd = append(cmd, "--providers.http.headers.X-Fleetly-Edge-Token="+p.authToken)
			}
			return cmd
		}(),
		Ports: []capability.WorkloadPort{
			{Port: 80, Protocol: capability.ProtocolHTTP},
			{Port: 443, Protocol: capability.ProtocolTCP},
		},
		Publish: []capability.PortPublish{
			{PublishedPort: 80, TargetPort: 80},
			{PublishedPort: 443, TargetPort: 443},
		},
		Replicas: 1,
		Volumes: []capability.VolumeMount{
			{VolumeID: acmeVolumeID, Target: "/acme"},
		},
	}}
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。端点与邮箱经环境
// 变量注入，令牌走装配 ctx（config.server.edge_config.auth_token，唯一
// 契约源）+ env FLEETLY_EDGE_AUTH_TOKEN 同键兜底（ADR-0036 形态）。
func init() {
	capability.RegisterFactory(capability.KindEdge, "traefik", func(ctx context.Context) (capability.Provider, error) {
		token := capability.EdgeAuthTokenFromContext(ctx)
		if token == "" {
			token = os.Getenv("FLEETLY_EDGE_AUTH_TOKEN")
		}
		return New(os.Getenv("FLEETLY_EDGE_CONFIG_ENDPOINT"), os.Getenv("FLEETLY_EDGE_ACME_EMAIL"), token)
	})
}
