package engine

import (
	"fmt"
	"time"
)

// Options 是引擎参数（装配注入；测试覆盖默认值）。按域切片（P3 收官批 8）：
// 装配注入面按域分组，fill() 缺省归一随域走。
type Options struct {
	// Deploy 是部署受理与释放窗参数（排队容量 + L1 就绪/L3 观察窗）。
	Deploy DeployOptions
	// Task 是 Task 域租约/补足/重叠策略参数。
	Task TaskOptions
	// Build 是构建循环参数（并发上限 + 单次硬超时）。
	Build BuildOptions
	// Backup 是备份执行面参数（硬超时 + restic 缝 + 平台备份配置）。
	Backup BackupOptions
	// Ring 是环节拍与带界超时族（兜底节拍 + 受管步/漂移扫描/强制重放/
	// 网络重建带界窗）。
	Ring RingOptions
	// Relay 是回环中继面参数（ADR-0049）。
	Relay RelayOptions
	// Data 是平台数据与公网发布面参数。
	Data DataOptions
}

// DeployOptions 是部署受理与释放窗参数（admission 排队 + 状态机等待窗）。
type DeployOptions struct {
	// QueueCapacity 是每 App 排队上限（默认 8；满即 ErrQueueFull）。
	QueueCapacity int
	// ObserveWindow 是 L3 观察窗（默认 60s，F0.12 验收口径）。
	ObserveWindow time.Duration
	// ReleaseTimeout 是 L1 就绪等待上限（默认 120s；超时即失败回滚）。
	ReleaseTimeout time.Duration
	// PreDeployWaitGrace 是 preDeployJobs 等待窗在作业 ttl 之上的余量
	//（默认 2m：mint→Run 创建滞后 + 终态观测滞后；镜像拉取计入 Run 自身
	// TTL——deadline 从 Run 创建起算，ADR-0030 决策 4）。
	PreDeployWaitGrace time.Duration
}

// TaskOptions 是 Task 域租约/补足/重叠策略参数。
type TaskOptions struct {
	// TaskLeaseInterval 是 Owner Lease 续期步长（默认 30s：deadline =
	// 续期时刻 + 步长；宽限叠加在其后，ADR-0012/F1.6）。
	TaskLeaseInterval time.Duration
	// TaskLeaseGrace 是租约过期宽限（默认 90s：超宽限未续期即排空回收）。
	TaskLeaseGrace time.Duration
	// TaskStopGrace 是 Run 停止宽限（默认 30s；SIGTERM 后强制收口前的
	// 等待窗，映射 Workload.StopGrace）。
	TaskStopGrace time.Duration
	// TaskOwnerRevokedRunToTTL 是属主吊销排空模式（ADR-0017 默认宽限排空
	// = false；true = 跑完 TTL：只停补足不停止存量 Run）。
	TaskOwnerRevokedRunToTTL bool
	// ScheduleOverlap 是 Schedule 重叠策略（ADR-0018 A.3 修订 / ADR-0017
	// 附录 A.4）：skip（默认——上一拍 Run 未终态时跳过本拍）或 fire（照常
	// 拍，允许并行拍）。值域由 ParseScheduleOverlap 把守（装配解析配置后
	// 注入；空值回退 skip）。
	ScheduleOverlap string
	// TaskReplenishBurst 是单拍补足创建上限（默认 8；防一拍海量创建打爆
	// swarm API——决策 7 压测锚的稳态面）。
	TaskReplenishBurst int
}

// BuildOptions 是构建循环参数（并发上限 + 单次硬超时）。
type BuildOptions struct {
	// BuildConcurrency 是并发构建上限（默认 2，F0.9）。
	BuildConcurrency int
	// BuildTimeout 是单次构建硬超时（默认 15m；超时=expired 看门狗）。
	BuildTimeout time.Duration
}

// BackupOptions 是备份执行面参数（硬超时 + restic 缝 + 平台备份配置）。
type BackupOptions struct {
	// BackupTimeout 是单次备份/恢复执行硬上限（默认 15m；GB 级 Backup 的
	// 流式执行面——单写者环卡死即时间看门狗失明，ManagedStepTimeout 同
	// 源教训）。
	BackupTimeout time.Duration
	// PlatformBackup 是控制面 restic 快照配置（nil = Platform Backup 停用
	//——装配缺省注入，nil 只属测试形态；ADR-0039）。
	PlatformBackup *PlatformBackupConfig
	// ResticPath 是 restic 二进制路径缝（测试注入假可执行；空 = 生产
	// exec.LookPath 探测——缺席即 Platform Backup 停用的探测面）。
	ResticPath string
}

// RingOptions 是环节拍与带界超时族（收敛循环兜底节拍 + 受管步/漂移扫描/
// 强制重放/网络重建的带界窗；NetworkRebuildTimeout 是 maintenance 专窗，
// 随环带界超时族归 Ring 域切片）。
type RingOptions struct {
	// Tick 是收敛循环兜底节拍（默认 1s）。
	Tick time.Duration
	// ManagedStepTimeout 是受管收敛步硬上限（默认 30s）。staging 实证
	//（2026-09-30）：docker daemon 重启/swarm 重建窗口内的 API hang 会把
	// 无界的单写者循环永久卡死（静默直至进程重启）——受管步一律带界。
	ManagedStepTimeout time.Duration
	// DriftScanInterval 是漂移扫描节拍（默认 30s；ADR-0022 spec 对照 +
	// 稳态看门狗共用此环）。
	DriftScanInterval time.Duration
	// ReconcileReplayInterval 是收敛域签名短路的统一强制重放节拍（默认
	// 60s；Task 域先例的全域推广，2026-10-03 起 Task 域同用本节拍——
	// 双旋钮退役）：受管/Database/Task Ensure、releasing 等待期物化、
	// Route 发布在签名未变时跳过重活，但自愈窗口不得无限期关闭——环外
	// 变更（人工改载体、载体漂移、Secret 重封装等签名外输入）由本节拍的
	// 强制重放兜底收敛，滞后有界。
	ReconcileReplayInterval time.Duration
	// NetworkRebuildTimeout 是网络重建全序（detach 排水 + rm 重试 + 复建
	// + re-attach）的硬上限（默认 5m；ADR-0046）。detach 触发的任务替换
	// 以 stop-first + StopGrace 量级排水（数据库 60s 宽限），分钟级是
	// 重建窗的实测量级；超时中断在可重试中间态。（maintenance 带界窗——
	// lockMaintenance 串行化域的专窗，随环带界超时族归 Ring 域切片。）
	NetworkRebuildTimeout time.Duration
}

