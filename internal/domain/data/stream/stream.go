// Package stream 是 Stream 子资源聚合的领域面（B8，ADR-0096 决策 10 /
// design §4.7）：nats pubsub 实例内的托管 JetStream Stream 声明行——
// **声明行 + admin 工具容器收敛**（nats-box 工具容器 `nats stream add/
// update/rm`；删除 = 声明式 GC，Stream 删除即数据删除——wide 档确认）。
// CONTEXT.md Stream 词条：nats 实例内的 JetStream 流。可变面与不可变面
// 分档（引擎方言约束，live 定案 2026-11-07 nats-box 0.15.0）：subjects 与
// max_stream_size 可变（`nats stream update --subjects/--max-bytes`）；
// storage 与 retention 创建后不可变（CLI 无该旗标——Update 受理在方言层
// 拒）。ADR-0075 Repo 接口层：行类型与 Repo 接口在此单源，SQLite 实现住
// infrastructure/persistence/stream。
package stream

// Status 值域（收敛段推进：pending/ready/error——topic.Status 同形注）。
const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusError   = "error"
)

// Retention 值域（nats 原生词：limits | interest | workqueue）。
const (
	RetentionLimits    = "limits"
	RetentionInterest  = "interest"
	RetentionWorkqueue = "workqueue"
)

// Storage 值域（nats 原生词：file | memory）。
const (
	StorageFile   = "file"
	StorageMemory = "memory"
)

// Stream 是聚合行（软删 tombstone = 声明式 GC 挂起；物理删除成功后收敛
// 段硬删行）。Subjects 是 nats 主题过滤器列表（JSON 数组列）；MaxBytes 为
// nil = 不设 max-bytes 上限。
type Stream struct {
	ID        string
	Seq       int64
	ProjectID string
	// PubSubID 是父 pubsub 实例行 id（nats 引擎才有 Stream 面）。
	PubSubID string
	Name     string
	// Subjects 是主题过滤器集（升序稳定存储；空集 = 无 subjects 的流
	// ——nats 允许，配 consumer 绑定用）。
	Subjects []string
	// Retention 是保留策略（创建后不可变）。缺省 limits。
	Retention string
	// Storage 是存储形态（创建后不可变；memory 流不可快照——备份面受理
	// 拒既有律，本面如实承载）。缺省 file。
	Storage string
	// MaxBytes 是 max_stream_size 期望（nil = 无上限；可变面）。
	MaxBytes  *int64
	Status    string
	StatusErr string
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态（= 声明式 GC 挂起中）。
func (s *Stream) Deleted() bool { return s.DeletedAt != "" }
