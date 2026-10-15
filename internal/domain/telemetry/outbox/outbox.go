// Package outbox 是事件 Outbox 聚合的领域面（架构 §6：状态迁移事件经
// Outbox 落库，单调 seq；消费面 = gRPC stream + SSE + events list——
// EventsService 族住 telemetry 上下文，故本聚合随服务族落 telemetry）。
// 事件名必须在 eventcode 注册表在册（三链咬合的运行时面：未注册名落库即
// 编程错误）。行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/outbox（ADR-0075 Repo 接口层路线）。
package outbox

// Event 是一条已落库事件（payload 是 JSON 字节）。TeamID 不在返回形态
// ——team 是读面过滤的内部列，不进 Event 消息（proto 契约不动，P0-2）。
type Event struct {
	Seq         int64
	Name        string
	Aggregate   string
	AggregateID string
	Payload     []byte
	CreatedAt   string
}
