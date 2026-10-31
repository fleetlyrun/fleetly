// Package outbox 是事件 Outbox repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/telemetry/outbox（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain outbox.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain outbox.Repo）。
func New(clock state.Clock, run state.Runner) outbox.Repo { return &repo{clock: clock, run: run} }

// Append 落一条事件并返回其单调 seq。team 是资源轴归属 Team（P0-2）：
// ” = 平台级事件（对全部 Team 可见）。事件名未注册 → 明确报错（fail-fast
// 于调用方测试，而非静默落库污染订阅面）。
func (r *repo) Append(ctx context.Context, team, name, aggregate, aggregateID string, payload []byte) (int64, error) {
	if _, ok := eventcode.Get(name); !ok {
		return 0, fmt.Errorf("outbox: event name %q is not registered in eventcode registry", name)
	}
	res, err := r.run.ExecContext(ctx, `
		INSERT INTO outbox (name, aggregate, aggregate_id, payload, created_at, team_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		name, aggregate, aggregateID, payload, state.FormatTime(r.clock.Now()), team)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAfter 返回 seq 之后的事件（升序；limit 上界钳制）。team 非空 =
// 行级过滤（team_id = team 或 ”平台级行——与 audit 面 ADR-0035 决策 6
// 同口径）；team 空 = 不过滤（平台 owner / 内部消费面，P0-2）。
func (r *repo) ListAfter(ctx context.Context, afterSeq int64, limit int, team string) ([]outbox.Event, error) {
	limit = persistence.ClampLimit(limit, 1000, 1000)
	query := `
		SELECT seq, name, aggregate, aggregate_id, payload, created_at
		FROM outbox WHERE seq > ?`
	args := []any{afterSeq}
	if team != "" {
		query += ` AND (team_id = ? OR team_id = '')`
		args = append(args, team)
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.Seq, &e.Name, &e.Aggregate, &e.AggregateID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListByAggregate 返回某聚合名下 seq 之后的事件（升序前向扫描按聚合
// 过滤；limit 上界钳制；team 语义同 ListAfter——本队 + 平台级行）。
// (team_id, aggregate, aggregate_id, seq) 索引承载（00037 迁移，B2a W1b
// facts 地基：诊断「近窗事件」查询面）。
func (r *repo) ListByAggregate(ctx context.Context, team, aggregate, aggregateID string, afterSeq int64, limit int) ([]outbox.Event, error) {
	limit = persistence.ClampLimit(limit, 1000, 1000)
	query := `
		SELECT seq, name, aggregate, aggregate_id, payload, created_at
		FROM outbox WHERE aggregate = ? AND aggregate_id = ? AND seq > ?`
	args := []any{aggregate, aggregateID, afterSeq}
	if team != "" {
		query += ` AND (team_id = ? OR team_id = '')`
		args = append(args, team)
	}
	query += ` ORDER BY seq LIMIT ?`
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []outbox.Event
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.Seq, &e.Name, &e.Aggregate, &e.AggregateID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastSeq 返回当前最大 seq（空表 = 0；快照重同步端点的基准）。
func (r *repo) LastSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := r.run.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM outbox`).Scan(&seq)
	return seq, err
}

// EarliestSeq 返回保留窗内最早事件 seq（空表 = 0）。断档判定锚：订阅方
// after_seq < earliest 即落窗外（410 + 快照重同步，ADR-0026）。
func (r *repo) EarliestSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := r.run.QueryRowContext(ctx, `SELECT COALESCE(MIN(seq), 0) FROM outbox`).Scan(&seq)
	return seq, err
}

// TrimBefore 删除保留窗外的行（janitor 周期执行；返回清理行数供观测）。
// 事件既成事实落审计后由本面回收——"只增"修订为"保留窗内只增"（ADR-0026：
// 订阅契约以 earliest_seq 划界，窗内不删不改）。
//
// 裁剪必须是 seq 前缀（R05-M4）：EarliestSeq = MIN(seq)，410 判定只看
// after_seq < earliest——只有"被删集合 = {seq <= N}"时该判定才等价于
// "游标落在窗外"。按 created_at 直接 DELETE 会在墙钟跳变（VM 快照/错误
// RTC/NTP 大步校正）下删掉中间行：旧行之后的未来行幸存 → earliest 不前移
// → 订阅方静默跳过被删事件。故先取"窗外行中的最大 seq"，再删所有 seq <=
// 它的行（窗外行可多删，但结果必为前缀）。
func (r *repo) TrimBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.run.ExecContext(ctx,
		`DELETE FROM outbox WHERE seq <= COALESCE((SELECT MAX(seq) FROM outbox WHERE created_at < ?), 0)`,
		state.FormatTime(cutoff))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
