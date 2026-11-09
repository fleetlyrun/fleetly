package stream

import (
	"context"
)

// QuotaPerInstance 是 per 父实例的 Stream 配额（B8 终裁值 = design §4.7
// 建议值 100：受理位 E_QUOTA_EXCEEDED 同律；按数不按权——类型配额同一
// 取舍〔design §7.4〕）。
const QuotaPerInstance = 100

// Repo 是 Stream 子资源聚合存取（topic.Repo 同构——per 父实例寻址）。
type Repo interface {
	// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, s *Stream) error
	// Get 按 ID 读行（tombstone 后一律不存在——受理面口径）。
	Get(ctx context.Context, id string) (*Stream, error)
	// ListByPubSub 返回父实例下全部行（含 tombstone：收敛段的 ensure 面
	// 与 GC 面共用；按 seq 升序稳定遍历）。
	ListByPubSub(ctx context.Context, pubsubID string) ([]Stream, error)
	// CountByPubSub 返回父实例下活跃行数（受理位 per 实例配额口径：
	// streams 100，design §4.7 配额建议值的 B8 终裁）。
	CountByPubSub(ctx context.Context, pubsubID string) (int, error)
	// UpdateDesired 落期望态面（可变面：subjects/max_bytes 全量替换；
	// retention/storage 不可变——受理位拒，本口不承载）。已删/不存在返回
	// state.ErrNotFound。
	UpdateDesired(ctx context.Context, id string, subjects []string, maxBytes *int64) error
	// SetStatus 落对账状态（同值写入不迁 updated_at；error 文案随行留痕）。
	SetStatus(ctx context.Context, id, status, statusError string) error
	// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
	SoftDelete(ctx context.Context, id string) error
	// Delete 物理删行（收敛段 GC 成功路径；幂等：不存在静默成功）。
	Delete(ctx context.Context, id string) error
	// DeleteByPubSub 物理删父实例下全部行（父实例删除的级联收口；幂等）。
	DeleteByPubSub(ctx context.Context, pubsubID string) error
}
