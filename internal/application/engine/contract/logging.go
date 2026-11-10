// logging.go 是采集循环应用模块的核/模块契约面（engine 去应用模块 import
// 第二批，2026-11-10）：engine 核只持 Logging 接口与构造端口袋，具体装配器
// 由组合根经 engine.Deps.LoggingFactory 注入——核不再 import 采集应用模块
// 包。端口形态全部落在 domain 面（capability/state/repo 签名），契约叶子
// 保持零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"log/slog"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/logcursor"
)

// Logging 是采集循环应用模块的核消费面：engine 循环表经 Module.Loops
// 取 logging 步，gRPC 装配面经 LoggingProvider 取受管日志存储（nil =
// Logging 面停用）。
type Logging interface {
	Module
	// LoggingProvider 返回受管日志存储端口（build 日志回读/managed 面消费）。
	LoggingProvider() capability.Logging
}

// LoggingPorts 是采集装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值/闭包与 repo；形态与采集应用模块的 Deps 逐字段同形，契约
// 叶子只依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type LoggingPorts struct {
	// Logging 是受管日志存储端口（nil = 采集/检索面停用）。
	Logging capability.Logging
	// Runtime 是运行时端口（Logs 扩展接口承载 Follow 流）。
	Runtime capability.Runtime
	// Proxy 是受管 Proxy 声明读面（受管域枚举）。
	Proxy capability.Proxy
	// Registry 是受管 Registry 声明读面（受管域枚举）。
	Registry capability.Registry
	// Repos 是只读聚合目录（权威表枚举；ADR-0088 决策 3 允许整体注入）。
	Repos transact.Stores
	// Log 是域内日志器。
	Log *slog.Logger
	// Clock 是域内时间面（帧时间兜底/闲置判据/节流锚）。
	Clock state.Clock
	// Cursors 是采集游标 repo（Ingest 成功后推进）。
	Cursors logcursor.Repo
	// RootCtx 是 lifecycle 派生根端口（核方法值；未 Start 退化为 Background）。
	RootCtx func() context.Context
	// Spawn 是监督 goroutine 起拍端口（Add/Done 配对单源留核）。
	Spawn func(func())
}

// LoggingFactory 由组合根注入（engine.Deps.LoggingFactory），把端口袋装配
// 成 Logging。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时
// 采集面停用：logging 环退化为 no-op 步，门面 nil-safe。
type LoggingFactory func(LoggingPorts) Logging
