// Package k3s 实现 Runtime Capability 的 k3s Provider（ADR-0052）：域映射 =
// per-Project Namespace + 六轴 fleetly.* label（载体命名/标记全部私有，
// 平台永不解析）；网络 = Namespace 即互通域，egress:none 载体级 NetworkPolicy
// 强隔离。
//
// 真机实证坑（2026-10-07 dind 预研，实现必读）：
//   - dind（overlay 文件系统）上 containerd overlayfs snapshotter 不可用
//     （overlay-on-overlay 挂载被拒）——e2e 形态 k3s server 起动带
//     --snapshotter=native（生产节点原生文件系统不受影响）。
//   - 容器内直拉外网镜像不可靠——e2e 预载走
//     docker image save | k3s ctr images import -。
//   - NetworkPolicy 无需换 CNI：k3s 内嵌 kube-router netpol 库（默认 flannel
//     即支持；--disable-network-policy 才关闭）。
//   - k3s 默认自带 traefik 与 klipper servicelb（争 80/443 hostPort，
//     与受管 Proxy 冲突）——集群起动必须 --disable=traefik --disable=servicelb。
package k3s

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Provider 是 k3s Runtime Provider。
type Provider struct {
	cli kubernetes.Interface

	// restCfg 是工作客户端的连接面（SA token 身份；ExecWorkload 的 SPDY
	// 执行器构造消费——ADR-0053 决策 1/4）。
	restCfg *rest.Config

	// apiserver 地址（kubeconfig 解析产物；Enrollment 的 agent 命令锚）。
	apiServer string

	// Ensure no-op 断路器账本（swarm lastIssued 同款语义：对象名 → 最近
	// 一次确认服务端持有的期望 canonical JSON。受管域每拍重放 Ensure，
	// 服务端 defaulted 字段漂移会让深比对恒不等，无此闸会每拍重发自激）。
	ledgerMu   sync.Mutex
	lastIssued map[string]string

	// issuedGen 是最近下发 Generation 的账本（Watch 的 Drift 对照信号面：
	// 载体 Generation 标记与最近下发值不一致即 drift；单进程内存态，重启
	// 即冷——冷启动首轮全量对账不发 drift 信号，与深比对冷启动行为一致）。
	issuedGenMu sync.Mutex
	issuedGen   map[string]uint64

	// nodeIDs 是 k8s 节点名 → 平台节点 ID 的观测缓存（锚定产物；Watch 流
	// 的 Node 字段还原锚——purge 由锚定扫描周期刷新）。
	nodeIDMu sync.Mutex
	nodeIDs  map[string]string

	// kubeconfigPath 是连接面元数据（Inspect/诊断输出用）。
	kubeconfigPath string

	// execFn 是 SPDY 执行器的单测接缝（nil = 生产 remotecommand 形态；
	// fake clientset 无 exec 子资源服务——接缝承载 argv/tty/退出码管道
	// 的确定性测试，真 SPDY 链路在 e2e）。
	execFn func(ctx context.Context, req capability.ExecWorkloadRequest, ns, name string) (int, error)

	// nodeTokenPath 是 node token 文件位置覆写（空 = k3s 发行缺省
	// nodeTokenPath 常量；单测注入临时文件）。
	nodeTokenPath string
}

// 编译期契约断言：核心面 + 六个子面（Exec/Hygiene 随 ADR-0053 补齐）。
// NetworkMaintenance 是永久语义性缺席（ADR-0053 决策 2：前置病灶在 k8s
// 不存在——重建动词诚实失败是正确行为，非缺口）——故意不实现接口。
var (
	_ capability.Runtime          = (*Provider)(nil)
	_ capability.RuntimeLogs      = (*Provider)(nil)
	_ capability.RuntimeAdmin     = (*Provider)(nil)
	_ capability.RuntimeInspector = (*Provider)(nil)
	_ capability.RuntimeUtility   = (*Provider)(nil)
	_ capability.RuntimeExec      = (*Provider)(nil)
	_ capability.RuntimeHygiene   = (*Provider)(nil)
)

