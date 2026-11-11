package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Loop.Reset 换代语义：panic 关闭当前代 Done；Reset 后 Done 是新开通道、
// Panicked 清零，循环可再次 Run；旧代 Done 保持已关闭（观察者不会看到
// 关闭通道"重新打开"）。
func TestLoopResetStartsFreshGeneration(t *testing.T) {
	loop := NewLoop("fixture", discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	loop.Kick()
	loop.Run(ctx, time.Hour, func(context.Context) {
		calls.Add(1)
		panic("first generation")
	})
	require.Equal(t, int32(1), calls.Load())
	require.True(t, loop.Panicked())
	oldDone := loop.Done()
	select {
	case <-oldDone:
	default:
		t.Fatal("panic must close the current generation's Done channel")
	}

	loop.Reset()
	assert.False(t, loop.Panicked(), "reset clears the panic marker for the new generation")
	assert.Equal(t, uint64(1), loop.Restarts(), "reset counts one panic-driven restart")
	select {
	case <-loop.Done():
		t.Fatal("reset must install a fresh, open Done channel")
	default:
	}
	select {
	case <-oldDone:
	default:
		t.Fatal("the old generation's Done channel must stay closed")
	}

	ran := make(chan struct{}, 1)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		loop.Run(ctx, time.Hour, func(context.Context) { ran <- struct{}{} })
	}()
	loop.Kick()
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("the reset loop must run the next step")
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the reset loop must stop on context cancellation")
	}
	assert.False(t, loop.Panicked(), "graceful cancellation is not a panic")
	select {
	case <-loop.Done():
	default:
		t.Fatal("graceful cancellation must close the current generation's Done channel")
	}
}
