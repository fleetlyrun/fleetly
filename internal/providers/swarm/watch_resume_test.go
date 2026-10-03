package swarm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// scriptedEventStreams 是 events 缝的脚本化注入（每次订阅弹出下一份流）。
// calls 只在 watchLoop goroutine 内写、消费方在 out 关闭后读（channel 关闭
// 即 happens-before），无需加锁。
type scriptedEventStreams struct {
	calls  []client.EventsListOptions
	stream []client.EventsResult
}

func (s *scriptedEventStreams) next(_ context.Context, opts client.EventsListOptions) client.EventsResult {
	s.calls = append(s.calls, opts)
	res := s.stream[0]
	s.stream = s.stream[1:]
	return res
}

// serviceEvent 是映射为 WorkloadEvent 的最小 service 事件。
func serviceEvent(wl, gen string, timeNano int64) events.Message {
	return events.Message{
		Type:     "service",
		Action:   "update",
		Actor:    events.Actor{ID: "svc-" + wl, Attributes: map[string]string{labelManaged: "true", labelWorkload: wl, labelGeneration: gen}},
		Time:     timeNano / int64(time.Second),
		TimeNano: timeNano,
	}
}

// TestWatchSubscriptionOnlyMapsSubscribedTypes（C20-1）：事件订阅只订
// mapEvent 有映射的类型——container 事件在 mapEvent 无分支（订阅即无消费
// 者：全集群最高频事件流每条白占一次循环调度再被丢弃），不得出现在过滤器
// 里。
func TestWatchSubscriptionOnlyMapsSubscribedTypes(t *testing.T) {
	closedMsg := make(chan events.Message)
	close(closedMsg)
	closedErr := make(chan error)
	close(closedErr)
	var calls []client.EventsListOptions // 仅 watchLoop goroutine 写、out 关闭后读
	p := &Provider{
		nodeList: func(context.Context) (client.NodeListResult, error) { return client.NodeListResult{}, nil },
		events: func(_ context.Context, opts client.EventsListOptions) client.EventsResult {
			calls = append(calls, opts)
			return client.EventsResult{Messages: closedMsg, Err: closedErr}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx)
	require.NoError(t, err)
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("watch loop must close the stream (existing semantics)")
	}
	require.Len(t, calls, 1)
	assert.Equal(t, map[string]bool{"service": true, "node": true},
		calls[0].Filters["type"],
		"subscription must cover exactly the mapped event types (container has no consumer)")
}

// TestWatchResumesFromEventAnchorAfterBreak（C20-2）：事件流断开重开时从
// 最后已见事件的 since 锚续传——从"现在"重放会漏掉断开窗口内的事件
// （daemon 重启窗口、网络抖动）。
func TestWatchResumesFromEventAnchorAfterBreak(t *testing.T) {
	stream1Msg := make(chan events.Message, 1)
	stream1Msg <- serviceEvent("wl_01", "2", 1700000000123456789)
	stream1Err := make(chan error) // 测试在读到流 1 事件后注入断流

	stream2Msg := make(chan events.Message, 1)
	stream2Msg <- serviceEvent("wl_02", "3", 1700000010987654321)
	stream2Err := make(chan error) // 保持开放：流 2 挂到 ctx 取消

	seams := &scriptedEventStreams{stream: []client.EventsResult{
		{Messages: stream1Msg, Err: stream1Err},
		{Messages: stream2Msg, Err: stream2Err},
	}}
	p := &Provider{
		nodeList: func(context.Context) (client.NodeListResult, error) { return client.NodeListResult{}, nil },
		events:   seams.next,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx)
	require.NoError(t, err)

	// 流 1 事件到达（锚推进到 1700000000.123456789）。
	waitEvent(t, ch, "wl_01")
	// 注入断流：watchRound 走错误分支 → 退避 → 重开。
	stream1Err <- errors.New("injected: stream break (EOF-shaped)")
	// 重开的流 2 事件照常到达（观测流自愈语义不变）。
	waitEvent(t, ch, "wl_02")
	cancel()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("watch loop must close the stream on ctx cancel (existing semantics)")
	}

	require.Len(t, seams.calls, 2)
	assert.Empty(t, seams.calls[0].Since, "fresh watch subscribes from now")
	assert.Equal(t, "1700000000.123456789", seams.calls[1].Since,
		"reopen must resume from the last seen event anchor, not from now")
	assert.Equal(t, map[string]bool{"service": true, "node": true}, seams.calls[1].Filters["type"])
}

func waitEvent(t *testing.T, ch <-chan capability.WorkloadEvent, want string) {
	t.Helper()
	select {
	case ev := <-ch:
		assert.Equal(t, want, ev.WorkloadID)
	case <-time.After(5 * time.Second):
		t.Fatalf("workload event %s missing from the observation stream", want)
	}
}
