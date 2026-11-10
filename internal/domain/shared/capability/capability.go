// Package capability 定义七个可插拔子系统的端口（ADR-0003：编译期 Go
// interface + 注册表；先不做进程外插件协议）。Provider 自注册（init），
// 配置选定每 Capability 同期唯一在册 Provider。
//
// 依赖方向（架构 §2）：capability 是叶子接口层，消费 spec 与 model 类型；
// providers 实现 capability；engine 消费 capability。除 cmd 外无人 import
// providers（守卫见 internal/guards）。
package capability

import (
	"context"
	"fmt"
	"sort"
)

// Kind 是 Capability 种类（七类端口，架构 §3）。
type Kind string

const (
	KindRuntime     Kind = "runtime"     // 编排器：Workload 期望状态下发与集群观测
	KindBuilder     Kind = "builder"     // Source → 镜像
	KindRegistry    Kind = "registry"    // OCI 镜像仓库（拉取来源/推送目标）
	KindProxy       Kind = "proxy"       // 流量接入：Route 发布与证书
	KindLogging     Kind = "logging"     // 日志采集与查询
	KindMetrics     Kind = "metrics"     // 指标采集与查询
	KindObjectStore Kind = "objectstore" // S3 兼容对象存储（Backup 与产物）
)

// Provider 是一切 Provider 的契约三件套（架构 §3）：能力接口（各端口单独
// 定义）+ Describe（自描述）+ Health（Provider 自身面可用性）。
type Provider interface {
	// Describe 返回 Provider 自描述；同期同 Capability 仅允许一个在册。
	Describe() ProviderDescriptor
	// Health 报告 Provider 自身面可用性（配置就绪/端点可达/上游版本；架构
	// §8 降级矩阵的 Provider 面信号）。受管自宿 Provider 的载体存活面由引擎
	// 受管健康行（managed proxy 等）从 Runtime 观测单源透出——本方法不重复
	// 声明载体健康（避免恒真与双源）。
	Health(ctx context.Context) HealthReport
}

// ProviderDescriptor 是 Provider 的静态自描述。
type ProviderDescriptor struct {
	// Name 是 Provider 名（"swarm"、"traefik"、"victorialogs"…）。
	Name string
	// Capability 是所属端口种类。
	Capability Kind
	// Version 是 Provider 实现版本。
	Version string
	// Managed 为 true 时该 Provider 以普通 Workload 形态受管自宿
	//（ADR-0004：唯一通用 ManagedProvider reconciler 部署）。
	Managed bool
	// Notes 是面向能力发现端点的诚实边界声明（如 swarm 的 egress:none
	// 弱隔离、软网络隔离说明；架构 §10）。
	Notes []string
}

// HealthReport 是 Health() 的返回。
type HealthReport struct {
	// Healthy 是 Provider 自身面的可用性（配置就绪/端点可达等；受管载体
	// 存活面不在此——见 Provider.Health 注释）。
	Healthy bool
	// Details 是降级矩阵口径的人读说明（用户可见文本英文）。
	Details string
}

// FactoryParams 是工厂的显式参数集（提案 2026-11-05 C1 / ADR-0036 载体
// 收拢节）：按端口分七子结构——窄面自洽（traefik 看不见 S3 凭证）。装配
// 期一次性构造；ctx 留函数首参不载配置。
type FactoryParams struct {
	Registry    RegistryParams    // zot
	Logging     LoggingParams     // victorialogs
	Metrics     MetricsParams     // victoriametrics
	ObjectStore ObjectStoreParams // local / s3
	Proxy       ProxyParams       // traefik
	Runtime     RuntimeParams     // swarm / k3s / builders
	Platform    PlatformParams    // 数据根（平台私有状态落盘目录）
}

// RegistryParams 是 Registry 端口的装配参数（zot）。
type RegistryParams struct {
	// Addr 是受管仓库引用地址（config registry.addr 单源；空 = Registry 面停用）。
	Addr string
}

// LoggingParams 是 Logging 端口的装配参数（victorialogs）。
type LoggingParams struct {
	// Addr 是受管日志存储端点（空 = Logging 面停用）。
	Addr string
	// RetentionDays 是日志保留窗天数（0 = 实现方自取缺省）。
	RetentionDays int64
}

// MetricsParams 是 Metrics 端口的装配参数（victoriametrics）。
type MetricsParams struct {
	// Addr 是受管指标存储端点（空 = Metrics 面停用）。
	Addr string
	// RetentionDays 是指标保留窗天数（0 = 实现方自取缺省）。
	RetentionDays int64
}

// ObjectStoreParams 是 ObjectStore 端口的装配参数（local / s3）。
type ObjectStoreParams struct {
	// S3 是外置 S3 目标五元组（nil = local 形态；s3 工厂据此精确失败）。
	S3 *ObjectStoreS3Config
}

