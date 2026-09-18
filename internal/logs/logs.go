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
	"time"

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
	mu      sync.Mutex
	log     *slog.Logger
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
		log:     log,
	}
}

// WithClock 覆盖时钟（测试注入）。
func (m *Manager) WithClock(f func() time.Time) *Manager { m.clock = f; return m }
