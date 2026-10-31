// Package outbox 是事件 Outbox repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/telemetry/outbox（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package outbox

import (
	"context"
	"fmt"
	"strings"
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

// ListByAggregate 返回某聚合名下 seq 之后的事件（按聚合过滤；limit 上界
// 钳制；team 语义同 ListAfter——本队 + 平台级行）。(team_id, aggregate,
// aggregate_id, seq) 索引承载（00037 迁移，B2a W1b facts 地基：诊断「近窗
// 事件」查询面）。
//
// 截断方向取**最新** limit 行（ORDER BY seq DESC 后 Go 侧反转回答升序）：
// 升序前缀截断会让持续崩溃风暴下最新事件被挤出扫描窗——越坏越看不见
// （终态计数/沿 marker 都只关心近端行）；返回仍恒升序，调用方契约不变。
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
	query += ` ORDER BY seq DESC LIMIT ?`
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// DESC 取最新 N 行后反转为升序（调用方升序契约不变：marker 判定取末条、
	// 事件面 Seq 升序合并都依赖）。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListByAggregateIDs 返回一组聚合键名下 seq 之后的事件（每键独立取最新
// limit 行——窗口函数 PARTITION BY aggregate_id ORDER BY seq DESC 圈定，
// 与 ListByAggregate 的最新 N 行截断方向单源；组内反转为升序）。空键集 =
// 空映射零 SQL。App 级诊断 facts 的账本通道（修复批 F4 扇出收口：快照内
// workload 集一次 IN 查询分组返回，替代逐 workload ListByAggregate 往返）。
func (r *repo) ListByAggregateIDs(ctx context.Context, team, aggregate string, aggregateIDs []string, afterSeq int64, limit int) (map[string][]outbox.Event, error) {
	out := map[string][]outbox.Event{}
	if len(aggregateIDs) == 0 {
		return out, nil
	}
	limit = persistence.ClampLimit(limit, 1000, 1000)
	placeholders := make([]string, 0, len(aggregateIDs))
	args := []any{aggregate}
	for _, id := range aggregateIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := `
		SELECT seq, name, aggregate, aggregate_id, payload, created_at
		FROM (
			SELECT seq, name, aggregate, aggregate_id, payload, created_at,
			       ROW_NUMBER() OVER (PARTITION BY aggregate_id ORDER BY seq DESC) AS rn
			FROM outbox WHERE aggregate = ? AND aggregate_id IN (` + strings.Join(placeholders, ",") + `) AND seq > ?`
	args = append(args, afterSeq)
	if team != "" {
		query += ` AND (team_id = ? OR team_id = '')`
		args = append(args, team)
	}
	query += `
		) WHERE rn <= ?
		ORDER BY aggregate_id, seq`
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	for rows.Next() {
		var e outbox.Event
		if err := rows.Scan(&e.Seq, &e.Name, &e.Aggregate, &e.AggregateID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out[e.AggregateID] = append(out[e.AggregateID], e)
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
