// Package volume 是 Volume 聚合 repo 的 SQLite 实现：行类型与 Repo 接口
// 住 domain/structure/volume（ADR-0075 Repo 接口层），本包只持不导出实现
// 与构造。
package volume

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain volume.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain volume.Repo）。
func New(clock state.Clock, run state.Runner) volume.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
// pinnedNodeID 为空表示待钉住（首次挂载时由引擎分配——分配后不可变）。
func (r *repo) Create(ctx context.Context, v *volume.Volume) error {
	seq, err := persistence.NextSeq(ctx, r.run, "volumes")
	if err != nil {
		return err
	}
	v.Seq = seq
	if v.ID == "" {
		v.ID = entityid.Join(v.Name, seq)
	}
	now := state.FormatTime(r.clock.Now())
	v.CreatedAt, v.UpdatedAt = now, now
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO volumes (id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')`,
		v.ID, v.Seq, v.ProjectID, v.Name, v.PinnedNodeID, v.CreatedAt, v.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: volume %q already exists in project", state.ErrAlreadyExists, v.Name)
	}
	return err
}

// GetByName 读活跃行。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*volume.Volume, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanVolume(row.Scan)
}

// Get 读活跃行（按 ID；anchor 解析链与 API 单体读面共用）。
func (r *repo) Get(ctx context.Context, id string) (*volume.Volume, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE id = ? AND deleted_at = ''`, id)
	return scanVolume(row.Scan)
}

// SoftDelete 收口删除（tombstone；活跃行口径——被引用卷由受理位前置拒绝，
// 本方法只管落账）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE volumes SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	return err
}

// Pin 钉住节点（一次性：已钉住时幂等返回；节点 ID 永不复用——不改锚）。
func (r *repo) Pin(ctx context.Context, projectID, name, nodeID string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE volumes SET pinned_node_id = ?, updated_at = ?
		WHERE project_id = ? AND name = ? AND deleted_at = '' AND pinned_node_id = ''`,
		nodeID, now, projectID, name)
	if err != nil {
		return err
	}
	if _, err := r.GetByName(ctx, projectID, name); err != nil {
		return err
	}
	return nil
}

// ListByProject 返回 Project 全部活跃卷。
func (r *repo) ListByProject(ctx context.Context, projectID string) ([]volume.Volume, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at
		FROM volumes WHERE project_id = ? AND deleted_at = '' ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []volume.Volume
	for rows.Next() {
		v, err := scanVolume(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

func scanVolume(scan func(dest ...any) error) (*volume.Volume, error) {
	var v volume.Volume
	err := scan(&v.ID, &v.Seq, &v.ProjectID, &v.Name, &v.PinnedNodeID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &v, nil
}
