package build

// solve 日志消费契约测试（H5，MG-1）：writeSolveLog 在 ctx 取消后必须
// 丢弃式排空 statusCh 直到关闭——buildkit 客户端的状态泵是无 ctx 保护的
// 阻塞发送（moby/buildkit v0.32.2 client/solve.go:393-394），弃读即泵
// 死锁、Solve 永不返回（并发槽永久占用）。timeout-guard 模式与
// internal/logs TestHubReplayBacklogBeyondBuffer 同款。

import (
	"context"
	"io"
	"testing"
	"time"

	bkclient "github.com/moby/buildkit/client"
)

// TestWriteSolveLogDrainsStatusChannelUntilClose 契约两段断言：①cancel 后、
// ch 关闭前 writeSolveLog 不得返回（旧形态 ctx.Done 即 return，会把泵
// 永久留在阻塞发送上）；②积压超过缓冲（8）的条目在 close 后全部排空并
// 返回（排空路径不阻塞于积压）。
func TestWriteSolveLogDrainsStatusChannelUntilClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan *bkclient.SolveStatus, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeSolveLog(ctx, ch, io.Discard)
	}()
	// 给渲染 goroutine 一点时间进入 select（避免 cancel 先于启动的竞态
	// 让断言一测不到旧形态）。
	time.Sleep(50 * time.Millisecond)

	cancel()
	// 断言一：cancel 后、ch 关闭前仍在排空等待——旧形态毫秒级即返回。
	select {
	case <-done:
		t.Fatal("writeSolveLog returned before ch was closed (ctx.Done branch does not drain statusCh — H5: the buildkit status pump would block forever)")
	case <-time.After(200 * time.Millisecond):
	}

	// 断言二：灌入超过缓冲容量的条目再 close——close 后必须返回。
	go func() {
		for i := 0; i < 50; i++ {
			ch <- &bkclient.SolveStatus{}
		}
		close(ch)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writeSolveLog did not return after ch was closed (drain path blocked — H5)")
	}
}
