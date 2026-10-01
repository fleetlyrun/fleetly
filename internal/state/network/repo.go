// Package networkrepo 是 Network 聚合 repo（CONTEXT.md Network 词条：
// Project 级互通附件；成员为 Project 进程、Task 网络组挂靠或显式跨
// Project 引用；可声明 egress:none。包名避开内部网络实现混淆）。
package networkrepo

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Network 是聚合行。
type Network struct {
	ID         string
	ProjectID  string
	Name       string
	EgressNone bool
	CreatedAt  string
	DeletedAt  string
}

// Deleted 报告 tombstone 状态。
func (n *Network) Deleted() bool { return n.DeletedAt != "" }

// Repo 是 Network 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
func (r *Repo) Create(ctx context.Context, run state.Runner, n *Network) error {
	now := state.FormatTime(r.clock.Now())
	n.CreatedAt = now
	egress := 0
	if n.EgressNone {
		egress = 1
	}
	_, err := run.ExecContext(ctx, `
		INSERT INTO networks (id, project_id, name, egress_none, created_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, '')`,
		n.ID, n.ProjectID, n.Name, egress, n.CreatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: network name %q already exists in project", state.ErrAlreadyExists, n.Name)
	}
	return err
}

// GetByID 按 ID 直读活跃行（peer 声明解析面：声明行只挂活跃网络）。
func (r *Repo) GetByID(ctx context.Context, run state.Runner, id string) (*Network, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE id = ? AND deleted_at = ''`, id)
	return scanNetwork(row.Scan)
}

// GetByName 读活跃行。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Network, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanNetwork(row.Scan)
}

// ListByProject 返回 Project 全部活跃网络。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID string) ([]Network, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE project_id = ? AND deleted_at = '' ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Network
	for rows.Next() {
		n, err := scanNetwork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// List 返回全部活跃网络（跨 Project；受管 Edge 挂网的全量真源，N0 修复
// 批 B1）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Network, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE deleted_at = '' ORDER BY project_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Network
	for rows.Next() {
		n, err := scanNetwork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func scanNetwork(scan func(dest ...any) error) (*Network, error) {
	var n Network
	var egress int
	err := scan(&n.ID, &n.ProjectID, &n.Name, &egress, &n.CreatedAt, &n.DeletedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	n.EgressNone = egress != 0
	return &n, nil
}
