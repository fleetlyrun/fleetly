package grpcapi

import (
	"context"
	"errors"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

// ErrWaitDone 是 send 侧的收流哨兵：终态帧已发，订阅循环应结束且对外
// 不是错误。
var ErrWaitDone = errors.New("wait: terminal frame delivered")

// WaitSource 是等待原语的事件源端口（outbox 游标 + Team 过滤 + 订阅核心）。
type WaitSource struct {
	LastSeq    func(ctx context.Context) (int64, error)
	TeamFilter func(ctx context.Context) (string, error)
	Subscribe  func(ctx context.Context, team string, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error
}

// WaitOnRow 是「读行 → 状态未变不重发 → 发帧 → 终态帧后收流」的唯一循环。
func WaitOnRow[Row any](ctx context.Context, src WaitSource, aggregate, id string,
	read func(context.Context) (Row, error),
	stateOf func(Row) string,
	terminal func(Row) bool,
	sendFrame func(Row) error,
) error {
	last := ""
	send := func() error {
		row, err := read(ctx)
		if err != nil {
			return err
		}
		if stateOf(row) == last {
			return nil
		}
		last = stateOf(row)
		if err := sendFrame(row); err != nil {
			return err
		}
		if terminal(row) {
			return ErrWaitDone
		}
		return nil
	}
	return WaitOnAggregate(ctx, src, aggregate, id, send)
}

// WaitOnAggregate：首帧（当前行快照）→ 从订阅起点跟随 aggregate/id 命中
// 重读行发帧。订阅起点先于行读：两步间落下的事件只多触发一次幂等重读。
func WaitOnAggregate(ctx context.Context, src WaitSource, aggregate, id string, send func() error) error {
	cursor, err := src.LastSeq(ctx)
	if err != nil {
		return err
	}
	team, terr := src.TeamFilter(ctx)
	if terr != nil {
		return terr
	}
	if err := send(); err != nil {
		if errors.Is(err, ErrWaitDone) {
			return nil
		}
		return err
	}
	err = src.Subscribe(ctx, team, cursor, true, func(ev *telemetryv1.Event) error {
		if ev.Aggregate != aggregate || ev.AggregateId != id {
			return nil
		}
		return send()
	})
	if errors.Is(err, ErrWaitDone) {
		return nil
	}
	return err
}
