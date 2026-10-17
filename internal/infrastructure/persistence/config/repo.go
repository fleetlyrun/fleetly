// Package configrepo 是 Config 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/structure/config（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。包名 configrepo 避开与 internal/config（平台配置）的同名
// 混淆。
package configrepo

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	configrepo "github.com/fleetlyrun/fleetly/internal/domain/structure/config"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain configrepo.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain configrepo.Repo）。
func New(clock state.Clock, run state.Runner) configrepo.Repo { return &repo{clock: clock, run: run} }

// Create 落新版本（同 project+name 下一版本号单调递增；配额执法在 API
// 层——本 repo 只承载版本化事实）。
func (r *repo) Create(ctx context.Context, c *configrepo.Config) error {
	seq, err := persistence.NextSeq(ctx, r.run, "configs")
	if err != nil {
		return err
	}
	c.Seq = seq
	if c.ID == "" {
		c.ID = entityid.Join("cfg", seq)
	}
	next, err := r.NextVersion(ctx, c.ProjectID, c.Name)
	if err != nil {
		return err
	}
	c.Version = next
	c.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO configs (id, seq, project_id, name, version, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Seq, c.ProjectID, c.Name, c.Version, c.Content, c.CreatedAt)
	return err
}

// NextVersion 返回下一版本号（首版 1）。
func (r *repo) NextVersion(ctx context.Context, projectID, name string) (int64, error) {
	var v int64
	err := r.run.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM configs WHERE project_id = ? AND name = ?`,
		projectID, name).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

// Latest 返回最新版本（无 → state.ErrNotFound）。
func (r *repo) Latest(ctx context.Context, projectID, name string) (*configrepo.Config, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ?
		ORDER BY version DESC LIMIT 1`, projectID, name)
	return scanConfig(row.Scan)
}

// GetVersion 返回指定版本。
func (r *repo) GetVersion(ctx context.Context, projectID, name string, version int64) (*configrepo.Config, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ? AND version = ?`,
		projectID, name, version)
	return scanConfig(row.Scan)
}

// LatestByProject 返回 Project 内每个 name 的最新版本（全量列表面——
// 配额执法与引擎装配面消费；API 分页读面走 LatestByProjectPage）。
func (r *repo) LatestByProject(ctx context.Context, projectID string) ([]configrepo.Config, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT c.id, c.seq, c.project_id, c.name, c.version, c.content, c.created_at
		FROM configs c
		JOIN (SELECT name, MAX(version) AS v FROM configs WHERE project_id = ? GROUP BY name) m
		  ON c.name = m.name AND c.version = m.v AND c.project_id = ?
		ORDER BY c.name`, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []configrepo.Config
	for rows.Next() {
		c, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// LatestByProjectPage 是 List 的分页读面（ADR-0026 after_* + limit；游标
// 轴 = 既有排序轴 name 字典序升序）。每行仍是该 name 的最新版本（分页只
// 动行集不改每行形态）。limit<=0 或 >200 回落/钳制缺省 50。
func (r *repo) LatestByProjectPage(ctx context.Context, projectID, afterName string, limit int) ([]configrepo.Config, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := `
		SELECT c.id, c.seq, c.project_id, c.name, c.version, c.content, c.created_at
		FROM configs c
		JOIN (SELECT name, MAX(version) AS v FROM configs WHERE project_id = ? GROUP BY name) m
		  ON c.name = m.name AND c.version = m.v AND c.project_id = ?`
	args := []any{projectID, projectID}
	if afterName != "" {
		q += ` WHERE c.name > ?`
		args = append(args, afterName)
	}
	q += ` ORDER BY c.name ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []configrepo.Config
	for rows.Next() {
		c, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// ListVersions 返回全部版本（旧→新）。
func (r *repo) ListVersions(ctx context.Context, projectID, name string) ([]configrepo.Config, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ? ORDER BY version`,
		projectID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []configrepo.Config
	for rows.Next() {
		c, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func scanConfig(scan func(dest ...any) error) (*configrepo.Config, error) {
	var c configrepo.Config
	if err := scan(&c.ID, &c.Seq, &c.ProjectID, &c.Name, &c.Version, &c.Content, &c.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &c, nil
}
