package topic

import (
	"context"
)

// QuotaPerInstance 是 per 父实例的 Topic 配额（B8 终裁值 = design §4.7 建议
// 值 100：受理位 E_QUOTA_EXCEEDED 同律。按数不按权——与家族配额同一取舍
// 〔design §7.4 评审 F-16〕：真实约束靠父实例资源面与单 broker 容量）。
const QuotaPerInstance = 100

// Repo 是 Topic 子资源聚合存取（pubsub.Repo 的子资源同构——per 父实例
// 寻址；执行面构造绑定 ADR-0080：组合根以 db 独立执行形态构造本 repo，
// 事务态由 transact.Binder 以事务执行面重造）。
type Repo interface {
	// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, t *Topic) error
	// Get 按 ID 读行（tombstone 后一律不存在——受理面口径）。
	Get(ctx context.Context, id string) (*Topic, error)
	// ListByPubSub 返回父实例下全部行（含 tombstone：收敛段的 ensure 面
	// 与 GC 面共用——按 seq 升序稳定遍历）。
	ListByPubSub(ctx context.Context, pubsubID string) ([]Topic, error)
	// CountByPubSub 返回父实例下活跃行数（受理位 per 实例配额口径：
	// topics 100，design §4.7 配额建议值的 B8 终裁）。
	CountByPubSub(ctx context.Context, pubsubID string) (int, error)
	// UpdateDesired 落期望态面（partitions/retention 全量替换；已删/不
	// 存在返回 state.ErrNotFound；受理位校验在方言层，本口只承载行写）。
	UpdateDesired(ctx context.Context, id string, partitions int32, retentionMS *int64) error
	// SetStatus 落对账状态（同值写入不迁 updated_at；error 文案随行留痕）。
	SetStatus(ctx context.Context, id, status, statusError string) error
	// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
	SoftDelete(ctx context.Context, id string) error
	// Delete 物理删行（收敛段 GC 成功路径——物理 Topic 已删，行随之消
	// 失；幂等：不存在静默成功）。
	Delete(ctx context.Context, id string) error
	// DeleteByPubSub 物理删父实例下全部行（父实例删除的级联收口——父
	// 载体拆除即物理面消亡，无 GC 可做；幂等）。
	DeleteByPubSub(ctx context.Context, pubsubID string) error
}