// RelayOptions 是回环中继面参数（ADR-0049）。
type RelayOptions struct {
	// RelayLoopbackURL 是控制面回环中继的 gateway 基址（空 = 回环
	// 中继停用；ADR-0049：manager 节点也是 exec 目标，进程内中继经此
	// URL 连自身 gateway——单节点部署零 enroll 即具备 exec 面）。
	RelayLoopbackURL string
	// RelayVersion 是回环中继握手上报的版本（buildinfo 注入；空 =
	// dev 形态）。
	RelayVersion string
}

// DataOptions 是平台数据与公网发布面参数。
type DataOptions struct {
	// DataRoot 是平台数据根（构建上下文与 git 检出落盘）。
	DataRoot string
	// Browse 是数据浏览器面配置（F3.6，ADR-0051；零值 = 面停用——
	// BrowseDatabase 受理拒 E_BROWSE_DISABLED，browseLoop 零成本拍）。
	Browse BrowseConfig
	// Console 是面板公网发布面配置（ADR-0061；零值 = 不发布——console
	// 公网路由零条，现状）。
	Console ConsoleConfig
	// Api 是平台 API（gRPC 控制面）公网发布面配置（ADR-0079；零值 =
	// 不发布——API 公网路由零条，现状）。
	Api ApiConfig
}

func (o *Options) fill() {
	if o.Deploy.QueueCapacity <= 0 {
		o.Deploy.QueueCapacity = 8
	}
	if o.Deploy.ObserveWindow <= 0 {
		o.Deploy.ObserveWindow = 60 * time.Second
	}
	if o.Deploy.ReleaseTimeout <= 0 {
		o.Deploy.ReleaseTimeout = 120 * time.Second
	}
	if o.Deploy.PreDeployWaitGrace <= 0 {
		o.Deploy.PreDeployWaitGrace = 2 * time.Minute
	}
	if o.Ring.Tick <= 0 {
		o.Ring.Tick = time.Second
	}
	if o.Build.BuildConcurrency <= 0 {
		o.Build.BuildConcurrency = 2
	}
	if o.Build.BuildTimeout <= 0 {
		o.Build.BuildTimeout = 15 * time.Minute
	}
	if o.Backup.BackupTimeout <= 0 {
		o.Backup.BackupTimeout = 15 * time.Minute
	}
	if o.Ring.NetworkRebuildTimeout <= 0 {
		o.Ring.NetworkRebuildTimeout = 5 * time.Minute
	}
	if o.Ring.ManagedStepTimeout <= 0 {
		o.Ring.ManagedStepTimeout = 30 * time.Second
	}
	if o.Ring.DriftScanInterval <= 0 {
		o.Ring.DriftScanInterval = 30 * time.Second
	}
	if o.Task.TaskLeaseInterval <= 0 {
		o.Task.TaskLeaseInterval = 30 * time.Second
	}
	if o.Task.TaskLeaseGrace <= 0 {
		o.Task.TaskLeaseGrace = 90 * time.Second
	}
	if o.Task.TaskStopGrace <= 0 {
		o.Task.TaskStopGrace = 30 * time.Second
	}
	if o.Task.TaskReplenishBurst <= 0 {
		o.Task.TaskReplenishBurst = 8
	}
	if o.Ring.ReconcileReplayInterval <= 0 {
		o.Ring.ReconcileReplayInterval = 60 * time.Second
	}
	if o.Task.ScheduleOverlap == "" {
		o.Task.ScheduleOverlap = ScheduleOverlapSkip
	}
}

// Schedule 重叠策略值（ADR-0017 附录 A.4 修订 ADR-0018 A.3）。
const (
	// ScheduleOverlapSkip：上一拍铸出的 Task 仍有未终态 Run 时跳过本拍
	//（schedule.skipped reason=overlap）——最少惊异默认。
	ScheduleOverlapSkip = "skip"
	// ScheduleOverlapFire：重叠时照常拍，允许并行拍（torchwood 类并行
	// 派发的实证形态）。
	ScheduleOverlapFire = "fire"
)

// ParseScheduleOverlap 归一配置值：空值/合法值直通，非法值报错
// （fail-fast——配置错误不得静默回退成 skip）。
func ParseScheduleOverlap(s string) (string, error) {
	switch s {
	case "", ScheduleOverlapSkip:
		return ScheduleOverlapSkip, nil
	case ScheduleOverlapFire:
		return ScheduleOverlapFire, nil
	default:
		return "", fmt.Errorf("engine: schedule_overlap_policy must be %q or %q, got %q",
			ScheduleOverlapSkip, ScheduleOverlapFire, s)
	}
}
