package idempotency_test

// Idempotency 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：幂等键
// 生命周期四拍——Claim 落 inflight 认领（expires = now+TTL，同作用域同键
// 二次认领归一 ErrAlreadyExists 且不覆盖在册行：同体重放/异体冲突的分诊
// 归调用方）、Complete 落完成态（响应引用 + 24h 保留窗；缺行 ErrNotFound
// 不复活）、Release 删认领（失败路径干净重试：同键可再 Claim）、Sweep 清
// 保留窗外行（计数返回，未到期行幸存）。Get 含过期行——过期判定与惰性清
// 理在调用方（repo 注释口径）。
//
// 键空间 = (scope, key)（H11 / R14-H4）：同键不同调用方作用域是两条独立
// 记录，Claim/Complete/Release 互不可见；同作用域同键仍是单请求语义。

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
	scopeA     = "token:tok-a" // 主作用域夹具（调用方 A）
	scopeB     = "token:tok-b" // 第二调用方（跨 Token/跨 Team 隔离面）
)

// responseVal 是确定性序列化响应体夹具（BLOB 载荷非常量形态）。
var responseVal = []byte(`{"msg":"done"}`)

// TestIdempotencyClaimCompleteReplay：认领 → 冲突不覆盖 → 完成 → 重放读。
func TestIdempotencyClaimCompleteReplay(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	records := idempotencyrepo.New(clock, db.Runner())

	// Claim 落 inflight 行：created = now，expires = now+TTL。
	require.NoError(t, records.Claim(ctx, scopeA, "op-1", "POST", "fp-a", claimTTL))
	rec, err := records.Get(ctx, scopeA, "op-1")
	require.NoError(t, err)
	assert.Equal(t, scopeA, rec.Scope, "the record carries its caller scope")
	assert.Equal(t, "op-1", rec.Key)
	assert.Equal(t, idempotency.StateInflight, rec.State)
	assert.Equal(t, "POST", rec.Method)
	assert.Equal(t, "fp-a", rec.Fingerprint)
	assert.True(t, rec.CreatedAt.Equal(clock.Now()), "created_at anchors at claim time")
	assert.True(t, rec.ExpiresAt.Equal(clock.Now().Add(claimTTL)),
		"inflight expires_at carries the claim TTL")

	// 同键二次认领（异指纹）→ ErrAlreadyExists；在册行不被覆盖——重放
	// 还是冲突，分诊在拦截器，存储面只保行事实。
	err = records.Claim(ctx, scopeA, "op-1", "POST", "fp-b", claimTTL)
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"a second claim on a live key must normalize to ErrAlreadyExists")
	rec, err = records.Get(ctx, scopeA, "op-1")
	require.NoError(t, err)
	assert.Equal(t, "fp-a", rec.Fingerprint, "the losing claim must not overwrite the row")

	// Complete 落完成态：响应引用 + 保留窗覆写 expires_at。
	clock.Advance(time.Second)
	require.NoError(t, records.Complete(ctx, scopeA, "op-1", responseMD, responseVal, retention))
	rec, err = records.Get(ctx, scopeA, "op-1")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateCompleted, rec.State)
	assert.Equal(t, responseMD, rec.ResponseType)
	assert.Equal(t, responseVal, rec.ResponseBody, "deterministic body round-trips as BLOB")
	assert.True(t, rec.ExpiresAt.Equal(clock.Now().Add(retention)),
		"completed expires_at switches to the replay retention window")

	// 完成态在册期间同键再认领同样被拒（一键一请求语义）。
	err = records.Claim(ctx, scopeA, "op-1", "POST", "fp-a", claimTTL)
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// Complete 缺行 ErrNotFound（幂等承诺随行消失，不复活）。
	err = records.Complete(ctx, scopeA, "missing-key", responseMD, responseVal, retention)
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// TestIdempotencyScopeIsolation：同键不同作用域是两条独立记录——各自
// 认领/完成/释放互不可见；同作用域同键仍归一 ErrAlreadyExists（H11 /
// R14-H4：跨 Token/跨 Team 不再串台）。
func TestIdempotencyScopeIsolation(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	records := idempotencyrepo.New(clock, db.Runner())

	// A 认领后，B 的同键读不到任何东西（键空间按作用域收窄）。
	require.NoError(t, records.Claim(ctx, scopeA, "shared-key", "POST", "fp-a", claimTTL))
	_, err := records.Get(ctx, scopeB, "shared-key")
	assert.ErrorIs(t, err, state.ErrNotFound, "another caller scope must not see the key")

	// B 同键可独立认领（旧全局 PK 已拆为 (scope, key)）。
	require.NoError(t, records.Claim(ctx, scopeB, "shared-key", "POST", "fp-a", claimTTL),
		"same key under another caller scope must be claimable independently")

	a, err := records.Get(ctx, scopeA, "shared-key")
	require.NoError(t, err)
	b, err := records.Get(ctx, scopeB, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, scopeA, a.Scope)
	assert.Equal(t, scopeB, b.Scope)

	// 完成 A 不触碰 B：两行各自独立演进。
	require.NoError(t, records.Complete(ctx, scopeA, "shared-key", responseMD, responseVal, retention))
	a, err = records.Get(ctx, scopeA, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateCompleted, a.State)
	b, err = records.Get(ctx, scopeB, "shared-key")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateInflight, b.State, "completing one scope must not touch the other")
	assert.Nil(t, b.ResponseBody)

	// 收窄不放松队内语义：同作用域同键二次认领仍 ErrAlreadyExists。
	err = records.Claim(ctx, scopeA, "shared-key", "POST", "fp-a", claimTTL)
	assert.ErrorIs(t, err, state.ErrAlreadyExists)

	// Release 只在作用域内生效：删 B 的行，A 的行仍在。
	require.NoError(t, records.Release(ctx, scopeB, "shared-key"))
	_, err = records.Get(ctx, scopeB, "shared-key")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = records.Get(ctx, scopeA, "shared-key")
	require.NoError(t, err, "releasing another scope's key must not affect this scope")
}

