package sourceupload

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是上传产物聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落行；同 (project, digest) 已有行 → state.ErrAlreadyExists
	// （finalize 的去重路径先 FindByDigest，此形态只在并发同内容上传时可
	// 达）。
	Create(ctx context.Context, run state.Runner, u *Upload) error
	// Get 按 ID 读行。
	Get(ctx context.Context, run state.Runner, id string) (*Upload, error)
	// FindByDigest 找项目内同内容行（重传去重路径；无行 →
	// state.ErrNotFound）。
	FindByDigest(ctx context.Context, run state.Runner, projectID, digest string) (*Upload, error)
	// ListByProject 返回项目上传行（新→旧 + after 游标；ADR-0026 惯例）。
	ListByProject(ctx context.Context, run state.Runner, projectID string, afterSeq int64, limit int) ([]Upload, error)
	// BytesByProject 汇总项目上传存量（(project, digest) 唯一使 SUM 即去重
	// 值；配额读在 finalize 事务内——ADR-0024 TOCTOU 口径）。
	BytesByProject(ctx context.Context, run state.Runner, projectID string) (int64, error)
	// CountByDigest 数共享同一 blob 的行数（末行删除时 blob 一并删除）。
	CountByDigest(ctx context.Context, run state.Runner, digest string) (int64, error)
	// Delete 落删除（清扫路径）。
	Delete(ctx context.Context, run state.Runner, id string) error
	// SweepUnreferenced 清扫过保留窗且未被任何 Revision 引用的行（引用=
	// spec 体内含 upload id 字面量——26 字符 ULID 的碰撞面可忽略；Revision
	// 冻结先于 engine.Submit，部署路径的上传天然受保护）。返回被删行
	// （含 digest，供 blob refcount GC）。
	SweepUnreferenced(ctx context.Context, run state.Runner, cutoff string) ([]Upload, error)
}
