// Package route 是 Route 聚合 repo（CONTEXT.md Route 词条：host/path →
// Process 端口的映射，附协议与 TLS 模式；Edge 上下文实体）。
package route

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Route 是聚合行（软删 tombstone）。
type Route struct {
	ID        string
	ProjectID string
	Host      string
	Path      string // 空 = /
	AppID     string
	Process   string
	Port      int32
	Protocol  capability.Protocol
	TLSMode   string // auto | none
	CreatedAt string
	UpdatedAt string
	DeletedAt string
}

// Deleted 报告 tombstone 状态。
func (r *Route) Deleted() bool { return r.DeletedAt != "" }

// Repo 是 Route 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；host+path 全局冲突（跨项目同 host 同为活跃行）返回
// state.ErrAlreadyExists（安全批：host 命名空间是平台级资产，唯一索引
// 已从项目内升级为全局——idx_routes_host_path）。
func (r *Repo) Create(ctx context.Context, run state.Runner, rt *Route) error {
	now := state.FormatTime(r.clock.Now())
	rt.CreatedAt, rt.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO routes (id, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		rt.ID, rt.ProjectID, rt.Host, normalizePath(rt.Path), rt.AppID, rt.Process,
		rt.Port, string(rt.Protocol), rt.TLSMode, rt.CreatedAt, rt.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: route %s%s already exists (host+path is globally unique across projects)", state.ErrAlreadyExists, rt.Host, rt.Path)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Route, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at, deleted_at
		FROM routes WHERE id = ?`, id)
	return scanRoute(row.Scan)
}

// List 返回全部活跃 Route（Edge 全量发布面；跨 Project）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Route, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at, deleted_at
		FROM routes WHERE deleted_at = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Route
	for rows.Next() {
		rt, err := scanRoute(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *rt)
	}
	return out, rows.Err()
}

// SoftDelete 落 tombstone（幂等）。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE routes SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	if err != nil {
		return err
	}
	if _, err := r.Get(ctx, run, id); err != nil {
		return err
	}
	return nil
}

// normalizePath 归一路径前缀（空与 "/" 等价，存空）。
func normalizePath(p string) string {
	if p == "/" {
		return ""
	}
	return p
}

func scanRoute(scan func(dest ...any) error) (*Route, error) {
	var rt Route
	var proto string
	err := scan(&rt.ID, &rt.ProjectID, &rt.Host, &rt.Path, &rt.AppID, &rt.Process,
		&rt.Port, &proto, &rt.TLSMode, &rt.CreatedAt, &rt.UpdatedAt, &rt.DeletedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	rt.Protocol = capability.Protocol(proto)
	return &rt, nil
}
