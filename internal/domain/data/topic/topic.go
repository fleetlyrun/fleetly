// Package topic 是 Topic 子资源聚合的领域面（B8，ADR-0096 决策 10 / design
// §4.7 子资源管理面）：kafka 协议族（kafka/redpanda）pubsub 实例内的托管
// Topic 声明行——**声明行 + admin 工具容器收敛**（对齐 ADR-0081/0082 严格
// 声明式：行即期望态，dataservices 卫星 pubsub 分族步尾段的子资源对账段
// 对照实况 ensure——存在+配置匹配否则创建/更新；删除 = 声明式 GC：行删→
// 收敛段删实况，Topic 删除即数据删除）。CONTEXT.md Topic 词条：kafka 协议
// 族 pubsub 实例内的消息分区主题；**副本恒 1**（单 broker 事实，目录 note
// 标注）。引擎可变面约束（partitions 可增不可减）在 pubsubtemplate 方言层
// 校验，域行面只承载已受理值。ADR-0075 Repo 接口层：行类型与 Repo 接口在
// 此单源，SQLite 实现住 infrastructure/persistence/topic。
package topic

// Status 值域（收敛段推进：pending = 行刚落/待对账；ready = 最近一次对账
// 实况与期望态匹配；error = 最近一次对账失败——错误留痕行上，收敛段下一
// 拍重试）。
const (
	StatusPending = "pending"
	StatusReady   = "ready"
	StatusError   = "error"
)

// Topic 是聚合行（软删 tombstone = 声明式 GC 挂起；物理删除成功后收敛段
// 硬删行）。version 无关（子资源面与引擎版本解耦——方言按父实例 engine
// 分派）。Partitions/RetentionMS 是期望态配置：Partitions 恒 ≥1；RetentionMS
// 为 nil = 不管理 retention.ms（引擎缺省），设置后 Update 可改。
type Topic struct {
	ID        string
	Seq       int64
	ProjectID string
	// PubSubID 是父 pubsub 实例行 id（受理位校验父在册 + engine 方言可用
	// 性：kafka 协议族才有 Topic 面）。
	PubSubID string
	Name     string
	// Partitions 是期望分区数（kafka 引擎约束：可增不可减——Update 受理
	// 在方言层拒减）。
	Partitions int32
	// RetentionMS 是期望 retention.ms（nil = 不管理；>0 时收敛段对照
	// live 配置 ensure）。
	RetentionMS *int64
	Status      string
	// StatusError 是最近一次对账失败的报文（历史事实留痕；成功对账清位）。
	StatusError string
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   string
}

// Deleted 报告 tombstone 状态（= 声明式 GC 挂起中）。
func (t *Topic) Deleted() bool { return t.DeletedAt != "" }
