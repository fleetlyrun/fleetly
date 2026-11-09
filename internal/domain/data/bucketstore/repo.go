package bucketstore

import (
	"context"
)

// Repo 是 Bucket Store 聚合存取（pubsub.Repo 同形——方法集机械平移；执行面
// 构造绑定 ADR-0080：组合根以 db 独立执行形态构造本 repo，事务态由
// transact.Binder 以事务执行面重造）。备份/恢复面（SetLastBackupAt/
// SetRestorePending/SetRestoreRunning/SetRestoreSucceeded/SetRestoreFailed/
// SweepInterruptedRestores）不设——本族 DataCapabilities.Backup=false（转储
// 挂账），列在表而面不在（pubsub 同形表的机械差异：无恢复消费面）。
type Repo interface {
	// Create 落一行；同 Project 内同名冲突返回 state.ErrAlreadyExists。
	Create(ctx context.Context, b *BucketStore) error
	// Get 按 ID 读活跃行（tombstone 后一律不存在）。
	Get(ctx context.Context, id string) (*BucketStore, error)
	// GetByName 在 Project 内按名读活跃行。
	GetByName(ctx context.Context, projectID, name string) (*BucketStore, error)
	// ListByProject 新→旧分页（after_seq 游标 = seq 创建序）。
	ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int) ([]BucketStore, error)
	// List 返回全部活跃行（收敛环的枚举面；按 seq 升序稳定遍历）。
	List(ctx context.Context) ([]BucketStore, error)
	// CountByProject 返回 Project 内活跃行数（受理位配额口径）。
	CountByProject(ctx context.Context, projectID string) (int, error)
	// EnsureGeneration 是收敛环的下发编号口（database 同律：指纹一致 →
	// 现行 gen 幂等重放；变化 → gen+1 与新指纹同事务落行）。
	EnsureGeneration(ctx context.Context, id, fingerprint string) (uint64, error)
	// SetStatus 落观测状态（同值写入不迁 updated_at）。
	SetStatus(ctx context.Context, id, status string) error
	// UpdateDesired 落期望态面（settings/resources 全量替换；值域校验在
	// 受理位，本口只承载行写；已删/不存在返回 state.ErrNotFound）。
	UpdateDesired(ctx context.Context, id string, settings map[string]string, cpuMillis, memoryMB *int32) error
	// SoftDelete 落 tombstone（幂等口径与 Get 一致）。
	SoftDelete(ctx context.Context, id string) error
}
