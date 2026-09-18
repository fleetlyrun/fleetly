package build

// Config 是构建管线配置（daemon config build.* 节的核心形态）。缺省值即
// 保守基线：并发 2（架构 §4.2 容量边界「并发构建 2」）、限额 1GiB/1.5CPU
// （Spike A E4 实证可在该预算内完成真实构建）、双缓存持久化（见下）。
//
// 缓存机制（Spike A「缓存是默认能力不是优化」；实机勘误后的正确形态）：
// buildkit local cache 的导入/导出是客户端侧行为（经 solve session 流式
// 传输，路径解析在 fleetlyd 宿主而非 buildkitd 容器内）。持久化因此分两
// 层，各自承载一半语义：
//   - CacheVolume（命名卷）挂进 buildkitd 容器的内部工作缓存目录
//     /var/lib/buildkit——层缓存随容器重建不丢（moby/buildkit 镜像对该
//     路径的匿名卷在容器删除即失，命名卷是持久化手段）；
//   - CacheDir（fleetlyd 宿主目录）是 local cache 导入/导出的数据根——
//     跨 buildkitd 容器重建、跨实例可移植的缓存层（Spike A E2b 的
//     registry-cache 形态在 v0.1 单机上的等价物）。

import (
	"time"
)

// buildkitd 资源与命名的平台缺省（Spike A 钉版与实证值）。
const (
	// DefaultBuildkitHost 是 buildkitd 连接端点缺省（docker-container://
	// connhelper 经 docker CLI exec buildctl dial-stdio）。
	DefaultBuildkitHost = "docker-container://" + DefaultDaemonContainerName
	// DefaultDaemonContainerName 是平台自管 buildkitd 容器名。
	DefaultDaemonContainerName = "fleetly-buildkit"
	// BuildkitImage 是 buildkitd 钉版镜像（与 Spike A 一致；升级 = 独立
	// 变更票，不跟随 railpack 传递依赖漂移）。
	BuildkitImage = "moby/buildkit:v0.32.2"
	// DefaultCacheVolume 是 buildkitd 内部工作缓存的持久化命名卷（挂载点
	// InternalCacheMountPath；容器重建不丢层缓存）。
	DefaultCacheVolume = "fleetly-buildkit-cache"
	// InternalCacheMountPath 是 buildkitd 容器内内部工作缓存目录（镜像
	// VOLUME 声明路径；命名卷持久化的挂载点）。
	InternalCacheMountPath = "/var/lib/buildkit"
	// DefaultCacheDir 是 local cache 导入/导出的宿主目录缺省（客户端侧
	// 数据根；相对 fleetlyd 工作目录，与 db_path/artifacts_dir 同约定）。
	DefaultCacheDir = "./build-cache"
	// DefaultConcurrency 是构建队列并发上限缺省（架构 §4.2：并发构建 2）。
	DefaultConcurrency = 2
	// MaxConcurrency 是并发上限配置天花板（防误配打穿宿主；超限取上限）。
	MaxConcurrency = 8
	// DefaultMemoryBytes / DefaultNanoCPUs 是 buildkitd 容器资源限额缺省
	// （Spike A E4 实证：1GiB/1.5CPU 下 node 冷构建可完成、峰值 59% 内存
	// 天花板）。
	DefaultMemoryBytes = int64(1) << 30
	DefaultNanoCPUs    = int64(1_500_000_000)
	// DefaultPollInterval 是队列扫描 builds queued 行的周期（CLI 入队为
	// 跨进程通道；wake 信号提供同进程加速路径）。
	DefaultPollInterval = 2 * time.Second
	// DefaultArtifactsDir 是产物归档缺省根目录（相对 daemon 工作目录）。
	DefaultArtifactsDir = "./build-artifacts"
)

// Config 是构建管线配置。零值经 Normalize 回落缺省。
type Config struct {
	// BuildkitHost 是 buildkit 端点（docker-container://<name> 或
	// tcp://…/unix://… 直连外部 buildkitd——隔离升级路径）。
	BuildkitHost string
	// ManageDaemon 报告是否由平台自管 buildkitd 容器（EnsureRunning 幂等
	// 拉起钉版容器 + 限额 + 内部缓存卷）。指向外部 buildkitd 时应置 false。
	ManageDaemon bool
	// DaemonContainerName 是自管 buildkitd 容器名。
	DaemonContainerName string
	// CacheVolume 是 buildkitd 内部工作缓存的持久化命名卷（缺省）。
	CacheVolume string
	// CacheDir 是 local cache 导入/导出的宿主目录（客户端侧数据根）。
	CacheDir string
	// ArtifactsDir 是 plan JSON / 构建日志归档根目录。
	ArtifactsDir string
	// Concurrency 是构建队列并发上限（≤0 取缺省 2，天花板 8）。
	Concurrency int
	// MemoryBytes / NanoCPUs 是 buildkitd 容器 cgroup 限额。
	MemoryBytes int64
	NanoCPUs    int64
	// PollInterval 是队列 DB 扫描周期。
	PollInterval time.Duration
}

// Normalize 回落全部缺省（config 缺省值单一事实源）。
func (c Config) Normalize() Config {
	if c.BuildkitHost == "" {
		c.BuildkitHost = DefaultBuildkitHost
	}
	if c.DaemonContainerName == "" {
		c.DaemonContainerName = DefaultDaemonContainerName
	}
	if c.CacheVolume == "" {
		c.CacheVolume = DefaultCacheVolume
	}
	if c.CacheDir == "" {
		c.CacheDir = DefaultCacheDir
	}
	if c.ArtifactsDir == "" {
		c.ArtifactsDir = DefaultArtifactsDir
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Concurrency > MaxConcurrency {
		c.Concurrency = MaxConcurrency
	}
	if c.MemoryBytes <= 0 {
		c.MemoryBytes = DefaultMemoryBytes
	}
	if c.NanoCPUs <= 0 {
		c.NanoCPUs = DefaultNanoCPUs
	}
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	return c
}
