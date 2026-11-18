package grpcapi

import (
	"context"
	"errors"
	"time"

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
//
// 节拍唤醒（waitTicker，2026-11-18）：事件不是唯一信号——零状态类事件的
// 聚合（Function 的 status 是观测缓存非状态机，ADR-0102 设计 §4.1/§10）
// 在受理事件之后再无事件可期，纯订阅唤醒会永挂。ticker 是"又一个信号"，
// 行重读仍为唯一真源；帧去重归 send 闭包（状态键未变跳过）。事件命中与
// 节拍并发安全：Subscribe 回调串行收事件，ticker 经 select 单飞（同一时刻
// 至多一个 send 在跑）。
const waitTicker = 2 * time.Second

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
	tick := time.NewTicker(waitTicker)
	defer tick.Stop()
	type signal struct{}
	events := make(chan signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- src.Subscribe(ctx, team, cursor, true, func(ev *telemetryv1.Event) error {
			if ev.Aggregate != aggregate || ev.AggregateId != id {
				return nil
			}
			select {
			case events <- signal{}:
				return nil
			default:
				return nil // 前一个信号未消费：事件幂等合并（重读是全量快照）
			}
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return <-done // 订阅随 ctx 取消收流；返回其终值（ErrWaitDone 归 nil）
		case err := <-done:
			if errors.Is(err, ErrWaitDone) {
				return nil
			}
			return err
		case <-tick.C:
		case <-events:
		}
		if err := send(); err != nil {
			if errors.Is(err, ErrWaitDone) {
				return nil
			}
			return err
		}
	}
}
