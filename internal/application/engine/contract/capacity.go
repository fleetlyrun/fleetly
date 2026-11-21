// capacity.go 是通用伸缩决策环应用模块的核消费面（P2-2，ADR-0088 生长律
// ——新域动词落应用模块，进核只加循环表一行 + 组合根工厂注入；engine 核
// 零 import capacity 包）。端口形态全部落在 domain 面，契约叶子保持零
// 应用模块类型（ADR-0088 决策 7，functions.go 同款）。
package contract

import (
	"context"
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Capacity 是通用伸缩决策环应用模块的核消费面：循环步经 Module.Loops 挂载
// （ring 表第 14 环 "autoscale"）；决策写经 contract.CommitWrite 事务承载。
// v1 单消费者 Function（App 侧接入 = 后续批，ADR-0102 收编条款）。
type Capacity interface {
	Module
}

// QuotaCheck 是函数配额定律端口类型（ADR-0068 单源 CheckFunctionQuota 的
// 绑定闭包——delta 是本次副本增量；配额类失败须包装 ErrFunctionQuota
// 哨兵：决策环 defer 语义分流据此判定。类型在契约叶子单源，应用模块
// 别名消费——Deps↔Ports 结构体直转的字段类型恒等要求）。
type QuotaCheck func(ctx context.Context, st transact.Stores, projectID string, delta int64) error

// FunctionScaleEventPayload 是决策环事件族的载荷形状（function.scaled/
// function.scale_deferred 同族——schema 登记按族共用形状面；cause 携
// park|autoscale|wake 动机位，reason 仅 deferred 携，origin=autoscaler 标
// 决策环来源）。
type FunctionScaleEventPayload struct {
	Replicas int64  `json:"replicas"`
	Cause    string `json:"cause,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Origin   string `json:"origin,omitempty"`
}

// CapacityPorts 是 capacity 装配器的构造端口袋（组合根在 engine
// construct.go 绑定核方法值/闭包与 repo；与 capacity.Deps 逐字段同形）。
type CapacityPorts struct {
	Repos          transact.Stores
	Metrics        capability.Metrics
	RouteMetricKey func(capability.Route) string
	// QuotaCheck 是函数配额定律端口（ADR-0068 单源 CheckFunctionQuota 的
	// 绑定闭包——配额类失败须包装 ErrFunctionQuota 哨兵：决策环 defer
	// 语义分流据此判定）。
	QuotaCheck QuotaCheck
	// FactTeam 是事件 Team 轴解析端口（engine projectTeam 绑定闭包）。
	FactTeam      func(ctx context.Context, projectID string) (string, error)
	Commit        CommitWrite
	KickFunctions func()
	Log           *slog.Logger
	Clock         state.Clock

	BoundedStep func(ctx context.Context) (context.Context, context.CancelFunc)
}

// CapacityFactory 是组合根注入的装配器工厂（engine 核不 import 应用模块
// 包——测试与生产同源经端口袋互转，FunctionsFactory 同款）。
type CapacityFactory func(CapacityPorts) Capacity
