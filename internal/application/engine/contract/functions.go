// functions.go 是 Functions 应用模块的核/模块契约面（ADR-0102 任务③B，
// engine 去应用模块 import——组合根经 engine.Deps.FunctionFactory 注入装配
// 器，核零 import functions 包）。端口形态全部落在 domain 面，契约叶子
// 保持零应用模块类型（ADR-0088 决策 7 裁定二，dataservice.go 同款）。
package contract

import (
	"context"
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Functions 是 Functions 应用模块的核消费面：循环步经 Module.Loops 挂载
// （ring 第 13 环 "function"）；写面动词经核门面委托（API 层不 import 应用
// 模块包，ADR-0088 消费面纪律）。
type Functions interface {
	Module
	// TeardownFunction 是聚合收口（ADR-0102/设计 v3 §4.3）：Remove 拆载体
	// + 观测/期望/ensure 缓存清理；行 tombstone 与 function_revisions 终局
	// 全清由 API 受理事务承载（任务④）——本面只拆不落账。
	TeardownFunction(ctx context.Context, id string) error
	// ParkedFunctionAliases 是 parked 函数的 fn-<id> 别名集（P2-1b 别名
	// 交接：fn 摘别名后由唤醒组件 Addressing 接管 private 解析；全部
	// 活跃 parked 行——exposure 无关，private 与 public 的 fn 名同一声明）。
	ParkedFunctionAliases(ctx context.Context) ([]string, error)
	// ParkedFunctionPorts 是 parked 函数声明口的并集（P2-1b private 半边
	// ——swarm VIP 按端口透传，唤醒组件任务须在函数声明口同端口监听，
	// private 唤醒窗才可达；public 半边无此约束：traefik 恒打组件口）。
	ParkedFunctionPorts(ctx context.Context) ([]int32, error)
}

// FunctionsPorts 是 functions 装配器的构造端口袋（组合根在 engine
// construct.go 绑定核方法值/闭包与 repo；与 functions.Deps 逐字段同形）。
type FunctionsPorts struct {
	Runtime capability.Runtime
	Repos   transact.Stores
	Cipher  *material.Cipher
	Log     *slog.Logger
	Clock   state.Clock

	LockMaintenance     func() func()
	BoundedStep         func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh         EnsureFresh
	EnsureRemember      EnsureRemember
	EnsureForget        EnsureForget
	ProjectTeam         func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	// RecordOwners/RecordExpected/ForgetExpected/ForgetObservation 是观测
	// 窄接口的 function 轴窄化端口（functionOwner 铸造与域判别留核绑定
	// 闭包——dataservices database 轴同款形态）。
	RecordOwners func(functionID string, gen uint64, ws []capability.Workload)
	// RecordSpecs 落投影 spec 账本（合成路由的后端解析期望集——§6：
	// ensuredSpec 是 Addresses 的端口匹配源；受管域 recordOwners 不落
	// spec，函数域因合成路由需要显式落）。
	RecordSpecs       func(functionID string, ws []capability.Workload)
	RecordExpected    func(functionID string, gen uint64)
	ForgetExpected    func(functionID string)
	ForgetObservation func(functionID string)
	ObservationOf     func(functionID string) (capability.WorkloadEvent, bool)
	KickFunctions     func()
	// EnsureMemo 是 per-Function Ensure 确保缓存表本体（组合根 make 后注入；
	// metastatewrite D3 同纪律）。
	EnsureMemo map[string]Memo
}

// FunctionsFactory 是组合根注入的装配器工厂（engine 核不 import 应用模块
// 包——测试与生产同源经端口袋互转，DiagnosisFactory 同款）。
type FunctionsFactory func(FunctionsPorts) Functions
