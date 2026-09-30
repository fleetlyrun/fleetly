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
	KindEdge        Kind = "edge"        // 流量接入：Route 发布与证书
	KindLogging     Kind = "logging"     // 日志采集与查询
	KindMetrics     Kind = "metrics"     // 指标采集与查询
	KindObjectStore Kind = "objectstore" // S3 兼容对象存储（Backup 与产物）
)

// Provider 是一切 Provider 的契约三件套（架构 §3）：能力接口（各端口单独
// 定义）+ Describe（自描述）+ Health（降级矩阵驱动）。
type Provider interface {
	// Describe 返回 Provider 自描述；同期同 Capability 仅允许一个在册。
	Describe() ProviderDescriptor
	// Health 报告当前可用性（架构 §8 降级矩阵的驱动信号）。
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
	// Healthy 为总体可用性；false 时 Details 说明原因。
	Healthy bool
	// Details 是降级矩阵口径的人读说明（用户可见文本英文）。
	Details string
}

// Factory 构造 Provider 实例：装配期（internal/assembly）按配置选定的
// Provider 名调用一次。工厂在 Provider 包 init() 里自注册。
type Factory func(ctx context.Context) (Provider, error)

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

// Build 按配置选定的 Provider 名构造实例（装配期调用）。未知名报错并列出
// 在册候选；Kind 缺省在册者（注册表里该 Kind 唯一）可空名直取。
func Build(ctx context.Context, k Kind, name string) (Provider, error) {
	byName := factories[k]
	if len(byName) == 0 {
		return nil, fmt.Errorf("capability: no provider registered for %s", k)
	}
	if name == "" {
		if len(byName) == 1 {
			for _, f := range byName {
				return f(ctx)
			}
		}
		return nil, fmt.Errorf("capability: multiple providers registered for %s, configuration must select one", k)
	}
	f, ok := byName[name]
	if !ok {
		return nil, fmt.Errorf("capability: unknown provider %q for %s (registered: %v)", name, k, registeredNames(k))
	}
	return f(ctx)
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
