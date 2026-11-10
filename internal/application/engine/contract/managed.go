// managed.go 是受管域应用模块的核/模块契约面（engine 去应用模块 import
// 第三批，2026-11-10）：engine 核只持 Managed 接口与构造端口袋，具体装配器
// 由组合根经 engine.Deps.ManagedFactory 注入——核不再 import 受管应用模块
// 包。端口形态全部落在 domain 面（capability/state 签名），契约叶子保持
// 零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Managed 是受管域应用模块的核消费面：engine 循环表经 Module.Loops 取
// managed 步，networkrebuild 经 ManagedProviders 判定受管网集归属，
// HealthComponents 经 Health 并入受管 Provider 载体存活行。
type Managed interface {
	Module
	// ManagedProviders 列出在册受管 Provider（注册序稳定：Proxy → Registry
	// → Logging → Metrics；窄读面只暴露声明）。
	ManagedProviders() []ManagedProvider
	// Health 返回受管 Provider 的载体存活健康行（未观测/启动窗不出行）。
	Health() []ManagedHealthRow
}

// ManagedProvider 是一个受管 Provider 的窄读面：核只消费 Managed() 的
// 命名空间判定（networkrebuild 的受管网集归属）。
type ManagedProvider interface {
	Managed() capability.Managed
}

// ManagedHealthRow 是受管 Provider 的载体存活健康行。Name 稳定
// （"managed <kind>"），Healthy=false 时 Details 列失联 Workload 的 ID 与
// 观测状态/消息；engine 把它映射为 HealthComponent 并入 GetStatus
// components（聚合态据此 DEGRADED）。
type ManagedHealthRow struct {
	Name    string
	Healthy bool
	Details string
}

// ManagedPorts 是受管装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值/闭包；形态与受管应用模块的 Deps 逐字段同形，契约叶子只
// 依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type ManagedPorts struct {
	Proxy    capability.Proxy
	Registry capability.Registry
	Logging  capability.Logging
	Runtime  capability.Runtime
	Log      *slog.Logger
	Clock    state.Clock

	LockMaintenance       func() func()
	BoundedStep           func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh           EnsureFresh
	EnsureRemember        EnsureRemember
	EnsureForget          EnsureForget
	ActiveProjectNetworks func(ctx context.Context) ([]capability.NetworkRef, error)
	ActiveProjectIDs      func(ctx context.Context) ([]string, error)
	ControlPlaneNode      func(ctx context.Context) (string, error)
	GensFor               func(nsKey string) *ManagedGen
	ReconcileNodes        func(ctx context.Context)
	PublishRoutes         func(ctx context.Context)
	MetricsProvider       func() capability.Metrics
	// RecordOwners/RecordExpected 是观测窄接口的 system 域窄化端口（核
	// 绑定闭包内铸造 systemOwner(w.Process)——域判别留核）。
	RecordOwners   func(gen uint64, ws []capability.Workload)
	RecordExpected func(process string, gen uint64)
	// ObservationOf 是观测窄接口的 managed 轴读面（workloadID → 最新观测；
	// 受管健康行的载体存活面输入）。组合根绑核 observationOf。
	ObservationOf func(workloadID string) (capability.WorkloadEvent, bool)

	// EnsureMemo 是 per-namespace Ensure 确保缓存表本体（组合根 make 后注入）。
	EnsureMemo map[string]Memo
}

// ManagedFactory 由组合根注入（engine.Deps.ManagedFactory），把端口袋装配
// 成 Managed。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时
// 受管面停用：managed 环退化为 no-op 步，networkrebuild 不枚举受管网集，
// HealthComponents 不出受管行。
type ManagedFactory func(ManagedPorts) Managed

// ManagedGen 是受管域 Generation 的实例态（C5：原包级全局会让多 Engine
// 实例（测试/多 daemon）互相污染指纹）。P5 wave-4 拆包后类型随应用模块包；
// engine 去应用模块 import 第三批（2026-11-10）随契约面下沉——gensFor 留核
// 持 *ManagedGen map（ensureMu 锁与 map 的配对裁定），应用模块经端口读写。
// 字段零值可用（gensFor 造零值计数器）。
type ManagedGen struct {
	gen atomic.Uint64
	fp  atomic.Value // string：最近一次已分配 gen 的 spec 指纹
	// adopted 标记首见指纹已沿用（SeedFromRuntime 播种后的第一拍不 +1——
	// 播种值本身来自载体现行 spec，是"已下发"事实而非新变更）。
	adopted atomic.Bool
	// seeded 标记本进程已做过载体观测播种（一次性）。P1-5 根修
	//（2026-10-05 评审，staging 记录·五 #2）：观测【失败】不再置位——
	// 冷启 gen=1 与载体持久的 fleetly.generation 标签差值就是假滚动
	//（I/O 风暴窗重启五受管域连滚、traefik 路由中断级）；失败 = 跳过
	// 本域本拍 Ensure/gen 推进，下拍重试播种。扩展接口缺席（无从观测）仍
	// 冷启 gen=1 兜底（旧行为）。
	seeded atomic.Bool
	// obsSkipLogged 是观测失败跳拍告警的去抖位：连续失败沿只告警一次，
	// 观测门恢复即清（告警状态机"沿才通知"同款取向——风暴窗不逐拍刷屏）。
	obsSkipLogged atomic.Bool
}

// Next 幂等分配：指纹同前且已有 gen → 复用；首见 → 沿用现值
// （SeedFromRuntime 播种的载体现行 gen，或未播种时 0→1）；变化 → +1
// （重启续接语义，见 SeedFromRuntime 注释）。
func (m *ManagedGen) Next(fp string) uint64 {
	prev, hasFP := m.fp.Load().(string)
	if hasFP && prev == fp && m.gen.Load() > 0 {
		return m.gen.Load()
	}
	if !hasFP && m.adopted.CompareAndSwap(false, true) {
		// 首见：指纹即载体现行形态（播种来源或进程冷启的第一拍）——沿用
		// 现值，不制造假变更。
		m.fp.Store(fp)
		if m.gen.Load() == 0 {
			return m.gen.Add(1)
		}
		return m.gen.Load()
	}
	m.fp.Store(fp)
	return m.gen.Add(1)
}

// SeedFromRuntime 用载体观测的现行 Generation 播种计数器（重启续接）。
// 缺席（首装/观测失败）不播种——Next 冷启取 1。
func (m *ManagedGen) SeedFromRuntime(gen uint64) {
	if gen > m.gen.Load() {
		m.gen.Store(gen)
	}
}

// Seeded 报告本进程是否已做过载体观测播种（一次性）。
func (m *ManagedGen) Seeded() bool { return m.seeded.Load() }

// MarkSeeded 标记载体观测播种完成（成功后置位；观测失败不置位）。
func (m *ManagedGen) MarkSeeded() { m.seeded.Store(true) }

// ResetObsSkipLogged 清观测失败跳拍告警的去抖位（观测门全过时调用）。
func (m *ManagedGen) ResetObsSkipLogged() { m.obsSkipLogged.Store(false) }

// MarkObsSkipLogged 尝试置位观测失败跳拍告警的去抖位，返回是否是本次
// 失败沿的首次置位（true = 应告警；false = 连续失败已告警过）。
func (m *ManagedGen) MarkObsSkipLogged() bool {
	return m.obsSkipLogged.CompareAndSwap(false, true)
}
