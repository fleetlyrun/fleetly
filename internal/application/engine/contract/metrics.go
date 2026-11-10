// metrics.go 是指标采集+告警域应用模块的核/模块契约面（engine 去应用模块
// import 第五批，2026-11-10）：engine 核只持 Metrics 接口与构造端口袋，具体
// 装配器由组合根经 engine.Deps.MetricsFactory 注入——核不再 import 指标
// 采集+告警应用模块包。值类型/事件常量随契约面下沉（gRPC ListAlertStates
// 组装面、converge 系统告警事件发射面与 schemareg 事件载荷注册面的既名
// 引用经 engine 别名零改）；端口形态全部落在 domain 面（capability/state/
// repo 签名），契约叶子保持零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// SystemAlertState 是内置规则（系统级）的现行状态（ListAlertStates 消费）。
// P5 wave-2 随 metrics 拆包下沉、engine 去应用模块 import 第五批随契约面
// 迁入本叶子——gRPC 组装面与 converge 缺省收敛循环的既名引用经 engine
// 类型别名零改。
type SystemAlertState struct {
	RuleID     string
	State      string
	StateSince time.Time
	System     bool
}

// 事件名（eventcode 注册表同源）。
const (
	// EventAlertFired 是用户/系统规则迁入 firing 的事件名（converge 缺省
	// 收敛循环的事件发射面同源消费）。
	EventAlertFired = "alert.fired"
	// EventAlertResolved 是规则迁回 ok 的事件名。
	EventAlertResolved = "alert.resolved"
	// EventAlertChannelFail 是通知通道投递失败的事件名（诊断面）。
	EventAlertChannelFail = "alert.channel_failed"
	// EventCrashLoopEntered 是进入 crash-loop 的沿 marker 事件名。
	EventCrashLoopEntered = "workload.crash_loop_entered"
	// EventCrashLoopCleared 是退出 crash-loop 的沿 marker 事件名。
	EventCrashLoopCleared = "workload.crash_loop_cleared"
)

// AlertEventPayload 是告警事件载荷（用户规则与系统内置规则同一载荷形态）。
type AlertEventPayload struct {
	RuleID    string  `json:"rule_id"`
	AppID     string  `json:"app_id,omitempty"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold,omitempty"`
	Value     float64 `json:"value,omitempty"`
	State     string  `json:"state"`
}

// AlertChannelFailedPayload 是 alert.channel_failed 事件载荷（诊断面）。
type AlertChannelFailedPayload struct {
	ChannelID string `json:"channel_id"`
	Error     string `json:"error"`
}

// CrashLoopEventPayload 是沿 marker 事件的载荷（字段只增；与
// container_exited 载荷同类——workload/app/process 归属 + 计数/终态快照）。
type CrashLoopEventPayload struct {
	WorkloadID   string `json:"workload_id"`
	AppID        string `json:"app_id,omitempty"`
	Process      string `json:"process,omitempty"`
	Generation   uint64 `json:"generation"`
	RestartCount int    `json:"restart_count"`
	State        string `json:"state"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Metrics 是指标采集+告警域应用模块的核消费面：engine 循环表经 Module.Loops
// 取 metrics 步，componentrestart/managed 面经 MetricsProvider，gRPC
// Alerting/Diagnosis 面经 repo 访问器与 SystemOffsiteAlertState/
// TestNotificationChannel，观测扇出经 EvaluateCrashLoopNotify，converge
// 缺省收敛循环的系统告警沿经 DispatchSystemAlert（消费点零改）。
type Metrics interface {
	Module
	// MetricsProvider 返回受管指标存储端口（nil = Metrics 面停用）。
	MetricsProvider() capability.Metrics
	// AlertRuleRepo 返回告警规则 repo（gRPC 受理面经 commit 原语持 tx 写行）。
	AlertRuleRepo() alertrule.Repo
	// NotificationChannelRepo 返回通知通道 repo（同上）。
	NotificationChannelRepo() channel.Repo
	// SystemOffsiteAlertState 是内置规则的现行状态（ListAlertStates 消费）。
	SystemOffsiteAlertState() SystemAlertState
	// TestNotificationChannel 即时派发一条测试载荷（API test 动词）。
	TestNotificationChannel(ctx context.Context, channelID string) error
	// EvaluateCrashLoopNotify 是诊断评估点（观测扇出的 crash-loop 投递沿）。
	EvaluateCrashLoopNotify(ctx context.Context, ev, prev capability.WorkloadEvent, hadPrev bool)
	// DispatchSystemAlert 把一条系统级规则迁移派发到全部启用通道。
	DispatchSystemAlert(ctx context.Context, ruleID, metric, state string, now time.Time)
}

// MetricsPorts 是指标采集+告警装配器的构造端口袋（组合根在 engine
// construct.go 绑定核方法值/闭包与 repo；形态与指标采集+告警应用模块的
// Deps 逐字段同形，契约叶子只依赖 domain 类型——应用模块包类型不得穿过
// 本叶子）。
type MetricsPorts struct {
	// Metrics 是受管指标存储端口（nil = Metrics 面停用，采集循环空转零噪音）。
	Metrics capability.Metrics
	// Runtime 是集群视图端口（DescribeCluster）。
	Runtime capability.Runtime
	// Repos 是只读聚合目录（crash-loop 沿 marker 扫描经 Outbox；事件 Team
	// 解析入参）。
	Repos transact.Stores
	// AlertRules 是告警规则 repo（规则行写与读）。
	AlertRules alertrule.Repo
	// Channels 是通知通道 repo（通道行写与投递记录）。
	Channels channel.Repo
	// Cipher 是通道配置解封（nil = 通道投递精确失败）。
	Cipher *material.Cipher
	// Log 是域内日志器。
	Log *slog.Logger
	// Clock 是域内时间面（节拍锚/载荷时间）。
	Clock state.Clock
	// OffsiteConfigured 是内置规则的只读事实端口（核侧闭包逐拍读 opts）。
	OffsiteConfigured func() bool
	// EmitObservation 是观测事实发射端口（核 emitObservation 方法值）。
	EmitObservation func(ctx context.Context, team, name, aggregate, id string, payload []byte) error
	// EventTeamOfApp 是 App 轴 team 解析端口（核 eventTeamOfApp 方法值）。
	EventTeamOfApp func(ctx context.Context, s transact.Stores, appID string) string
	// AppOwnerOf 是载体归属的 App 域窄面（核侧绑定 obs.ownerOf + ownerApp
	// 域过滤；非 App 域/不归属 = owned false）。
	AppOwnerOf func(workloadID string) (appID string, owned bool)
	// WorkloadProcess 是进程模板名窄导出端口（核 WorkloadProcess 方法值）。
	WorkloadProcess func(workloadID string) (string, bool)
	// RootCtx 是 lifecycle 派生根端口（engine 侧绑定 buildRootCtx）。
	RootCtx func() context.Context
	// Spawn 是监督 goroutine 起拍端口（Add/Done 配对单源留核）。
	Spawn func(fn func())
}

// MetricsFactory 由组合根注入（engine.Deps.MetricsFactory），把端口袋装配
// 成 Metrics。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时
// 指标采集+告警面停用：metrics 环退化为 no-op 步，门面 nil-safe。
type MetricsFactory func(MetricsPorts) Metrics
