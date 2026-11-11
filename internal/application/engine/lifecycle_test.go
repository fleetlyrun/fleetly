package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 重启退避钉板：第 n 次重启等待 baseDelay×2^(n-1)，封顶 maxDelay（缺省
// 1s→2s→4s→8s→16s，之后 30s）。
func TestRingRestartPolicyBackoff(t *testing.T) {
	p := defaultRingRestartPolicy()
	assert.Equal(t, 5, p.maxAttempts)
	for attempt, want := range map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 30 * time.Second,
		9: 30 * time.Second,
	} {
		assert.Equal(t, want, p.restartDelay(attempt), "attempt %d backoff", attempt)
	}
}

// 瞬时 panic 自愈：环 panic 一次后有界退避重启，下一代 step 继续执行；
// 不记死（DeadRings 空），Panicked 回到 false，Restarts 计数可见。
func TestRingPanicOnceRestartsAndConverges(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ringRestart = ringRestartPolicy{maxAttempts: 3, baseDelay: time.Millisecond, maxDelay: time.Millisecond}

	var calls atomic.Int32
	resumed := make(chan struct{}, 1)
	injected := false
	for i := range e.rings {
		if e.rings[i].name == "deployment" {
			e.rings[i].step = func(context.Context) {
				if calls.Add(1) == 1 {
					panic("injected once")
				}
				select {
				case resumed <- struct{}{}:
				default:
				}
			}
			injected = true
		}
	}
	require.True(t, injected, "fixture: deployment ring must be registered")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)

	select {
	case <-resumed:
	case <-time.After(2 * time.Second):
		t.Fatal("ring did not resume after a transient panic")
	}
	assert.Empty(t, e.DeadRings(), "a transient panic restart is not terminal death")
	assert.False(t, e.loop.Panicked(), "the new generation is healthy after the restart")
	assert.Equal(t, uint64(1), e.loop.Restarts(), "one panic-driven restart is counted")
}

// 优雅 Stop 不触发重启、不记死：ctx 取消是正常收口，不是 panic 失败。
func TestGracefulStopDoesNotRestartRings(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)

	require.NoError(t, e.Stop(context.Background()))
	assert.Empty(t, e.DeadRings(), "graceful stop must not mark rings dead")
	for _, r := range e.rings {
		assert.Zero(t, r.loop.Restarts(), "ring %s must not restart on graceful stop", r.name)
	}
	assert.Zero(t, e.driftLoop.Restarts(), "the drift loop must not restart on graceful stop")
}
