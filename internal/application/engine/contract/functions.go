// functions.go 是 Functions 应用模块的核/模块契约面（ADR-0102 任务③B，
// engine 去应用模块 import——组合根经 engine.Deps.FunctionFactory 注入装配
// 器，核零 import functions 包）。端口形态全部落在 domain 面，契约叶子
// 保持零应用模块类型（ADR-0088 决策 7 裁定二，dataservice.go 同款）。
package contract

import (
	"context"
	"errors"
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// FunctionRequestStats 是 per-Function 请求观测结果（P2-4 per-invocation
// 消费面——序列 + observed 缺席断口；结构叶子让核门面与 gRPC 窄接口同
// 签名，B1 律键公式仍不出 Provider）。
type FunctionRequestStats struct {
	RequestsPerSecond []capability.SeriesPoint
	P95Ms             []capability.SeriesPoint
	Observed          bool
}

// ErrFunctionQuota 是函数配额哨兵（真源在契约叶子——API 受理位映射
// E_QUOTA_EXCEEDED 与 P2-2 决策环 defer 语义的 errors.Is 消费同源；
// browses ErrBrowseQuota 先例）。
var ErrFunctionQuota = errors.New("engine: project function quota exceeded")

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
	Ensure              EnsureCache
	ProjectTeam         func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFacts func(ctx context.Context, projectID string) ([]string, map[string]bool)
	// Observations 是 function 域观测句柄（批 1①b，BindDomain 铸造——域
	// 判别随句柄收口）；RecordProjected 落投影 spec 账本（合成路由的后端
	// 解析期望集——§6：ensuredSpec 是 Addresses 的端口匹配源；受管域
	// RecordOwners 不落 spec，函数域因合成路由需要显式落）。
	Observations      Observations
	ForgetObservation func(functionID string)
	ObservationOf     func(functionID string) (capability.WorkloadEvent, bool)
	Kick              Kicker
	// Commit 是写事务端口（P2-3：构建就绪门的环内兑现——SwapCurrent + 清位
	// + 事件一事务；组合根绑核 commitWrite）。
	Commit CommitWrite
	// RegistryEndpoint 是受管仓端点端口（P2-3 投影半边：from_build digest
	// 引用组合——LocalImageDigestRef 需要地址；组合根绑核 registryEndpoint）。
	RegistryEndpoint func(ctx context.Context) (capability.RegistryEndpoint, error)
	// IntakeBuildFace 是构建摄取窄端口（P2-3：inputs 登记导出出口——模块
	// 间禁 import 的窄化消费面；PinVolumes 先例。nil = 构建源停用）。
	IntakeBuildFace IntakeBuildFace
}

// IntakeBuildFace 是 Function 构建行输入登记的窄端口（P2-3——完整 Intake
// 接口的方法值窄化：模块间消费面最小化，ADR-0088 消费面纪律）。
type IntakeBuildFace interface {
	PrepareFunctionBuild(ctx context.Context, b build.Build, fnRow function.Function, rev function.Revision) error
}

// FunctionsFactory 是组合根注入的装配器工厂（engine 核不 import 应用模块
// 包——测试与生产同源经端口袋互转，DiagnosisFactory 同款）。
type FunctionsFactory func(FunctionsPorts) Functions
