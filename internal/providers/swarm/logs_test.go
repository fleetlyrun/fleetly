package swarm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// fakeLogStream 是服务日志流的 fake（不依赖真 docker）：lines 逐行发放
// （每行封装为 stdcopy stdout 帧——真 daemon 的服务日志流是 stdcopy 解
// 复用形态，ADR-0040），关闭后 EOF（非 Follow 形态）；lines 保持打开则
// 读端挂住直至 ctx 取消（Follow 形态——等价真实现"请求 ctx 取消即中断
// 响应体"的解除语义，这是 openServiceLog 缝的契约）。
type fakeLogStream struct {
	ctx   context.Context
	lines <-chan string
	carry []byte
}

// stdcopyFrame 封装一个 stdcopy 帧（[stream(1)][0×3][size(4 BE)]，stdout=1
// ——docker api/pkg/stdcopy 头格式）。
func stdcopyFrame(payload string) []byte {
	h := make([]byte, 8, 8+len(payload))
	h[0] = 1
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload))) //nolint:gosec // G115 测试夹具：payload 是本文件短行，域内恒安全
	return append(h, payload...)
}

func (f *fakeLogStream) Read(p []byte) (int, error) {
	for len(f.carry) == 0 {
		select {
		case line, ok := <-f.lines:
			if !ok {
				return 0, io.EOF
			}
			f.carry = stdcopyFrame(line)
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

// countByTask 按 task ID 统计帧数（集群面帧身份锚 = swarm task ID）。
func (c *frameCollector) countByTask() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := map[string]int{}
	for _, f := range c.frames {
		counts[f.Container]++
	}
	return counts
}

// svcLogLine 生成服务日志行（Timestamps+Details 形态："<RFC3339> <归因
// 属性> <消息>"——daemon write_log_stream 编码）。
func svcLogLine(nodeID, taskID string, seq int) string {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	details := fmt.Sprintf("com.docker.swarm.node.id=%s,com.docker.swarm.service.id=svc-x,com.docker.swarm.task.id=%s", nodeID, taskID)
	return fmt.Sprintf("%s %s hello from %s #%d", ts, details, taskID, seq)
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

// fanInFixture 是双服务日志缝夹具：listServices/openServiceLog 注入 fake，
// opened 通知某服务流已打开，feed 是测试侧的行注入通道。服务标记携带
// per-Workload 锚（发现 A 修复后的过滤面）。
type fanInFixture struct {
	p      *Provider
	feed   map[string]chan string
	opened chan string
	mu     sync.Mutex
	badOpt []string // 记录 Follow/Details 旗标未按契约透传的服务
}

func newFanInFixture(q capability.LogQuery) *fanInFixture {
	const svc1, svc2 = "svc-web-1", "svc-web-2"
	mk := func(id, workload string) swarm.Service {
		svc := swarm.Service{ID: id}
		svc.Spec.Labels = map[string]string{labelWorkload: workload}
		return svc
	}
	services := []swarm.Service{mk(svc1, "wl_01H"), mk(svc2, "wl_02H")}
	f := &fanInFixture{
		feed:   map[string]chan string{svc1: make(chan string), svc2: make(chan string)},
		opened: make(chan string, 2),
	}
	f.p = &Provider{
		listServices: func(context.Context, capability.NamespaceRef) ([]swarm.Service, error) {
			return services, nil
		},
		openServiceLog: func(ctx context.Context, id string, opts client.ServiceLogsOptions) (io.ReadCloser, error) {
			if opts.Follow != q.Follow || !opts.Details || !opts.Timestamps {
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

// assertFollowPassthrough 断言 Follow/Details/Timestamps 旗标契约（N0.1
// P2-12 回归面 + ADR-0040 归因依赖）。
func (f *fanInFixture) assertFollowPassthrough(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Empty(t, f.badOpt, "Follow/Details/Timestamps must pass through to the service log stream")
}

// TestStreamLogsFollowFanInBothServices Q-4/P1-15 回归（集群面形态）：
// Follow 多服务合流——两服务帧都到达，ctx 取消后 StreamLogs 有界返回
// nil 且全部 goroutine 回收。帧身份锚 = task ID + 节点归因（details）。
func TestStreamLogsFollowFanInBothServices(t *testing.T) {
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

	// 两流都打开后各发 2 帧（交错注入，模拟双服务——跨节点——并发产日志）。
	for i := 0; i < 2; i++ {
		id := <-f.opened
		var node, task string
		if id == "svc-web-1" {
			node, task = "node-a", "task-1a"
		} else {
			node, task = "node-b", "task-2b"
		}
		f.feed[id] <- svcLogLine(node, task, 1)
		f.feed[id] <- svcLogLine(node, task, 2)
	}

	waitFor(t, "frames from both services", func() bool {
		counts := c.countByTask()
		return counts["task-1a"] >= 2 && counts["task-2b"] >= 2
	})
	f.assertFollowPassthrough(t)

	// 帧的身份锚正确：task 归因（details）+ 服务标记 → 平台 Workload ID。
	for _, frame := range c.snapshot() {
		switch frame.Container {
		case "task-1a":
			assert.Equal(t, "wl_01H", frame.WorkloadID)
			assert.Equal(t, "node-a", frame.Node)
			assert.Contains(t, string(frame.Line), "hello from task-1a")
			assert.False(t, frame.Time.IsZero(), "frame timestamp from the line prefix")
		case "task-2b":
			assert.Equal(t, "wl_02H", frame.WorkloadID)
			assert.Equal(t, "node-b", frame.Node)
		default:
			t.Fatalf("unexpected task %q in merged stream", frame.Container)
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

// TestStreamLogsWorkloadFilter：WorkloadID 过滤在服务标记面（发现 A 修复
// 后的过滤层——服务级全等，不逐容器）。
func TestStreamLogsWorkloadFilter(t *testing.T) {
	q := capability.LogQuery{
		Namespace:  capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"},
		WorkloadID: "wl_01H",
	}
	f := newFanInFixture(q)
	c := &frameCollector{}

	done := make(chan error, 1)
	go func() { done <- f.p.StreamLogs(context.Background(), q, c) }()
	// 只开 wl_01H 的流（另一服务被过滤，opened 恒只收一个）。
	id := <-f.opened
	assert.Equal(t, "svc-web-1", id, "workload filter must narrow to the matching service")
	f.feed[id] <- svcLogLine("node-a", "task-1a", 1)
	close(f.feed[id])
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("filtered non-follow stream did not terminate at EOF")
	}
	assert.Equal(t, 1, c.countByTask()["task-1a"])
}

// TestStreamLogsNoFollowCompletesAcrossServices：非 Follow 与 Follow
// 统一走同一合流路径——各流 EOF 后 StreamLogs 自然收尾返回 nil，双服务
// 帧全部到达且帧数封口（EOF 后不再产帧）。
func TestStreamLogsNoFollowCompletesAcrossServices(t *testing.T) {
	q := capability.LogQuery{
		Namespace: capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"},
		Follow:    false,
	}
	f := newFanInFixture(q)
	c := &frameCollector{}

	done := make(chan error, 1)
	go func() {
		// 各服务 3 帧后关流（EOF = 非 Follow 流的自然终点）。
		for i := 0; i < 2; i++ {
			id := <-f.opened
			var node, task string
			if id == "svc-web-1" {
				node, task = "node-a", "task-1a"
			} else {
				node, task = "node-b", "task-2b"
			}
			for seq := 1; seq <= 3; seq++ {
				f.feed[id] <- svcLogLine(node, task, seq)
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

	counts := c.countByTask()
	assert.Equal(t, 3, counts["task-1a"], "all frames from service 1 must arrive")
	assert.Equal(t, 3, counts["task-2b"], "all frames from service 2 must arrive")
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
		f.feed[id] <- svcLogLine("node-a", "task-1a", 1)
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

// TestParseServiceLogLine 钉行解析契约：完整形态归因齐备；消息体含空格
// 不拆；转义值（逗号/等号/空格）往返；畸形行（缺段/坏时间戳/坏 details）
// 按无归因原始行发帧。
func TestParseServiceLogLine(t *testing.T) {
	ts := "2026-10-04T12:34:56.789Z"
	f := parseServiceLogLine("wl_01H", []byte(ts+" com.docker.swarm.node.id=node-a,com.docker.swarm.service.id=svc1,com.docker.swarm.task.id=task9 msg with spaces here"))
	assert.Equal(t, "wl_01H", f.WorkloadID)
	assert.Equal(t, "node-a", f.Node)
	assert.Equal(t, "task9", f.Container)
	assert.Equal(t, "msg with spaces here", string(f.Line))
	got, err := time.Parse(time.RFC3339Nano, ts)
	require.NoError(t, err)
	assert.Equal(t, got, f.Time)

	// 畸形形态：无 details 的原始行（丢归因不丢行）。
	f = parseServiceLogLine("wl", []byte("raw payload"))
	assert.Equal(t, "raw payload", string(f.Line))
	assert.Empty(t, f.Container)
	assert.True(t, f.Time.IsZero())
}

// TestParseLogDetails 钉 details 编解码（URL query 转义往返——docker/cli
// internal/logdetails 同款语义）。
func TestParseLogDetails(t *testing.T) {
	d, err := parseLogDetails("key=value,com.docker.swarm.node.id=node-a")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key": "value", "com.docker.swarm.node.id": "node-a"}, d)

	// 值内转义的逗号/等号/空格。
	d, err = parseLogDetails("key+with+spaces=value%3Dequals,asdf%2C=")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"key with spaces": "value=equals", "asdf,": ""}, d)

	// 畸形：缺等号/空键拒绝。
	_, err = parseLogDetails("novalue")
	require.Error(t, err)
	_, err = parseLogDetails("=nothing")
	require.Error(t, err)
}
