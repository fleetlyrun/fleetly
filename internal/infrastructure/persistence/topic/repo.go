// Package topic 是 Topic 子资源 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/data/topic（ADR-0075 Repo 接口层；persistence/pubsub 同形范本），
// 本包只持不导出实现与构造。表名 topics（migration 00042）。
package topic

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain topic.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain topic.Repo）。
func New(clock state.Clock, run state.Runner) topic.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, t *topic.Topic) error {
	seq, err := persistence.NextSeq(ctx, r.run, "topics")
	if err != nil {
		return err
	}
	t.Seq = seq
	if t.ID == "" {
		t.ID = entityid.Mint(r.clock.Now())
	}
	now := state.FormatTime(r.clock.Now())
	t.CreatedAt, t.UpdatedAt, t.Status = now, now, topic.StatusPending
	t.StatusError = ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO topics (id, seq, project_id, pubsub_id, name, partitions, retention_ms,
			status, status_error, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, '')`,
		t.ID, t.Seq, t.ProjectID, t.PubSubID, t.Name, t.Partitions, t.RetentionMS,
		t.Status, t.CreatedAt, t.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: topic name %q already exists on the pubsub instance", state.ErrAlreadyExists, t.Name)
	}
	return err
}

// Get 按 ID 读行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*topic.Topic, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanTopic(row.Scan)
}

// ListByPubSub 返回父实例下全部行（含 tombstone——ensure 面 + GC 面共用）。
func (r *repo) ListByPubSub(ctx context.Context, pubsubID string) ([]topic.Topic, error) {
	return r.query(ctx, selectCols+` WHERE pubsub_id = ? ORDER BY seq`, pubsubID)
}

// CountByPubSub 返回父实例下活跃行数（per 实例配额口径：topics 100）。
func (r *repo) CountByPubSub(ctx context.Context, pubsubID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM topics WHERE pubsub_id = ? AND deleted_at = ''`, pubsubID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// UpdateDesired 落期望态面（partitions/retention 全量替换）。
func (r *repo) UpdateDesired(ctx context.Context, id string, partitions int32, retentionMS *int64) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE topics SET partitions = ?, retention_ms = ?, status = ?, status_error = '', updated_at = ?
		WHERE id = ? AND deleted_at = ''`, partitions, retentionMS, topic.StatusPending, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update topic %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SetStatus 落对账状态（同值写入不迁 updated_at；命中 0 行静默返回）。
func (r *repo) SetStatus(ctx context.Context, id, status, statusError string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE topics SET status = ?, status_error = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND (status != ? OR status_error != ?)`,
		status, statusError, now, id, status, statusError)
	return err
}

// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE topics SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at = ''`, now, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Delete 物理删行（收敛段 GC 成功路径；幂等：不存在静默成功）。
func (r *repo) Delete(ctx context.Context, id string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM topics WHERE id = ?`, id)
	return err
}

// DeleteByPubSub 物理删父实例下全部行（父实例删除的级联收口；幂等）。
func (r *repo) DeleteByPubSub(ctx context.Context, pubsubID string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM topics WHERE pubsub_id = ?`, pubsubID)
	return err
}

const selectCols = `
	SELECT id, seq, project_id, pubsub_id, name, partitions, retention_ms,
		status, status_error, created_at, updated_at, deleted_at
	FROM topics`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]topic.Topic, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []topic.Topic
	for rows.Next() {
		t, err := scanTopic(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanTopic(scan func(dest ...any) error) (*topic.Topic, error) {
	var t topic.Topic
	err := scan(&t.ID, &t.Seq, &t.ProjectID, &t.PubSubID, &t.Name, &t.Partitions, &t.RetentionMS,
		&t.Status, &t.StatusError, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &t, nil
}
