// Package revision 是 Revision 聚合 repo（CONTEXT.md Revision 词条：冻结
// 且不可变的规范化 Spec 快照，回滚与审计的单位）。存储形态 = blob 落库
// 裁决（2026-09-30）：spec 列存 AppSpec 的 protojson 规范序列化，digest
// 列存其 sha256——Platform Backup = 单 SQLite 文件 + 密钥即全部，无第二
// 棵状态树。不可变：无 UPDATE 路径，只有 INSERT 与读。
package revision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Revision 是聚合行（不可变）。
type Revision struct {
	ID        string
	AppID     string
	Seq       int64
	Digest    string
	Spec      []byte
	CreatedAt string
}

// Repo 是 Revision 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Digest 计算 spec 规范序列化的冻结指纹（sha256 hex）。
func Digest(spec []byte) string {
	sum := sha256.Sum256(spec)
	return hex.EncodeToString(sum[:])
}

// Create 落一行（digest 由 spec 派生；同 App 序号唯一、同 App 同内容唯一
// ——重复冻结同内容 → state.ErrAlreadyExists，调用方应改走 FindByDigest
// 复用）。
func (r *Repo) Create(ctx context.Context, run state.Runner, rev *Revision) error {
	now := state.FormatTime(r.clock.Now())
	rev.CreatedAt = now
	rev.Digest = Digest(rev.Spec)
	_, err := run.ExecContext(ctx, `
		INSERT INTO revisions (id, app_id, seq, digest, spec, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		rev.ID, rev.AppID, rev.Seq, rev.Digest, rev.Spec, rev.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: revision with identical spec already frozen for app", state.ErrAlreadyExists)
	}
	return err
}

// FindByDigest 按 App + 内容指纹找既有冻结体（内容寻址复用：无变化重
// 部署复用 Rn 而非新冻结 Rn+1）。
func (r *Repo) FindByDigest(ctx context.Context, run state.Runner, appID, digest string) (*Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, created_at
		FROM revisions WHERE app_id = ? AND digest = ?`, appID, digest)
	return scanRevision(row.Scan)
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, created_at
		FROM revisions WHERE id = ?`, id)
	return scanRevision(row.Scan)
}

// GetBySeq 按 App 内序号读（R1..Rn 对外形态）。
func (r *Repo) GetBySeq(ctx context.Context, run state.Runner, appID string, seq int64) (*Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, created_at
		FROM revisions WHERE app_id = ? AND seq = ?`, appID, seq)
	return scanRevision(row.Scan)
}

// Latest 返回 App 最新 Revision（无 → state.ErrNotFound；"上一成功"由
// 调用方结合 Deployment 终态判定，本 repo 不持部署语义）。
func (r *Repo) Latest(ctx context.Context, run state.Runner, appID string) (*Revision, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, app_id, seq, digest, spec, created_at
		FROM revisions WHERE app_id = ? ORDER BY seq DESC LIMIT 1`, appID)
	return scanRevision(row.Scan)
}

// NextSeq 返回 App 下一序号（首条 = 1；单调推进）。
func (r *Repo) NextSeq(ctx context.Context, run state.Runner, appID string) (int64, error) {
	var seq int64
	err := run.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(seq), 0) FROM revisions WHERE app_id = ?`, appID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq + 1, nil
}

// ListByApp 返回 App 全部 Revision（序号升序）。
func (r *Repo) ListByApp(ctx context.Context, run state.Runner, appID string) ([]Revision, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, app_id, seq, digest, spec, created_at
		FROM revisions WHERE app_id = ? ORDER BY seq`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Revision
	for rows.Next() {
		rev, err := scanRevision(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *rev)
	}
	return out, rows.Err()
}

func scanRevision(scan func(dest ...any) error) (*Revision, error) {
	var rev Revision
	if err := scan(&rev.ID, &rev.AppID, &rev.Seq, &rev.Digest, &rev.Spec, &rev.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &rev, nil
}
