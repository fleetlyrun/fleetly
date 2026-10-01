package authn

// resolve 的凭证查表分诊测试（Q-24）：行不存在 = E_UNAUTHENTICATED（正常
// 拒绝面，静默）；存储故障 = E_INTERNAL + ERROR 日志（不吞错）。

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// syncBuffer 串行化并发写（slog handler 可能跨 goroutine 调用）。
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

func TestResolveTokenLookupFailure(t *testing.T) {
	db, _ := statetest.New(t)
	var logs syncBuffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	// resolve 只触查表路径（policy 在 guard 执法时消费），nil 即占位。
	a := NewAuthenticator(db, nil, testVocab, log)
	// 合法 platform 前缀但不在册的凭证：进 GetBySHA256 分支。
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+identity.TokenPrefix+"unknown-token-material"))

	// ① 未知 token（state.ErrNotFound）→ E_UNAUTHENTICATED，无查表失败日志。
	_, err := a.resolve(ctx)
	require.Error(t, err)
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "E_UNAUTHENTICATED", e.Code())
	assert.Equal(t, "invalid token", e.Message())
	assert.NotContains(t, logs.String(), "authn: token lookup failed")

	// ② 存储故障（关闭库 → 非 ErrNotFound 的查询错误）→ E_INTERNAL +
	// "authn: token lookup failed" ERROR 日志（不伪装成无效凭证吞错）。
	require.NoError(t, db.Close())
	_, err = a.resolve(ctx)
	require.Error(t, err)
	require.ErrorAs(t, err, &e)
	assert.Equal(t, "E_INTERNAL", e.Code())
	assert.Equal(t, "token lookup failed", e.Message())
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), "authn: token lookup failed")
}
