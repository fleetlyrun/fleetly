package idempotency_test

// Idempotency 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：幂等键
// 生命周期四拍——Claim 落 inflight 认领（expires = now+TTL，同键二次认领
// 归一 ErrAlreadyExists 且不覆盖在册行：同体重放/异体冲突的分诊归调用
// 方）、Complete 落完成态（响应引用 + 24h 保留窗；缺行 ErrNotFound 不复
// 活）、Release 删认领（失败路径干净重试：同键可再 Claim）、Sweep 清保
// 留窗外行（计数返回，未到期行幸存）。Get 含过期行——过期判定与惰性清
// 理在调用方（repo 注释口径）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/idempotency"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	idempotencyrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/idempotency"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	claimTTL   = 5 * time.Minute
	retention  = 24 * time.Hour
	responseMD = "fleetly.test.v1.EchoResponse"
)

// responseVal 是确定性序列化响应体夹具（BLOB 载荷非常量形态）。
var responseVal = []byte(`{"msg":"done"}`)

// TestIdempotencyClaimCompleteReplay：认领 → 冲突不覆盖 → 完成 → 重放读。
func TestIdempotencyClaimCompleteReplay(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	records := idempotencyrepo.New(clock, db.Runner())

	// Claim 落 inflight 行：created = now，expires = now+TTL。
	require.NoError(t, records.Claim(ctx, "op-1", "POST", "fp-a", claimTTL))
	rec, err := records.Get(ctx, "op-1")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateInflight, rec.State)
	assert.Equal(t, "POST", rec.Method)
	assert.Equal(t, "fp-a", rec.Fingerprint)
	assert.True(t, rec.CreatedAt.Equal(clock.Now()), "created_at anchors at claim time")
	assert.True(t, rec.ExpiresAt.Equal(clock.Now().Add(claimTTL)),
		"inflight expires_at carries the claim TTL")

	// 同键二次认领（异指纹）→ ErrAlreadyExists；在册行不被覆盖——重放
	// 还是冲突，分诊在拦截器，存储面只保行事实。
	err = records.Claim(ctx, "op-1", "POST", "fp-b", claimTTL)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"a second claim on a live key must normalize to ErrAlreadyExists")
	rec, err = records.Get(ctx, "op-1")
	require.NoError(t, err)
	assert.Equal(t, "fp-a", rec.Fingerprint, "the losing claim must not overwrite the row")

	// Complete 落完成态：响应引用 + 保留窗覆写 expires_at。
	clock.Advance(time.Second)
	require.NoError(t, records.Complete(ctx, "op-1", responseMD, responseVal, retention))
	rec, err = records.Get(ctx, "op-1")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateCompleted, rec.State)
	assert.Equal(t, responseMD, rec.ResponseType)
	assert.Equal(t, responseVal, rec.ResponseBody, "deterministic body round-trips as BLOB")
	assert.True(t, rec.ExpiresAt.Equal(clock.Now().Add(retention)),
		"completed expires_at switches to the replay retention window")

	// 完成态在册期间同键再认领同样被拒（一键一请求语义）。
	err = records.Claim(ctx, "op-1", "POST", "fp-a", claimTTL)
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// Complete 缺行 ErrNotFound（幂等承诺随行消失，不复活）。
	err = records.Complete(ctx, "missing-key", responseMD, responseVal, retention)
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestIdempotencyReleaseRetryAndSweep：失败释放干净重试 + janitor 清理窗。
func TestIdempotencyReleaseRetryAndSweep(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	records := idempotencyrepo.New(clock, db.Runner())

	// Release 删认领行；再 Complete 是 ErrNotFound（行没了承诺也没了）。
	require.NoError(t, records.Claim(ctx, "k2", "PUT", "fp-x", claimTTL))
	require.NoError(t, records.Release(ctx, "k2"))
	_, err := records.Get(ctx, "k2")
	assert.ErrorIs(t, err, state.ErrNotFound)
	err = records.Complete(ctx, "k2", responseMD, responseVal, retention)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 释放后同键可干净重试（失败请求无响应可重放）。
	require.NoError(t, records.Claim(ctx, "k2", "PUT", "fp-x", claimTTL))
	rec, err := records.Get(ctx, "k2")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateInflight, rec.State)

	// Release 未认领键是 no-op（plain DELETE，无 NotFound 面）。
	require.NoError(t, records.Release(ctx, "never-claimed"))

	// k2 先落完成态（24h 保留窗），另认领长 TTL 的 k4——两者都是 Sweep
	// 窗外的幸存者；k3 认领 5m 后过期。
	require.NoError(t, records.Complete(ctx, "k2", responseMD, responseVal, retention))
	require.NoError(t, records.Claim(ctx, "k4", "GET", "fp-y", 24*time.Hour))
	require.NoError(t, records.Claim(ctx, "k3", "GET", "fp-z", claimTTL))
	clock.Advance(10 * time.Minute)
	n, err := records.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the expired inflight row is swept")
	_, err = records.Get(ctx, "k3")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = records.Get(ctx, "k4")
	require.NoError(t, err, "a live inflight row survives the sweep")
	_, err = records.Get(ctx, "k2")
	require.NoError(t, err, "a completed row inside its retention window survives the sweep")

	// 保留窗/TTL 走完后，幸存行也一并被清。
	clock.Advance(25 * time.Hour)
	n, err = records.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "the completed row and the long-claim row leave together")
	_, err = records.Get(ctx, "k2")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
