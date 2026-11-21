package capacity

import (
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

var (
	// 核消费面编译期断言（ADR-0088 消费面纪律；functions 同款形态）。
	_ contract.Module = (*Capacity)(nil)
	// Deps 与契约端口袋逐字段同形（组合根工厂经直接互转装配；字段名/类型
	// 任一漂移即编译红）。
	_ = contract.CapacityPorts(Deps{})
)