// TestIdempotencyReleaseRetryAndSweep：失败释放干净重试 + janitor 清理窗。
func TestIdempotencyReleaseRetryAndSweep(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	records := idempotencyrepo.New(clock, db.Runner())

	// Release 删认领行；再 Complete 是 ErrNotFound（行没了承诺也没了）。
	require.NoError(t, records.Claim(ctx, scopeA, "k2", "PUT", "fp-x", claimTTL))
	require.NoError(t, records.Release(ctx, scopeA, "k2"))
	_, err := records.Get(ctx, scopeA, "k2")
	assert.ErrorIs(t, err, state.ErrNotFound)
	err = records.Complete(ctx, scopeA, "k2", responseMD, responseVal, retention)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 释放后同键可干净重试（失败请求无响应可重放）。
	require.NoError(t, records.Claim(ctx, scopeA, "k2", "PUT", "fp-x", claimTTL))
	rec, err := records.Get(ctx, scopeA, "k2")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StateInflight, rec.State)

	// Release 未认领键是 no-op（plain DELETE，无 NotFound 面）。
	require.NoError(t, records.Release(ctx, scopeA, "never-claimed"))

	// k2 先落完成态（24h 保留窗），另认领长 TTL 的 k4——两者都是 Sweep
	// 窗外的幸存者；k3 认领 5m 后过期。
	require.NoError(t, records.Complete(ctx, scopeA, "k2", responseMD, responseVal, retention))
	require.NoError(t, records.Claim(ctx, scopeA, "k4", "GET", "fp-y", 24*time.Hour))
	require.NoError(t, records.Claim(ctx, scopeA, "k3", "GET", "fp-z", claimTTL))
	clock.Advance(10 * time.Minute)
	n, err := records.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the expired inflight row is swept")
	_, err = records.Get(ctx, scopeA, "k3")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = records.Get(ctx, scopeA, "k4")
	require.NoError(t, err, "a live inflight row survives the sweep")
	_, err = records.Get(ctx, scopeA, "k2")
	require.NoError(t, err, "a completed row inside its retention window survives the sweep")

	// 保留窗/TTL 走完后，幸存行也一并被清。
	clock.Advance(25 * time.Hour)
	n, err = records.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "the completed row and the long-claim row leave together")
	_, err = records.Get(ctx, scopeA, "k2")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
