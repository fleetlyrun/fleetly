// Package secret 是 Secret 聚合 repo（CONTEXT.md Secret 词条：Project 级
// 敏感值实体，age 信封加密存储，值永不回显只回指纹；注入 App/Task/
// Database）。密文由 internal/material 加解密，本 repo 只管存取。
package secret

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Secret 是聚合行（ciphertext 是 age 信封；fingerprint 供回显对账）。
type Secret struct {
	ID          string
	ProjectID   string
	Name        string
	Ciphertext  []byte
	Fingerprint string
	CreatedAt   string
	UpdatedAt   string
	DeletedAt   string
}

// Deleted 报告 tombstone 状态。
func (s *Secret) Deleted() bool { return s.DeletedAt != "" }

// Repo 是 Secret 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Upsert 写入（同名活跃行覆盖：fingerprint 变更即新值；四件一拍的审计
// 由调用方同事务组合）。
func (r *Repo) Upsert(ctx context.Context, run state.Runner, s *Secret) error {
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
	_, err := run.ExecContext(ctx, `
		INSERT INTO secrets (id, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		s.ID, s.ProjectID, s.Name, s.Ciphertext, s.Fingerprint, s.CreatedAt, s.UpdatedAt)
	return err
}

// GetByName 读活跃行（含密文——仅注入/分发路径调用；API 回显面只用
// ListFingerprints）。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Secret, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at
		FROM secrets WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanSecret(row.Scan)
}

// ListFingerprints 返回 Project 活跃 Secret 的指纹面（回显契约：值永不
// 出现）。name 字典序升序 + after 游标（ADR-0026 after_* + limit 惯例
// ——游标轴 = 既有排序轴 name，分页只动行集不改每行形态）。limit<=0 或
// >200 回落/钳制缺省 50。
func (r *Repo) ListFingerprints(ctx context.Context, run state.Runner, projectID, afterName string, limit int) ([]Secret, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := `
		SELECT id, project_id, name, NULL, fingerprint, created_at, updated_at, deleted_at
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
	var out []Secret
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
func (r *Repo) ListAll(ctx context.Context, run state.Runner) ([]Secret, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at
		FROM secrets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Secret
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
func (r *Repo) UpdateCiphertext(ctx context.Context, run state.Runner, id string, ciphertext []byte) error {
	_, err := run.ExecContext(ctx, `
		UPDATE secrets SET ciphertext = ?, updated_at = ? WHERE id = ?`,
		ciphertext, state.FormatTime(r.clock.Now()), id)
	return err
}

// SoftDelete 落 tombstone（幂等）。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, projectID, name string) error {
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

func scanSecret(scan func(dest ...any) error) (*Secret, error) {
	var s Secret
	err := scan(&s.ID, &s.ProjectID, &s.Name, &s.Ciphertext, &s.Fingerprint,
		&s.CreatedAt, &s.UpdatedAt, &s.DeletedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	return &s, nil
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)