// New 构造 Provider：kubeconfig 为文件路径（空 = 缺省 /etc/rancher/k3s/k3s.yaml，
// k3s 发行缺省；e2e 形态 fleetlyd 与 k3s 同容器即达）。kubeconfig 是自举
// 身份（ADR-0053 决策 4）：构造期经它收敛专用 SA/ClusterRole/token（带界
// 重试——apiserver 起动竞态容忍），工作客户端整体换为 SA token，自举
// 客户端即弃（进程内不再持全权凭证）。
func New(ctx context.Context, kubeconfig string) (*Provider, error) {
	if kubeconfig == "" {
		kubeconfig = defaultKubeconfig
	}
	bootCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("k3s provider: kubeconfig %s: %w", kubeconfig, err)
	}
	boot, err := kubernetes.NewForConfig(bootCfg)
	if err != nil {
		return nil, fmt.Errorf("k3s provider: bootstrap client: %w", err)
	}
	var token string
	for attempt := 0; ; attempt++ {
		token, err = ensureRBAC(ctx, boot, rbacTokenWait)
		if err == nil {
			break
		}
		if attempt >= 14 { // ~30s 带界（apiserver 起动竞态窗；超窗 fail-fast——config 面语义）
			return nil, fmt.Errorf("k3s provider: rbac bootstrap: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("k3s provider: rbac bootstrap: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	work := rest.CopyConfig(bootCfg)
	work.BearerToken = token
	work.BearerTokenFile = ""
	// 自举凭证面（客户端证书/外部凭证）全部让位 SA token。
	work.TLSClientConfig.CertFile = ""
	work.TLSClientConfig.KeyFile = ""
	work.TLSClientConfig.CertData = nil
	work.TLSClientConfig.KeyData = nil
	work.ExecProvider = nil
	cli, err := kubernetes.NewForConfig(work)
	if err != nil {
		return nil, fmt.Errorf("k3s provider: client: %w", err)
	}
	return &Provider{
		cli:            cli,
		restCfg:        work,
		apiServer:      bootCfg.Host,
		kubeconfigPath: kubeconfig,
	}, nil
}

// defaultKubeconfig 与 config.DefaultK3sKubeconfig 同值（config 是唯一契约
// 源；工厂读 env 通道时缺省在此内联——bind.go 的 env 映射缺省不覆盖）。
const defaultKubeconfig = "/etc/rancher/k3s/k3s.yaml"

// Close 释放底层连接（client-go rest 无显式 Close 面；保留装配 cleanup 契约）。
func (p *Provider) Close() error { return nil }

// Describe 实现 Provider 契约三件套之一（能力发现端点的诚实边界声明，
// 架构 §10——与 swarm Notes 的弱隔离声明对照）。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "k3s",
		Capability: capability.KindRuntime,
		Version:    "1",
		Notes: []string{
			"network isolation enforced by NetworkPolicy; egress:none is strong isolation (per-carrier deny with in-namespace and DNS allowlist)",
			"task network group isolation is relaxed: single per-project namespace is fully connected; cross-project peers are not isolated yet (pilot)",
			"network rebuild verb is semantically absent: namespaces are always present with no carrier-network object to repair (swarm attachable flag-day has no k8s counterpart)",
			"full process DNS names ({process}.{app}) fold dots to dashes for service carrier names (k8s services are single DNS labels); bare process names are unchanged",
			"processes without declared ports resolve via headless services (pod IPs directly, no virtual IP round-robin for multi-replica)",
			"exec sessions run through the apiserver natively (per-node relay registrations are manager-side; worker nodes carry no platform agent)",
			"orphan secret sweep removes unreferenced managed secrets; orphan volume sweep is a no-op (k8s has no anonymous-volume legacy; PVC lifecycle is explicit data disposal)",
			"platform identity is the fleetly-manager ServiceAccount bound to a single narrowly-scoped ClusterRole (bootstrap identity is discarded after startup)",
			"workload identity is carried by fleetly.* labels; platform node IDs never reuse",
		},
	}
}

// Health 实现 Provider 契约三件套之一（apiserver /readyz 探测）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	body, err := p.cli.Discovery().RESTClient().Get().AbsPath("/readyz").Do(ctx).Raw()
	if err != nil {
		return capability.HealthReport{Healthy: false, Details: "k3s apiserver unreachable: " + err.Error()}
	}
	if string(body) != "ok" {
		return capability.HealthReport{Healthy: false, Details: "k3s apiserver not ready: " + string(body)}
	}
	return capability.HealthReport{Healthy: true, Details: "k3s apiserver ready"}
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。kubeconfig 经标准
// FLEETLY_RUNTIME_K3S_KUBECONFIG 环境变量覆盖（lynx ConfigSource 的 env
// 绑定与 config runtime.k3s.kubeconfig 同键，config 值优先生效）。
func init() {
	capability.RegisterFactory(capability.KindRuntime, "k3s", func(ctx context.Context) (capability.Provider, error) {
		return New(ctx, os.Getenv("FLEETLY_RUNTIME_K3S_KUBECONFIG"))
	})
}
