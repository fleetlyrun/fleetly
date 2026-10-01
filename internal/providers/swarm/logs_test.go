package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeLogStream 是容器日志流的 fake（不依赖真 docker）：lines 逐行发放，
// 关闭后 EOF（非 Follow 形态）；lines 保持打开则读端挂住直至 ctx 取消
// （Follow 形态——等价真实现"请求 ctx 取消即中断响应体"的解除语义，
// 这是 openContainerLog 缝的契约）。
type fakeLogStream struct {
	ctx   context.Context
	lines <-chan string
	carry []byte
}

func (f *fakeLogStream) Read(p []byte) (int, error) {
	for len(f.carry) == 0 {
		select {
		case line, ok := <-f.lines:
			if !ok {
				return 0, io.EOF
			}
			f.carry = []byte(line + "\n")
		case <-f.ctx.Done():
			return 0, f.ctx.Err()
		}
	}
	n := copy(p, f.carry)
	f.carry = f.carry[n:]
	return n, nil
}

func (f *fakeLogStream) Close() error { return nil }

// frameCollector 收集合流帧（合流路径的帧来自多读端交错，必须并发安全）。
type frameCollector struct {
	mu     sync.Mutex
	frames []capability.LogFrame
	// failAt 非零时第 failAt 帧起写端报错（写端失败路径注入）。
	failAt int
	err    error
}

func (c *frameCollector) WriteLog(_ context.Context, f capability.LogFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failAt != 0 && len(c.frames) >= c.failAt {
		return c.err
	}
	c.frames = append(c.frames, f)
	return nil
}

func (c *frameCollector) snapshot() []capability.LogFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]capability.LogFrame, len(c.frames))
	copy(out, c.frames)
	return out
}

// countByContainer 按容器 ID 统计帧数。
func (c *frameCollector) countByContainer() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := map[string]int{}
	for _, f := range c.frames {
		counts[f.Container]++
	}
	return counts
}

// tsLine 生成带 RFC3339 时间戳前缀的日志行（docker timestamps 形态）。
func tsLine(containerID string, seq int) string {
	return time.Now().UTC().Format(time.RFC3339Nano) + fmt.Sprintf(" hello from %s #%d", containerID, seq)
}

// waitFor 轮询直至 cond 为真或超时（自带实现，不用 testify.Eventually：
// 其内部逐拍起协程，会污染随后的 goroutine 计数断言）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fanInFixture 是双容器日志缝夹具：listContainers/openContainerLog 注入
// fake，opened 通知某容器流已打开，feed 是测试侧的行注入通道。
type fanInFixture struct {
	p      *Provider
	feed   map[string]chan string
	opened chan string
	mu     sync.Mutex
	badOpt []string // 记录 Follow 旗标未按查询透传的容器
}

func newFanInFixture(q capability.LogQuery) *fanInFixture {
	const cid1, cid2 = "ctr-web-1", "ctr-web-2"
	containers := []container.Summary{
		{ID: cid1, Labels: map[string]string{labelWorkload: "wl_01H", "com.docker.swarm.node.id": "node-a"}},
		{ID: cid2, Labels: map[string]string{labelWorkload: "wl_02H", "com.docker.swarm.node.id": "node-b"}},
	}
	f := &fanInFixture{
		feed:   map[string]chan string{cid1: make(chan string), cid2: make(chan string)},
		opened: make(chan string, 2),
	}
	f.p = &Provider{
		listContainers: func(context.Context, capability.NamespaceRef) ([]container.Summary, error) {
			return containers, nil
		},
		openContainerLog: func(ctx context.Context, id string, opts client.ContainerLogsOptions) (io.ReadCloser, error) {
			if opts.Follow != q.Follow {
				f.mu.Lock()
				f.badOpt = append(f.badOpt, id)
				f.mu.Unlock()
			}
			f.opened <- id
			return &fakeLogStream{ctx: ctx, lines: f.feed[id]}, nil
		},
	}
	return f
}

// assertFollowPassthrough 断言 Follow 旗标透传无回退（N0.1 P2-12 回归面）。
func (f *fanInFixture) assertFollowPassthrough(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Empty(t, f.badOpt, "Follow flag must pass through to the container log stream")
}

