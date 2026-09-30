// Package outbox 是事件 Outbox repo（架构 §6：状态迁移事件经 Outbox 落库，
// 单调 seq；消费面 = gRPC stream + SSE + events list）。事件名必须在
// eventcode 注册表在册（三链咬合的运行时面：未注册名落库即编程错误）。
package outbox

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Event 是一条已落库事件（payload 是 JSON 字节）。
type Event struct {
	Seq         int64
	Name        string
	Aggregate   string
	AggregateID string
	Payload     []byte
	CreatedAt   string
}

// Repo 是 Outbox 存取（只增：事件是既成事实，无 UPDATE/DELETE）。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Append 落一条事件并返回其单调 seq。事件名未注册 → 明确报错（fail-fast
// 于调用方测试，而非静默落库污染订阅面）。
func (r *Repo) Append(ctx context.Context, run state.Runner, name, aggregate, aggregateID string, payload []byte) (int64, error) {
	if _, ok := eventcode.Get(name); !ok {
		return 0, fmt.Errorf("outbox: event name %q is not registered in eventcode registry", name)
	}
	res, err := run.ExecContext(ctx, `
		INSERT INTO outbox (name, aggregate, aggregate_id, payload, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		name, aggregate, aggregateID, payload, state.FormatTime(r.clock.Now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListAfter 返回 seq 之后的事件（升序；limit 上界钳制）。
func (r *Repo) ListAfter(ctx context.Context, run state.Runner, afterSeq int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := run.QueryContext(ctx, `
		SELECT seq, name, aggregate, aggregate_id, payload, created_at
		FROM outbox WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Name, &e.Aggregate, &e.AggregateID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastSeq 返回当前最大 seq（空表 = 0；快照重同步端点的基准）。
func (r *Repo) LastSeq(ctx context.Context, run state.Runner) (int64, error) {
	var seq int64
	err := run.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM outbox`).Scan(&seq)
	return seq, err
}
