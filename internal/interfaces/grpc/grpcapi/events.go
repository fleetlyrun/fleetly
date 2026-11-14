package grpcapi

// StructureEventPayload 是结构面/数据服务面事件的最小载荷（id + project_id；
// teardown-aborted 额外带 active_deployments 诊断字段）。
type StructureEventPayload struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	// ActiveDeployments 是 teardown-aborted 事件的诊断字段（拒删时收口后
	// 复查所见的活跃部署数；其余事件恒缺省）。
	ActiveDeployments int `json:"active_deployments,omitempty"`
}

// EventSchemaRegistrar 是事件 payload schema 的注册回调：handler 包导出
// RegisterEventSchemas，由 fleetlygrpc 聚合注入 schema/eventcode 注册实现。
type EventSchemaRegistrar func(name string, payload any)
