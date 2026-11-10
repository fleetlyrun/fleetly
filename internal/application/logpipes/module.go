package logpipes

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (s *Logpipes) Name() string { return "logpipes" }

// Loops implements contract.Module: the log ingestion loop.
func (s *Logpipes) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "logging", Step: s.Step}}
}

var _ contract.Module = (*Logpipes)(nil)

// Logging 是核消费面契约（engine 去应用模块 import 第二批，2026-11-10：
// engine 只持 contract.Logging 接口，具体装配器经组合根工厂注入）。
var _ contract.Logging = (*Logpipes)(nil)

// Deps 与核侧端口袋同形（组合根工厂把 contract.LoggingPorts 映射为 Deps；
// 字段漂移即编译红——契约叶子与应用模块装配面单源保鲜）。
var _ = contract.LoggingPorts(Deps{})
