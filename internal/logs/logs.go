// Package logs 是应用日志管线（T2.20；architecture §4.2 第 5 项「实时流
// + 历史落盘检索」）：
//
//	采集（底座 docker service logs 轮询）→ 脱敏（该 app env 已知值）
//	→ 内存 ring buffer（per app-service 有界，Follow 回放与实时扇出）
//	→ 落盘（目录可配，按天分文件，7 天轮转清理，History 检索源）。
//
// Secret 值纪律（state-model §2.9）：脱敏只针对**该 app 的 env 已知值**
// （负面测试保证 env 值不出现在任何出口）；短值（< 8 字节）不参与脱敏
// （避免把日志打成筛子），属已知取舍。
package logs

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// Entry 是一条应用日志（采集出口与 API 投影的共同形态）。
type Entry struct {
	App     string    `json:"app"`
	Service string    `json:"service"`
	At      time.Time `json:"at"`
	Stderr  bool      `json:"stderr,omitempty"`
	Line    string    `json:"line"`
	// Source ∈ container | build（History 按 source 区分检索）。
	Source string `json:"source"`
}

// 日志来源词表。
const (
	// SourceContainer 是容器（docker service logs 采集）来源。
	SourceContainer = "container"
	// SourceBuild 是构建日志（builds 表 log_path 产物）来源。
	SourceBuild = "build"
)

// Port 是日志采集的底座端口（实现 = substrate.Client；cmd 装配注入，
// 测试用 fake）。
type Port interface {
	// StreamServiceLogs 打开 Swarm 服务日志流（follow=false 轮询拉取语义）。
	StreamServiceLogs(ctx context.Context, service string, since time.Time, follow bool) (<-chan substrate.LogLine, error)
	// ManagedServiceProcesses 返回应用的受管 compose 服务名集。
	ManagedServiceProcesses(ctx context.Context, app string) ([]string, error)
	// CronJobServiceStates 返回该 app 当前存活的一次性 cron job 服务实况投影
	//（fleetly-cron- 前缀受管服务，E5 Cron「日志进现有采集」）。实现只负责
	// 忠实列出；归属映射解析（job 名 + 受管 label → compose 服务）由 logs 层
	// cronJobRefOf 纯函数承载（可 hermetic 测试）。
	CronJobServiceStates(ctx context.Context, app string) ([]engine.ServiceState, error)
}

// CronJobRef 是一次性 cron job 服务的日志采集归属：JobService 是 Swarm 服务
// 名（流打开目标），Service 是归属 compose 服务名（ring/落盘/History/Follow
// 的 (app, service) 键——job 行与长驻服务同面合流）。
type CronJobRef struct {
	JobService string
	Service    string
}

// Config 是日志管线配置（config 键 logs.*）。零值经 Normalize 回落缺省。
type Config struct {
	// Dir 是落盘根目录（logs.dir；缺省 ./fleetly-logs）。
	Dir string `mapstructure:"dir"`
	// RetentionDays 是落盘保留天数（logs.retention_days；缺省 7——架构
	// §2.3 数据保留：应用日志 7 天轮转）。
	RetentionDays int `mapstructure:"retention_days"`
	// ScanIntervalMillis 是采集轮询周期毫秒数（logs.scan_interval_millis；
	// 缺省 2000）。
	ScanIntervalMillis int `mapstructure:"scan_interval_millis"`
	// RingSize 是 per app-service 内存环形缓冲深度（logs.ring_size；缺省
	// 1000——Follow 接入时的回放上限）。
	RingSize int `mapstructure:"ring_size"`
}

// Normalize 回落缺省值（单一事实源在本包；cmd 配置层不做缺省）。
func (c Config) Normalize() Config {
	if c.Dir == "" {
		c.Dir = "fleetly-logs"
	}
	if c.RetentionDays <= 0 {
		c.RetentionDays = 7
	}
	if c.ScanIntervalMillis <= 0 {
		c.ScanIntervalMillis = 2000
	}
	if c.RingSize <= 0 {
		c.RingSize = 1000
	}
	return c
}

// ScanInterval 返回轮询周期。
func (c Config) ScanInterval() time.Duration {
	return time.Duration(c.ScanIntervalMillis) * time.Millisecond
}

// SecretValuesSource 是 B3 脱敏值集扩面的补充供给端口：返回该 app 在
// state 密文列之外仍存明文落点的 secret 值（实现 = gitserver.GitTriggers 的
// post-receive 钩子 token——明文只写进钩子文件；logs 不感知实现类型，
// 装配点注入）。读取失败应返回 nil（观测面降级不阻断采集）。
type SecretValuesSource interface {
	// SecretValues 返回该 app 的补充脱敏明文值集（可为空）。
	SecretValues(ctx context.Context, appID string) []string
}

