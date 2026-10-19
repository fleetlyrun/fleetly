package cmd

// dialFromEnv 的请求级 deadline 形态测试（P1-2 CLI 面）：非流式动词默认
// 有界（cliRequestTimeout），流式动词（logs / builds logs 的 follow 会话）
// 经 noDeadline 豁免——不得被客户端 deadline 腰斩。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialFromEnvDeadline(t *testing.T) {
	// 非流式默认：拨号返回的 ctx 带 ~cliRequestTimeout 的 deadline。
	ctx, cancel, c, err := dialFromEnv(context.Background())
	require.NoError(t, err)
	defer cancel()
	defer func() { _ = c.Close() }()
	dl, ok := ctx.Deadline()
	require.True(t, ok, "non-streaming dial must carry a request deadline")
	assert.WithinDuration(t, time.Now().Add(cliRequestTimeout), dl, 5*time.Second)

	// 流式豁免：noDeadline 的拨号不附 deadline（follow 会话长存活）。
	sctx, scancel, sc, err := dialFromEnv(context.Background(), noDeadline())
	require.NoError(t, err)
	defer scancel()
	defer func() { _ = sc.Close() }()
	_, ok = sctx.Deadline()
	assert.False(t, ok, "streaming verbs must be exempt from the client deadline")
}

// TestUnaryDeadlineContextKeepsWaitCtxClean：H14（R10-H1）——unary 的 120s
// 上限只包 unary 调用；等待流使用的原始 ctx 不得被污染（否则 deploy --wait
// 超过 2 分钟必然假失败）。已有 deadline 时不再叠加，原样复用。
func TestUnaryDeadlineContextKeepsWaitCtxClean(t *testing.T) {
	parent := context.Background()
	unaryCtx, cancel := unaryDeadlineContext(parent)
	defer cancel()
	dl, ok := unaryCtx.Deadline()
	require.True(t, ok, "unary calls keep the bounded request deadline")
	assert.WithinDuration(t, time.Now().Add(cliRequestTimeout), dl, 5*time.Second)
	_, waitHasDeadline := parent.Deadline()
	assert.False(t, waitHasDeadline, "the wait stream must keep the deadline-free parent ctx")

	bounded, bcancel := context.WithTimeout(context.Background(), time.Minute)
	defer bcancel()
	want, _ := bounded.Deadline()
	got, noop := unaryDeadlineContext(bounded)
	defer noop()
	gotDL, ok := got.Deadline()
	require.True(t, ok)
	assert.Equal(t, want, gotDL, "an existing deadline is reused, not stacked")
}
