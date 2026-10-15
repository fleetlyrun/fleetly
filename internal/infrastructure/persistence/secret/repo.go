// Package secret 是 Secret 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/structure/secret（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package secret

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain secret.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain secret.Repo）。
func New(clock state.Clock) secret.Repo { return &repo{clock: clock} }

// Upsert 写入（同名活跃行覆盖：fingerprint 变更即新值；四件一拍的审计
// 由调用方同事务组合）。
func (r *repo) Upsert(ctx context.Context, run state.Runner, s *secret.Secret) error {
	now := state.FormatTime(r.clock.Now())
	if existing, err := r.GetByName(ctx, run, s.ProjectID, s.Name); err == nil {
		_, err := run.ExecContext(ctx, `
			UPDATE secrets SET ciphertext = ?, fingerprint = ?, updated_at = ?, deleted_at = ''
			WHERE id = ?`,
			s.Ciphertext, s.Fingerprint, now, existing.ID)
		s.ID, s.CreatedAt = existing.ID, existing.CreatedAt
		s.UpdatedAt = now
		return err
	}
	s.CreatedAt, s.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, run, "secrets")
	if err != nil {
		return err
	}
	s.Seq = seq
	if s.ID == "" {
		s.ID = entityid.Join("sec", seq)
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO secrets (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '')`,
		s.ID, s.Seq, s.ProjectID, s.Name, s.Ciphertext, s.Fingerprint, s.CreatedAt, s.UpdatedAt)
	// 并发同名 PUT 交错（GetByName 未命中 ↔ 对端已落活跃行）撞部分唯一
	// 索引时归一为 ErrAlreadyExists——apps/volumes 同款两步形态。
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: secret name %q already exists in project", state.ErrAlreadyExists, s.Name)
	}
	return err
}

// GetByName 读活跃行（含密文——仅注入/分发路径调用；API 回显面只用
// ListFingerprints）。
func (r *repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*secret.Secret, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at
		FROM secrets WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanSecret(row.Scan)
}

// ListFingerprints 返回 Project 活跃 Secret 的指纹面（回显契约：值永不
// 出现）。name 字典序升序 + after 游标（ADR-0026 after_* + limit 惯例
// ——游标轴 = 既有排序轴 name，分页只动行集不改每行形态）。limit<=0 或
// >200 回落/钳制缺省 50。
func (r *repo) ListFingerprints(ctx context.Context, run state.Runner, projectID, afterName string, limit int) ([]secret.Secret, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, seq, project_id, name, NULL, fingerprint, created_at, updated_at, deleted_at
		FROM secrets WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterName != "" {
		q += ` AND name > ?`
		args = append(args, afterName)
	}
	q += ` ORDER BY name LIMIT ?`
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []secret.Secret
	for rows.Next() {
		s, err := scanSecret(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListAll 读全表行（含 tombstone）：离线维护面（KEK 重封）专用——墓碑
// 行的密文同样要可解（undelete 路径依赖），含密文列故禁止 API 回显面
// 消费。
func (r *repo) ListAll(ctx context.Context, run state.Runner) ([]secret.Secret, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at
		FROM secrets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []secret.Secret
	for rows.Next() {
		s, err := scanSecret(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// UpdateCiphertext 重写密文（KEK 重封专用：fingerprint 是明文的函数，
// 重封不变；updated_at 随写推进）。行不存在不报错——调用方（rewrap）的
// 行列表与本写同窗口，无并发删除面。
func (r *repo) UpdateCiphertext(ctx context.Context, run state.Runner, id string, ciphertext []byte) error {
	_, err := run.ExecContext(ctx, `
		UPDATE secrets SET ciphertext = ?, updated_at = ? WHERE id = ?`,
		ciphertext, state.FormatTime(r.clock.Now()), id)
	return err
}

// SoftDelete 落 tombstone（幂等）。
func (r *repo) SoftDelete(ctx context.Context, run state.Runner, projectID, name string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE secrets SET deleted_at = ?, updated_at = ?
		WHERE project_id = ? AND name = ? AND deleted_at = ''`, now, now, projectID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := r.GetByName(ctx, run, projectID, name)
		return err
	}
	return nil
}

func scanSecret(scan func(dest ...any) error) (*secret.Secret, error) {
	var s secret.Secret
	err := scan(&s.ID, &s.Seq, &s.ProjectID, &s.Name, &s.Ciphertext, &s.Fingerprint,
		&s.CreatedAt, &s.UpdatedAt, &s.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &s, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)