// Manager 是日志管线管理器：采集循环（Start 阻塞到 ctx 取消）、Follow
// 订阅、History 落盘检索、保留期清理。
type Manager struct {
	st    *state.Store
	port  Port
	box   *secrets.Box
	cfg   Config
	clock func() time.Time

	hub     *hub
	dsk     *diskStore
	red     *redactorRegistry
	streams map[string]*stream // 采集游标表（key = streamKey；mu 保护）
	// appMiss 是 app 不在 active 集的首见时刻（M7-6 延迟淘汰计时；mu
	// 保护）——连续 miss 超过 streamEvictAfter 即回收该 app 的游标与 ring。
	appMiss map[string]time.Time
	// pollWatchdogOverride 是单轮看门狗期限覆盖（MG-1 纵深防御的测试注入
	// 位；零值 = 按 cfg 计算 max(3×扫描周期, 30s)）。
	pollWatchdogOverride time.Duration
	mu                   sync.Mutex
	log                  *slog.Logger

	// ing 是日志库入湖批量器（W5-S1，E6 设计 §2.3；nil = 未装配——纯
	// jsonl 形态，采集/落盘路径零差异）。传输面经 WithIngestBackend 注入
	//（实现 = victorialogs.Backend；方向纪律：logs 定义 IngestBackend
	// 端口，不反向感知 VL 组件包）。
	ing *Ingester
	// vlIngest 是「入湖启用」门（backend=victorialogs 且 ing 已装配）。
	// 每扫描拍由 LoadLogsSettings 现读刷新（设置保存即生效，读侧不缓存
	// 长驻——s3settings 同口径）；false = 纯 jsonl 形态（落盘照旧）。
	vlIngest atomic.Bool
}

// NewManager 构造日志管线管理器（不启动采集；Run 承载循环）。
func NewManager(cfg Config, st *state.Store, port Port, box *secrets.Box, log *slog.Logger) *Manager {
	norm := cfg.Normalize()
	if log == nil {
		log = discardLogger()
	}
	return &Manager{
		st:      st,
		port:    port,
		box:     box,
		cfg:     norm,
		clock:   time.Now,
		hub:     newHub(norm.RingSize),
		dsk:     newDiskStore(norm.Dir),
		red:     newRedactorRegistry(st, box, log),
		streams: make(map[string]*stream),
		appMiss: make(map[string]time.Time),
		log:     log,
	}
}

// WithClock 覆盖时钟（测试注入）。
func (m *Manager) WithClock(f func() time.Time) *Manager { m.clock = f; return m }

// WithSecretSource 注入补充脱敏值集供给（B3；装配期调用——实现方提供
// state 密文列之外的明文 secret 落点，如 gitserver 钩子 token）。
func (m *Manager) WithSecretSource(s SecretValuesSource) *Manager { m.red.extra = s; return m }

// WithIngestBackend 注入日志库入湖传输面（W5-S1；装配期调用）。nil 合法
//（纯 jsonl 形态——单测/精简装配，行为与 v0.1 逐字一致）。注入即构造
// 批量器（flush 循环由 Run 拉起；启用门每拍由设置现读刷新）。
func (m *Manager) WithIngestBackend(b IngestBackend) *Manager {
	if b == nil {
		return m
	}
	m.ing = NewIngester(b, m.st, m.log)
	return m
}

// vlEnabled 报告当前拍是否应入湖（backend=victorialogs 且批量器已装配）。
func (m *Manager) vlEnabled() bool { return m.vlIngest.Load() }

// refreshBackendGate 现读 logs.backend 刷新启用门（每扫描拍一次；读取
// 失败保持当前门——底座暂态不该翻转采集路由，warn 记录下拍再试）。
func (m *Manager) refreshBackendGate(ctx context.Context) {
	if m.ing == nil {
		return
	}
	in, err := m.st.LoadLogsSettings(ctx)
	if err != nil {
		m.log.Warn("logs: load logs.backend failed (keeping current ingest route)", "error", err.Error())
		return
	}
	m.vlIngest.Store(in.Backend == state.LogsBackendVictorialogs)
}

// IngestDegraded 报告入湖 streak 是否降级中（system status 组件面；未
// 装配批量器或零值 Manager = 恒 false——纯 jsonl 形态无降级面）。
func (m *Manager) IngestDegraded() bool {
	if m == nil || m.ing == nil {
		return false
	}
	return m.ing.Degraded()
}

// IngestDroppedTotal 返回溢出丢弃累计行数（诚实面的常驻计数）。
func (m *Manager) IngestDroppedTotal() uint64 {
	if m == nil || m.ing == nil {
		return 0
	}
	return m.ing.DroppedTotal()
}

// IngestStreakSince 返回降级 streak 起点（零值 = 未降级）。
func (m *Manager) IngestStreakSince() time.Time {
	if m == nil || m.ing == nil {
		return time.Time{}
	}
	return m.ing.StreakSince()
}

// InvalidateRedaction 主动失效某 app 的脱敏值集缓存（H9）：env 写路径
// （api set/remove）与部署 env 提升点（engine 观察窗成功）联动调用，把
// 「新 secret 值在 TTL 窗内被明文采集并按天落盘保留 7 天」的暴露窗收敛
// 到下一次 forApp 重建。幂等；未知 appID 无害。未挂钩的写路径仍由 TTL
// 兜底（redact.go redactorTTL 注释）。
func (m *Manager) InvalidateRedaction(appID string) { m.red.invalidate(appID) }
