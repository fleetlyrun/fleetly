// diagnosis.go 是诊断域应用模块的核/模块契约面（engine 去应用模块 import
// 试点，2026-11-10）：engine 核只持 Diagnosis 接口与构造端口袋，具体装配器
// 由组合根经 engine.Deps.DiagnosisFactory 注入——核不再 import 诊断应用模块
// 包。端口形态全部落在 domain 面（repo/clock/观测导出面签名），契约叶子保持
// 零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// Diagnosis 是诊断域应用模块的核消费面（读时装配器）：engine 门面
// DiagnoseFacts/DiagnoseAppCheapHit 委托本接口，gRPC 消费点零改。
type Diagnosis interface {
	// Facts 装配某 App 的诊断事实快照（读时计算面）。
	Facts(ctx context.Context, appID string) diagnosis.Facts
	// CheapHit 报告廉价先行筛命中（纯内存判定）。
	CheapHit(appID string) bool
}

// DiagnosisPorts 是诊断装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值/repo；形态与诊断应用模块的 Deps 逐字段同形，契约叶子只依赖
// domain 类型——应用模块包类型不得穿过本叶子）。
type DiagnosisPorts struct {
	// Clock 是装配节拍（Now 进 Facts）。
	Clock state.Clock
	// Outbox 是事件账本读面（ListByAggregateIDs 的 App 级一次往返）。
	Outbox outbox.Repo
	// AlertRules 是告警行读面（firing+pending；repo 持有权随 metrics 应用模块）。
	AlertRules alertrule.Repo
	// Deployments 是 App 部署行读面（第五事实源）。
	Deployments deployment.Repo
	// ObservationSnapshot 是 obs 活状态快照导出面端口（核方法值）。
	ObservationSnapshot func(ctx context.Context, appID string) []capability.WorkloadEvent
	// WorkloadProcess 是载体进程模板名窄导出端口（核方法值）。
	WorkloadProcess func(workloadID string) (string, bool)
}

// DiagnosisFactory 由组合根注入（engine.Deps.DiagnosisFactory），把端口袋
// 装配成 Diagnosis。engine 核不 import 应用模块包；工厂缺席（手动测试装配）
// 时诊断面停用，门面 nil-safe。
type DiagnosisFactory func(DiagnosisPorts) Diagnosis