// ProxyParams 是 Proxy 端口的装配参数（traefik）。
type ProxyParams struct {
	// AuthToken 是拉取端点共享令牌（空 = 无认证现状）。
	AuthToken string
	// ConfigEndpoint 是 traefik HTTP provider 拉取端点完整 URL（空 = 装配面停用）。
	ConfigEndpoint string
	// AcmeEmail 是 ACME 账户邮箱（空 = 不带 contact 注册）。
	AcmeEmail string
	// AcmeCAServer 是 ACME CA 目录端点覆盖（空 = 实现方 staging 缺省）。
	AcmeCAServer string
}

// RuntimeParams 是 Runtime 与 Builder 端口的装配参数（swarm / k3s / builders）。
type RuntimeParams struct {
	// DockerHost 是 daemon 端点（docker 生态约定键，无 config 键；空 = 客户端缺省）。
	DockerHost string
	// K3sKubeconfig 是 k3s kubeconfig 路径（空 = k3s 发行缺省路径）。
	K3sKubeconfig string
}

// PlatformParams 是平台私有状态的落盘参数（各受管 Provider 共用）。
type PlatformParams struct {
	// DataRoot 是平台数据根（config data.root 单源；空 = 工厂精确失败）。
	DataRoot string
}

// Factory 构造 Provider 实例：装配期（internal/assembly）按配置选定的
// Provider 名调用一次，配置经 FactoryParams 显式传入（零 env 读取、零
// 缺省回退）。工厂在 Provider 包 init() 里自注册。
type Factory func(ctx context.Context, p FactoryParams) (Provider, error)

// factories 是编译期工厂注册表：Kind → Provider 名 → 工厂。
var factories = map[Kind]map[string]Factory{}

// RegisterFactory 自注册 Provider 工厂（Provider 包 init 期调用）；同
// Kind+名 重复注册 panic——装配错误在启动期暴露。
func RegisterFactory(k Kind, name string, f Factory) {
	if f == nil || name == "" {
		panic(fmt.Sprintf("capability: factory registration incomplete: kind=%s name=%q", k, name))
	}
	if _, dup := factories[k][name]; dup {
		panic(fmt.Sprintf("capability: duplicate factory for %s/%s", k, name))
	}
	if factories[k] == nil {
		factories[k] = map[string]Factory{}
	}
	factories[k][name] = f
}

// Build 按配置选定的 Provider 名构造实例并断言为请求的端口类型（编译期
// 钉型，提案 2026-11-05 C2——断言入库内，装配侧样板删除）。未知名报错
// 并列出在册候选；Kind 缺省在册者（注册表里该 Kind 唯一）可空名直取。
func Build[T Provider](ctx context.Context, k Kind, name string, p FactoryParams) (T, error) {
	var zero T
	byName := factories[k]
	if len(byName) == 0 {
		return zero, fmt.Errorf("capability: no provider registered for %s", k)
	}
	if name == "" {
		if len(byName) == 1 {
			for _, f := range byName {
				return buildAs[T](ctx, f, k, p)
			}
		}
		// 空名多注册按 kind 参数化（提案 2026-11-05 C4）：Runtime 有真旋钮
		// （runtime.provider），报错指名它；其余 kind 无选择旋钮——误指不
		// 存在的键是对无旋钮 kind 撒谎。
		if k == KindRuntime {
			return zero, fmt.Errorf("capability: multiple providers registered for runtime; set runtime.provider in the configuration to select one (registered: %v)", registeredNames(k))
		}
		return zero, fmt.Errorf("capability: multiple providers registered for %s — this kind has no name selector, only one provider may be compiled in (registered: %v)", k, registeredNames(k))
	}
	f, ok := byName[name]
	if !ok {
		return zero, fmt.Errorf("capability: unknown provider %q for %s (registered: %v)", name, k, registeredNames(k))
	}
	return buildAs[T](ctx, f, k, p)
}

// buildAs 调一次工厂并把产物断言为请求的端口类型：断言失败报 Provider
// 自描述名（不用 reflect），报错列在册候选供装配日志定位。
func buildAs[T Provider](ctx context.Context, f Factory, k Kind, p FactoryParams) (T, error) {
	var zero T
	provider, err := f(ctx, p)
	if err != nil {
		return zero, err
	}
	t, ok := provider.(T)
	if !ok {
		return zero, fmt.Errorf("capability: provider %s does not implement the port requested for %s (registered: %v)", provider.Describe().Name, k, registeredNames(k))
	}
	return t, nil
}

// registeredNames 返回某 Kind 在册 Provider 名（排序稳定，供报错与能力
// 发现端点）。
func registeredNames(k Kind) []string {
	names := make([]string, 0, len(factories[k]))
	for n := range factories[k] {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RegisteredFactories 返回全部在册工厂名（doctor 与能力发现端点消费）。
func RegisteredFactories() map[Kind][]string {
	out := make(map[Kind][]string, len(factories))
	for k := range factories {
		out[k] = registeredNames(k)
	}
	return out
}

// ResetFactories 清空工厂注册表（仅测试用；生产代码不得调用）。
func ResetFactories() {
	factories = map[Kind]map[string]Factory{}
}