// TestStreamLogsFollowFanInBothContainers Q-4/P1-15 回归：Follow 多容器
// 合流——两容器帧都到达（旧实现串行跟随只读首容器，第二容器永不输出），
// ctx 取消后 StreamLogs 有界返回 nil 且全部 goroutine 回收。
func TestStreamLogsFollowFanInBothContainers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"},
		Follow:    true,
	}
	f := newFanInFixture(q)
	c := &frameCollector{}

	base := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() { done <- f.p.StreamLogs(ctx, q, c) }()

	// 两流都打开后各发 2 帧（交错注入，模拟双容器并发产日志）。
	for i := 0; i < 2; i++ {
		id := <-f.opened
		f.feed[id] <- tsLine(id, 1)
		f.feed[id] <- tsLine(id, 2)
	}

	waitFor(t, "frames from both containers", func() bool {
		counts := c.countByContainer()
		return counts["ctr-web-1"] >= 2 && counts["ctr-web-2"] >= 2
	})
	f.assertFollowPassthrough(t)

	// 帧的身份锚正确：容器 → 平台 Workload ID / 节点标记还原。
	for _, frame := range c.snapshot() {
		switch frame.Container {
		case "ctr-web-1":
			assert.Equal(t, "wl_01H", frame.WorkloadID)
			assert.Equal(t, "node-a", frame.Node)
		case "ctr-web-2":
			assert.Equal(t, "wl_02H", frame.WorkloadID)
			assert.Equal(t, "node-b", frame.Node)
		default:
			t.Fatalf("unexpected container %q in merged stream", frame.Container)
		}
	}

	// ctx 取消：跟随流干净收口，StreamLogs 返回 nil。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("StreamLogs did not return after ctx cancel")
	}

	// goroutine 回收：读端×2 + 关闭协程 + StreamLogs 主体全部退出
	//（Q-4 验收：取消后无泄漏）。
	waitFor(t, "goroutines reclaimed after cancel", func() bool {
		return runtime.NumGoroutine() <= base
	})
}

// TestStreamLogsNoFollowCompletesAcrossContainers：非 Follow 与 Follow
// 统一走同一合流路径——各流 EOF 后 StreamLogs 自然收尾返回 nil，双容器
// 帧全部到达且帧数封口（EOF 后不再产帧）。
func TestStreamLogsNoFollowCompletesAcrossContainers(t *testing.T) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"},
		Follow:    false,
	}
	f := newFanInFixture(q)
	c := &frameCollector{}

	done := make(chan error, 1)
	go func() {
		// 各容器 3 帧后关流（EOF = 非 Follow 流的自然终点）。
		for i := 0; i < 2; i++ {
			id := <-f.opened
			for seq := 1; seq <= 3; seq++ {
				f.feed[id] <- tsLine(id, seq)
			}
			close(f.feed[id])
		}
	}()
	go func() { done <- f.p.StreamLogs(context.Background(), q, c) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("non-follow StreamLogs did not terminate at EOF")
	}

	counts := c.countByContainer()
	assert.Equal(t, 3, counts["ctr-web-1"], "all frames from container 1 must arrive")
	assert.Equal(t, 3, counts["ctr-web-2"], "all frames from container 2 must arrive")
	f.assertFollowPassthrough(t)
}

// TestStreamLogsWriterErrorReclaimsReaders：写端失败路径——取消读端并
// 排干合流通道后返回写端错误，goroutine 全部回收（无泄漏孤儿帧路径）。
func TestStreamLogsWriterErrorReclaimsReaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"},
		Follow:    true,
	}
	f := newFanInFixture(q)
	injected := errors.New("client stream went away")
	c := &frameCollector{failAt: 1, err: injected}

	base := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() { done <- f.p.StreamLogs(ctx, q, c) }()

	// 首帧即触发写端失败；两流都打开并各注入一帧保证读端在飞行中。
	for i := 0; i < 2; i++ {
		id := <-f.opened
		f.feed[id] <- tsLine(id, 1)
	}

	select {
	case err := <-done:
		require.ErrorIs(t, err, injected)
	case <-time.After(10 * time.Second):
		t.Fatal("StreamLogs did not return after writer failure")
	}

	waitFor(t, "goroutines reclaimed after writer failure", func() bool {
		return runtime.NumGoroutine() <= base
	})
}
