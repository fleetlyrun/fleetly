// Package volume 是 Volume 聚合 repo（CONTEXT.md Volume 词条：持久存储
// 附件；无显式 Placement 时默认钉住节点——pinned_node_id 是平台节点 ID
// 锚，节点 ID 永不复用）。
package volume

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Volume 是聚合行。
type Volume struct {
	ID           string
	ProjectID    string
	Name         string
	PinnedNodeID string
	CreatedAt    string
	UpdatedAt    string
	DeletedAt    string
}

// Deleted 报告 tombstone 状态。
func (v *Volume) Deleted() bool { return v.DeletedAt != "" }

// Repo 是 Volume 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
// pinnedNodeID 为空表示待钉住（首次挂载时由引擎分配——分配后不可变）。
func (r *Repo) Create(ctx context.Context, run state.Runner, v *Volume) error {
	now := state.FormatTime(r.clock.Now())
	v.CreatedAt, v.UpdatedAt = now, now
	_, err := run.ExecContext(ctx, `
		INSERT INTO volumes (id, project_id, name, pinned_node_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		v.ID, v.ProjectID, v.Name, v.PinnedNodeID, v.CreatedAt, v.UpdatedAt)
	if state.IsUniqueViolation(err) {
		return fmt.Errorf("%w: volume %q already exists in project", state.ErrAlreadyExists, v.Name)
	}
	return err
}

// GetByName 读活跃行。
func (r *Repo) GetByName(ctx context.Context, run state.Runner, projectID, name string) (*Volume, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanVolume(row.Scan)
}

// Get 读活跃行（按 ID；anchor 解析链与 API 单体读面共用）。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Volume, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE id = ? AND deleted_at = ''`, id)
	return scanVolume(row.Scan)
}

// SoftDelete 收口删除（tombstone；活跃行口径——被引用卷由受理位前置拒绝，
// 本方法只管落账）。
func (r *Repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE volumes SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	return err
}

// Pin 钉住节点（一次性：已钉住时幂等返回；节点 ID 永不复用——不改锚）。
func (r *Repo) Pin(ctx context.Context, run state.Runner, projectID, name, nodeID string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		UPDATE volumes SET pinned_node_id = ?, updated_at = ?
		WHERE project_id = ? AND name = ? AND deleted_at = '' AND pinned_node_id = ''`,
		nodeID, now, projectID, name)
	if err != nil {
		return err
	}
	if _, err := r.GetByName(ctx, run, projectID, name); err != nil {
		return err
	}
	return nil
}

// ListByProject 返回 Project 全部活跃卷。
func (r *Repo) ListByProject(ctx context.Context, run state.Runner, projectID string) ([]Volume, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE project_id = ? AND deleted_at = '' ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Volume
	for rows.Next() {
		v, err := scanVolume(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func scanVolume(scan func(dest ...any) error) (*Volume, error) {
	var v Volume
	err := scan(&v.ID, &v.ProjectID, &v.Name, &v.PinnedNodeID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	return &v, nil
}
