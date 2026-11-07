// Package stream 是 Stream 子资源 repo 的 SQLite 实现：行类型与 Repo 接口
// 住 domain/data/stream（ADR-0075 Repo 接口层；persistence/topic 同形范本），
// 本包只持不导出实现与构造。表名 streams（migration 00042）。
package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain stream.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain stream.Repo）。
func New(clock state.Clock, run state.Runner) stream.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同父实例内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, s *stream.Stream) error {
	seq, err := persistence.NextSeq(ctx, r.run, "streams")
	if err != nil {
		return err
	}
	s.Seq = seq
	if s.ID == "" {
		s.ID = entityid.Mint(r.clock.Now())
	}
	subjects, err := marshalSubjects(s.Subjects)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	s.CreatedAt, s.UpdatedAt, s.Status = now, now, stream.StatusPending
	s.StatusErr = ""
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO streams (id, seq, project_id, pubsub_id, name, subjects, retention, storage, max_bytes,
			status, status_err, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, '')`,
		s.ID, s.Seq, s.ProjectID, s.PubSubID, s.Name, subjects, s.Retention, s.Storage, s.MaxBytes,
		s.Status, s.CreatedAt, s.UpdatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: stream name %q already exists on the pubsub instance", state.ErrAlreadyExists, s.Name)
	}
	return err
}

// Get 按 ID 读行（ADR-0023 统一口径：tombstone 后一律不存在）。
func (r *repo) Get(ctx context.Context, id string) (*stream.Stream, error) {
	row := r.run.QueryRowContext(ctx, selectCols+` WHERE id = ? AND deleted_at = ''`, id)
	return scanStream(row.Scan)
}

// ListByPubSub 返回父实例下全部行（含 tombstone——ensure 面 + GC 面共用）。
func (r *repo) ListByPubSub(ctx context.Context, pubsubID string) ([]stream.Stream, error) {
	return r.query(ctx, selectCols+` WHERE pubsub_id = ? ORDER BY seq`, pubsubID)
}

// CountByPubSub 返回父实例下活跃行数（per 实例配额口径：streams 100）。
func (r *repo) CountByPubSub(ctx context.Context, pubsubID string) (int, error) {
	var n int
	err := r.run.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM streams WHERE pubsub_id = ? AND deleted_at = ''`, pubsubID).Scan(&n)
	if err != nil {
		return 0, persistence.MapScanErr(err)
	}
	return n, nil
}

// UpdateDesired 落期望态面（可变面 subjects/max_bytes 全量替换；不可变轴
// retention/storage 不承载——受理位拒）。
func (r *repo) UpdateDesired(ctx context.Context, id string, subjects []string, maxBytes *int64) error {
	column, err := marshalSubjects(subjects)
	if err != nil {
		return err
	}
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE streams SET subjects = ?, max_bytes = ?, status = ?, status_err = '', updated_at = ?
		WHERE id = ? AND deleted_at = ''`, column, maxBytes, stream.StatusPending, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update stream %s: %w", id, state.ErrNotFound)
	}
	return nil
}

// SetStatus 落对账状态（同值写入不迁 updated_at；命中 0 行静默返回）。
func (r *repo) SetStatus(ctx context.Context, id, status, statusErr string) error {
	now := state.FormatTime(r.clock.Now())
	_, err := r.run.ExecContext(ctx, `
		UPDATE streams SET status = ?, status_err = ?, updated_at = ?
		WHERE id = ? AND deleted_at = '' AND (status != ? OR status_err != ?)`,
		status, statusErr, now, id, status, statusErr)
	return err
}

// SoftDelete 落 tombstone（= 声明式 GC 挂起；幂等口径与 Get 一致）。
func (r *repo) SoftDelete(ctx context.Context, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := r.run.ExecContext(ctx, `
		UPDATE streams SET deleted_at = ?, updated_at = ?
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
	_, err := r.run.ExecContext(ctx, `DELETE FROM streams WHERE id = ?`, id)
	return err
}

// DeleteByPubSub 物理删父实例下全部行（父实例删除的级联收口；幂等）。
func (r *repo) DeleteByPubSub(ctx context.Context, pubsubID string) error {
	_, err := r.run.ExecContext(ctx, `DELETE FROM streams WHERE pubsub_id = ?`, pubsubID)
	return err
}

const selectCols = `
	SELECT id, seq, project_id, pubsub_id, name, subjects, retention, storage, max_bytes,
		status, status_err, created_at, updated_at, deleted_at
	FROM streams`

func (r *repo) query(ctx context.Context, q string, args ...any) ([]stream.Stream, error) {
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []stream.Stream
	for rows.Next() {
		s, err := scanStream(rows.Scan)
		if err != nil {
			return nil, persistence.MapScanErr(err)
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanStream(scan func(dest ...any) error) (*stream.Stream, error) {
	var s stream.Stream
	var subjects string
	err := scan(&s.ID, &s.Seq, &s.ProjectID, &s.PubSubID, &s.Name, &subjects, &s.Retention, &s.Storage, &s.MaxBytes,
		&s.Status, &s.StatusErr, &s.CreatedAt, &s.UpdatedAt, &s.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	s.Subjects = unmarshalSubjects(subjects)
	return &s, nil
}

// marshalSubjects 把主题过滤器集序列化为升序 JSON 数组（确定性序列化纪律
// ——同集恒同字节）。
func marshalSubjects(subjects []string) (string, error) {
	if len(subjects) == 0 {
		return "[]", nil
	}
	sorted := append([]string(nil), subjects...)
	sort.Strings(sorted)
	b, err := json.Marshal(sorted)
	if err != nil {
		return "", fmt.Errorf("marshal stream subjects: %w", err)
	}
	return string(b), nil
}

// unmarshalSubjects 反序列化主题过滤器列（宽容读：空/畸形态落 nil）。
func unmarshalSubjects(column string) []string {
	if column == "" || column == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(column), &out); err != nil {
		return nil
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
