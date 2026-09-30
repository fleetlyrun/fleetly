// Package configrepo 是 Config 聚合 repo（CONTEXT.md Config 词条：版本化
// 的非敏感挂载文件实体，可回读、有配额——OT-3）。包名 configrepo 避开
// 与 internal/config（平台配置）的同名混淆。
package configrepo

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Config 是聚合行（不可变版本：只有 INSERT 与读）。
type Config struct {
	ID        string
	ProjectID string
	Name      string
	Version   int64
	Content   []byte
	CreatedAt string
}

// Repo 是 Config 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落新版本（同 project+name 下一版本号单调递增；配额执法在 API
// 层——本 repo 只承载版本化事实）。
func (r *Repo) Create(ctx context.Context, run state.Runner, c *Config) error {
	next, err := r.NextVersion(ctx, run, c.ProjectID, c.Name)
	if err != nil {
		return err
	}
	c.Version = next
	c.CreatedAt = state.FormatTime(r.clock.Now())
	_, err = run.ExecContext(ctx, `
		INSERT INTO configs (id, project_id, name, version, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProjectID, c.Name, c.Version, c.Content, c.CreatedAt)
	return err
}

// NextVersion 返回下一版本号（首版 1）。
func (r *Repo) NextVersion(ctx context.Context, run state.Runner, projectID, name string) (int64, error) {
	var v int64
	err := run.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM configs WHERE project_id = ? AND name = ?`,
		projectID, name).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

// Latest 返回最新版本（无 → state.ErrNotFound）。
func (r *Repo) Latest(ctx context.Context, run state.Runner, projectID, name string) (*Config, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ?
		ORDER BY version DESC LIMIT 1`, projectID, name)
	return scanConfig(row.Scan)
}

// GetVersion 返回指定版本。
func (r *Repo) GetVersion(ctx context.Context, run state.Runner, projectID, name string, version int64) (*Config, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ? AND version = ?`,
		projectID, name, version)
	return scanConfig(row.Scan)
}

// LatestByProject 返回 Project 内每个 name 的最新版本（列表面）。
func (r *Repo) LatestByProject(ctx context.Context, run state.Runner, projectID string) ([]Config, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT c.id, c.project_id, c.name, c.version, c.content, c.created_at
		FROM configs c
		JOIN (SELECT name, MAX(version) AS v FROM configs WHERE project_id = ? GROUP BY name) m
		  ON c.name = m.name AND c.version = m.v AND c.project_id = ?
		ORDER BY c.name`, projectID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Config
	for rows.Next() {
		c, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListVersions 返回全部版本（旧→新）。
func (r *Repo) ListVersions(ctx context.Context, run state.Runner, projectID, name string) ([]Config, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, version, content, created_at
		FROM configs WHERE project_id = ? AND name = ? ORDER BY version`,
		projectID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Config
	for rows.Next() {
		c, err := scanConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func scanConfig(scan func(dest ...any) error) (*Config, error) {
	var c Config
	if err := scan(&c.ID, &c.ProjectID, &c.Name, &c.Version, &c.Content, &c.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	return &c, nil
}
