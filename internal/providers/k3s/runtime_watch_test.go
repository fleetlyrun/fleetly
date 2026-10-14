package k3s

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// syncBuffer 是 slog 捕获面的并发安全缓冲（watchLoop 与 podWatchStream 两
// 个 goroutine 都会写日志，-race 下裸 bytes.Buffer 会炸；包内测试不并行，
// 全局默认 logger 的换入/还原安全）。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs 把默认 slog 换成缓冲 handler（watch 韧性逐点 Warn/Error 断言
// 面；t.Cleanup 还原——runtime_ensure_test.go 的 swarm 同款纪律）。
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// waitFor 在限时内轮询条件成立（日志断言的时序解耦面：写日志的 goroutine
// 与断言的测试 goroutine 不同步；cond 必须是纯谓词——testify 断言失败即
// 永久标记，不能塞进轮询体）。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWatchDegradedPathsWarn（收敛批 P0-5 静默失败族）：初始锚定失败与
// pod 建流失败都必须有 Warn（apiserver 不可达窗口的诊断线索），且降级期
// 循环存活、按退避节拍持续重试，不 panic；ctx 取消即时收口。
func TestWatchDegradedPathsWarn(t *testing.T) {
	logs := captureLogs(t)
	p, cli := newFakeProvider()
	cli.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected: apiserver unreachable")
	})
	var watchAttempts atomic.Int32
	cli.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
		watchAttempts.Add(1)
		return true, nil, errors.New("injected: watch setup refused")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx)
	require.NoError(t, err)

	waitFor(t, "initial anchoring degradation to be logged", func() bool {
		return strings.Contains(logs.String(), "k3s watch: initial node anchoring failed")
	})
	assert.Contains(t, logs.String(), "k3s watch: initial node anchoring failed; degraded to periodic rescan")
	waitFor(t, "pod watch setup failure to be logged", func() bool {
		return strings.Contains(logs.String(), "k3s watch: pod watch stream setup failed")
	})
	assert.Contains(t, logs.String(), "k3s watch: pod watch stream setup failed; caller will retry after backoff")

	// 退避节拍下的持续重试（间隔 1s：1.5s 窗内至少完成第二次建流尝试）——
	// 循环存活、降级不 panic 的实证。
	time.Sleep(1500 * time.Millisecond)
	assert.GreaterOrEqual(t, watchAttempts.Load(), int32(2),
		"stream rebuild must keep retrying on the backoff cadence")
	select {
	case <-ch:
		t.Fatal("watch stream must stay open while degraded (backoff retry loop)")
	default:
	}

	cancel()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("watch loop must close the stream on ctx cancel")
	}
}

// TestWatchRoundRecoversFromPanic（收敛批 P0-5 panic 护栏）：锚定路径 panic
// 必须被 watchRound 的 recover 接住（记 Error），退避后重开下一轮——不修
// 则 panic 直接击穿 fleetlyd。panic 经 fake reactor 注入（reactor 在调用方
// goroutine 内同步执行，走真实调用链，无需额外缝）；节点故意不带 labels
// （nil Labels 是护栏落地前就存在的真实 panic 形态——顺带钉住
// SetMetaDataLabel 的 nil map 修复）；第二轮锚定成功铸造节点 → NodeJoined
// 事件到达 = loop 存活、继续轮次的实证。
func TestWatchRoundRecoversFromPanic(t *testing.T) {
	logs := captureLogs(t)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	p, cli := newFakeProvider(node)
	var panicked atomic.Bool
	cli.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if panicked.CompareAndSwap(false, true) {
			panic("injected: anchoring blew up")
		}
		return false, nil, nil // 首次后放行为缺省反应（第二轮锚定走真实路径）
	})
	p.mintNodePlatformID = func(context.Context) (string, error) { return "node-platform-01", nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx)
	require.NoError(t, err)

	// 第二轮（recover → 1s 退避 → 重开）锚定成功铸造并上报 NodeJoined。
	select {
	case ev := <-ch:
		require.NotNil(t, ev.NodeJoined, "expected a node.joined event after panic recovery, got %+v", ev)
		assert.Equal(t, "node-platform-01", ev.NodeJoined.NodeID)
	case <-time.After(5 * time.Second):
		t.Fatal("node.joined event missing: watch loop did not survive the injected panic")
	}
	assert.True(t, panicked.Load(), "anchor path must have been entered (panic injection sanity)")
	assert.Contains(t, logs.String(), "k3s watch: recovered from panic")
	assert.Contains(t, logs.String(), "injected: anchoring blew up")

	cancel()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("watch loop must close the stream on ctx cancel")
	}
}

// TestWatchBackoffIsCtxAware（收敛批 P0-5 退避纪律）：断流退避必须 ctx 感知
// ——time.Sleep 不看 ctx，优雅退出会被整段退避拖住（swarm Q-19 同族）。
// 建流恒失败让循环持续处于 1s 退避窗，窗内取消必须在远小于整窗的时间内
// 收口（不修则取消后仍要白等满 1s）。
func TestWatchBackoffIsCtxAware(t *testing.T) {
	p, cli := newFakeProvider()
	cli.PrependWatchReactor("pods", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New("injected: stream unavailable")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx)
	require.NoError(t, err)

	// 让循环进入退避窗（首拍建流失败即刻退避）。
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case <-ch:
		assert.Less(t, time.Since(start).Milliseconds(), int64(900),
			"cancel during backoff must exit immediately, not after the full 1s window")
	case <-time.After(5 * time.Second):
		t.Fatal("watch loop must close the stream on ctx cancel")
	}
}

// TestReconcileAllPodsListFailureReturnsError（收敛批 P0-5 静默失败族）：
// 全量对账的 list 失败必须显式返回错误（调用方记 Warn）——修复前裸
// return，兜底路径失灵无诊断线索。
func TestReconcileAllPodsListFailureReturnsError(t *testing.T) {
	p, cli := newFakeProvider()
	cli.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected: pod list refused")
	})
	out := make(chan capability.WorkloadEvent, 8)
	err := p.reconcileAllPods(context.Background(), out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list pods")
	assert.Contains(t, err.Error(), "injected: pod list refused")
}
