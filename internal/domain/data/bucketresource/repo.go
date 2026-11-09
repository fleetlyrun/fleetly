package bucketresource

import (
	"context"
)

// QuotaPerInstance 是 per 父实例的 Bucket 配额（B8 终裁值 = design §4.7
// 建议值 50：S3 桶是平面对象命名空间，50 已覆盖 per-App 隔离惯例；受理位
// E_QUOTA_EXCEEDED 同律；按数不按权——类型配额同一取舍〔design §7.4〕）。
const QuotaPerInstance = 50

// Repo 是 Bucket 子资源聚合存取（topic.Repo 同构——per 父实例寻址）。
type Repo interface {
	// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, b *Bucket) error
	// Get 按 ID 读行（tombstone 后一律不存在——受理面口径）。
	Get(ctx context.Context, id string) (*Bucket, error)
	// ListByBucketStore 返回父实例下全部行（含 tombstone：收敛段的
	// ensure 面与 GC 面共用；按 seq 升序稳定遍历）。
	ListByBucketStore(ctx context.Context, bucketStoreID string) ([]Bucket, error)
	// CountByBucketStore 返回父实例下活跃行数（受理位 per 实例配额口径：
	// buckets 50，design §4.7 配额建议值的 B8 终裁）。
	CountByBucketStore(ctx context.Context, bucketStoreID string) (int, error)
	// UpdateDesired 落期望态面（quota 全量替换；已删/不存在返回
	// state.ErrNotFound）。
	UpdateDesired(ctx context.Context, id string, quotaBytes *int64) error
	// SetStatus 落对账状态（同值写入不迁 updated_at；error 文案随行留痕）。
	SetStatus(ctx context.Context, id, status, statusError string) error
	// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
	SoftDelete(ctx context.Context, id string) error
	// Delete 物理删行（收敛段 GC 成功路径；幂等：不存在静默成功）。
	Delete(ctx context.Context, id string) error
	// DeleteByBucketStore 物理删父实例下全部行（父实例删除的级联收口；
	// 幂等）。
	DeleteByBucketStore(ctx context.Context, bucketStoreID string) error
}
