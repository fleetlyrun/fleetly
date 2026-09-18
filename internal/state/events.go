package state

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/edgesets/edgefleet/internal/apperr"
	"github.com/edgesets/edgefleet/internal/eventcode"
)

// 平台事件自存（state-model §2.9）：seq 单调（SSE 游标，Outbox 模式与
// 业务写同事务）；since_seq 早于保留期 → 410 E_EVENT_CURSOR_EXPIRED +
// oldest_seq（显式断档，不静默跳号）；底座事件流只作缓存失效信号、不作
// 产品事件来源（observer.go）；secret 值禁止进入事件。

// Event 是一条平台事件。Seq 由库分配（单调、删除后永不复用）；Name 必须
// 是 eventcode 注册表内事件名（构造期纪律与 errcode 一致：非法名 panic）。
type Event struct {
	Seq     int64
	At      time.Time
	Name    string
	Subject string
	// Payload 是脱敏后的 JSON 文本（默认 "{}"）。
	Payload string
}

// AppendEvent 在事务内追加事件并返回分配的 seq：AUTOINCREMENT 保证 seq
// 严格单调且被保留期清理删除后不复用（sqlite_sequence 记录历史最大值），
// SSE 游标语义因此可依赖「seq 不回退、不断号不透明化」——断档必须经
// E_EVENT_CURSOR_EXPIRED 显式暴露。与业务写同事务 = Outbox 模式。
func (t *Tx) AppendEvent(ctx context.Context, e Event) (int64, error) {
	if _, ok := eventcode.Get(e.Name); !ok {
		panic("state: 事件名 " + e.Name + " 未在 eventcode 注册表注册（只允许注册表内事件）")
	}
	payload := e.Payload
	if payload == "" {
		payload = "{}"
	}
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	const q = `INSERT INTO events (at, name, subject, payload) VALUES (?, ?, ?, ?)`
	res, err := t.ExecContext(ctx, q, at.UnixNano(), e.Name, e.Subject, payload)
	if err != nil {
		return 0, fmt.Errorf("state: append event %s: %w", e.Name, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("state: read event seq: %w", err)
	}
	return seq, nil
}

// EventRecord 是事件只读投影。
type EventRecord = Event

// EventsSince 返回 seq > since 的事件（升序，至多 limit 条）。游标早于
// 保留窗（(since, oldest_seq) 区间的事件已被清理）时返回
// *apperr.Error{code: E_EVENT_CURSOR_EXPIRED}（HTTP 410），context 携带
// oldest_seq——显式断档，调用方（SSE/列表）应从 oldest_seq 重新拉全量。
func (s *Store) EventsSince(ctx context.Context, since int64, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	oldest, hasOldest, err := s.OldestSeq(ctx)
	if err != nil {
		return nil, err
	}
	// 保留窗校验：since 与 oldest 之间存在已清理区段（since+1 < oldest）
	// 即断档。since=0 表示「从头」，首条缺失同样是断档。
	if hasOldest && since+1 < oldest {
		return nil, cursorExpired(oldest)
	}
	const q = `SELECT seq, at, name, subject, payload FROM events
		WHERE seq > ? ORDER BY seq ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, since, limit)
	if err != nil {
		return nil, fmt.Errorf("state: query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Event, 0, limit)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate events: %w", err)
	}
	return out, nil
}

// OldestSeq 返回保留窗内最旧事件的 seq；事件表为空时返回 (0, false)。
func (s *Store) OldestSeq(ctx context.Context) (int64, bool, error) {
	const q = `SELECT MIN(seq) FROM events`
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("state: read oldest event seq: %w", err)
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Int64, true, nil
}

// cursorExpired 构造 E_EVENT_CURSOR_EXPIRED（410）应用错误：context 附
// oldest_seq，供调用方重新对齐游标。
func cursorExpired(oldest int64) *apperr.Error {
	return apperr.New("E_EVENT_CURSOR_EXPIRED",
		"事件游标早于保留期：seq ≤ %d 的事件已按保留策略清理", oldest-1).
		WithContext("oldest_seq", strconv.FormatInt(oldest, 10))
}

// scanner 抽象 *sql.Rows 的 Scan（复用于行迭代）。
type scanner interface {
	Scan(dest ...any) error
}

func scanEvent(rows scanner) (Event, error) {
	var ev Event
	var atNano int64
	if err := rows.Scan(&ev.Seq, &atNano, &ev.Name, &ev.Subject, &ev.Payload); err != nil {
		return Event{}, fmt.Errorf("state: scan event: %w", err)
	}
	ev.At = time.Unix(0, atNano).UTC()
	return ev, nil
}

// PruneExpiredEvents 删除早于 cutoff 的事件，返回清理条数（保留期清理
// job 的执行体；job 编排在 janitor.go）。
func (s *Store) PruneExpiredEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("state: prune events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read prune count: %w", err)
	}
	return n, nil
}

// PruneExpiredAudits 删除早于 cutoff 的审计记录，返回清理条数
// （审计保留期默认 1 年，state-model §2.9）。
func (s *Store) PruneExpiredAudits(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, cutoff.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("state: prune audits: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read prune count: %w", err)
	}
	return n, nil
}
