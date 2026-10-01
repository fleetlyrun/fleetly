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
