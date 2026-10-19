package swarm

// swarm exec 轮询节拍单测（R06-M5）：stdin 立即 EOF 是 CI/脚本/echo| 的常态；
// stdinDone 是"已关闭"通道，select 里恒就绪会让每次 ExecInspect 紧接下一次
// （无退避忙轮询打到容器退出）。本文件用 fake daemon 的 exec inspect 面 +
// 注入节拍缝钉住"stdin 尽后恢复 150ms 节拍"。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execBeatRecorder 是注入的轮询节拍（记录请求的间隔；返回"delay 后就绪"
// 的通道——首拍在 stdinDone 已关闭时不会被选中，之后每拍真的等待 delay，
// 测试无需真等 150ms）。
type execBeatRecorder struct {
	delay time.Duration
	beats []time.Duration
}

func (r *execBeatRecorder) beat(d time.Duration) <-chan time.Time {
	r.beats = append(r.beats, d)
	ch := make(chan time.Time, 1)
	go func() {
		time.Sleep(r.delay)
		ch <- time.Now()
	}()
	return ch
}

// TestWaitExecExitRestoresPollBeatAfterStdinEOF 钉 R06-M5：stdin 在首个轮询
// 周期前已 EOF（echo | fleetly exec 形态）时，每次 ExecInspect 之间必须走
// 150ms 节拍——旧实现 stdinDone 恒就绪，select 每拍立即落该 case（inspect
// 紧接下一次、零等待）。判据 = 每次请求的间隔恒为 execInspectPoll 且总耗时
// 真的覆盖多次注入节拍（忙轮询会在微秒级跑完）。
func TestWaitExecExitRestoresPollBeatAfterStdinEOF(t *testing.T) {
	const beatDelay = 30 * time.Millisecond
	f := newFakeDaemon()
	p := &Provider{cli: f.newClient(t)}
	f.execRunning = 3
	f.execExitCode = 7
	stdinDone := make(chan struct{})
	close(stdinDone)

	rec := &execBeatRecorder{delay: beatDelay}
	start := time.Now()
	code, err := p.waitExecExit(context.Background(), "exec-1", stdinDone, rec.beat)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, 7, code, "exit code must pass through")
	assert.Equal(t, 4, f.execPollCount())
	require.NotEmpty(t, rec.beats, "the poll beat must be armed")
	for _, d := range rec.beats {
		assert.Equal(t, execInspectPoll, d, "each post-EOF poll iteration must request the 150ms beat")
	}
	// 首拍被 stdinDone 吃掉，余下两次轮询必须真的等待（30ms × 2）；忙轮询
	// 的耗时为微秒级。
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond,
		"busy polling returns immediately: every post-EOF iteration must actually wait the beat")
}

// TestWaitExecExitHonorsContextCancellation：取消仍是终止面（stdin 未尽的
// 交互态下 ctx 结束必须立即返回，不得挂在节拍上）。
func TestWaitExecExitHonorsContextCancellation(t *testing.T) {
	f := newFakeDaemon()
	p := &Provider{cli: f.newClient(t)}
	f.execRunning = 1000 // 永不在本轮退出
	ctx, cancel := context.WithCancel(context.Background())
	stdinDone := make(chan struct{}) // stdin 未竭
	rec := &execBeatRecorder{delay: 30 * time.Millisecond}
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	code, err := p.waitExecExit(ctx, "exec-1", stdinDone, rec.beat)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, code)
}
