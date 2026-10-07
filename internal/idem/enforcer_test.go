package idem

// 幂等执法器 hermetic 单测（ADR-0024 验收锚）：重放/异体冲突/在途冲突/
// 失败释放/保留窗过期/认领过期/无键放行/双源一致性/清理。

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/idempotency"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

const createProjectMethod = "/fleetly.structure.v1.ProjectsService/CreateProject"

// call 经拦截器驱动一次请求；handler 返回固定响应（计数由闭包持有）。
func call(t *testing.T, e *Enforcer, key string, req proto.Message, handler grpc.UnaryHandler) (any, error) {
	t.Helper()
	ctx := context.Background()
	if key != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(HeaderKey, key))
	}
	return e.Unary()(ctx, req, &grpc.UnaryServerInfo{FullMethod: createProjectMethod}, handler)
}

func projReq(name string) *structurev1.CreateProjectRequest {
	return &structurev1.CreateProjectRequest{Name: name, TeamId: "default"}
}

func projResp(id string) *structurev1.CreateProjectResponse {
	return &structurev1.CreateProjectResponse{Project: &structurev1.Project{Id: id, Name: "shop"}}
}

// codeOf 提取 apperr 信封错误码（无信封时给 gRPC code 兜底标签）。
func codeOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	if e, ok := apperr.FromGRPCStatus(st); ok {
		return e.Code()
	}
	return "grpc:" + st.Code().String()
}

// 同键同体重放返回同一响应，handler 只执行一次。
func TestReplaySameBodyReturnsSameResponse(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	r1, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err)
	r2, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "replay must not re-execute the handler")
	assert.True(t, proto.Equal(r1.(proto.Message), r2.(proto.Message)), "replay must return the stored response verbatim")
}

// 同键异体 → E_IDEMPOTENCY_KEY_CONFLICT。
func TestConflictDifferentBody(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err)
	_, err = call(t, e, "k1", projReq("other"), handler)
	require.Error(t, err)
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))
	assert.Equal(t, 1, count)
}

// 在途同键 → 冲突（in progress）；完成后同键重试 → 重放。
func TestInflightConflictThenReplay(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	// entered 必须缓冲 1：handler 内是非阻塞 send——无缓冲形态下 spawn 的
	// goroutine 在主测试抵达 <-entered 前跑到 select 即信号被 default 吞，
	// 主测试永久阻塞（CI 慢调度实证挂起 600s，2026-10-07；本地快调度侥幸）。
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	handler := func(context.Context, any) (any, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		close(done)
		return projResp("01JD0P"), nil
	}
	go func() { _, _ = call(t, e, "k1", projReq("shop"), handler) }() //nolint:errcheck // 结果经下方断言收口
	<-entered

	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.Error(t, err, "a concurrent same-key request must not double-execute")
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))

	close(release)
	<-done
	// handler 返回 ≠ 回放账已落：拦截器在 handler 返回后才写响应行——CI
	// 负载下该窗放大（裸读即 flake，2026-10-04 CI 实证）。有界重试收敛窗
	// 口（在途态的重复调用不执行 handler——认领行恒在场，无假执行面）。
	var (
		r    any
		rerr error
	)
	for i := 0; i < 40; i++ {
		r, rerr = call(t, e, "k1", projReq("shop"), handler)
		if rerr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, rerr, "after completion the same key+body replays")
	assert.NotNil(t, r)
}

// handler 失败 → 释放认领：同键干净重试（第二次真执行）。
func TestHandlerFailureReleasesClaim(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	fail := true
	handler := func(context.Context, any) (any, error) {
		count++
		if fail {
			return nil, errors.New("boom")
		}
		return projResp("01JD0P"), nil
	}
	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.Error(t, err)
	fail = false
	_, err = call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err, "a failed request must release the key for a clean retry")
	assert.Equal(t, 2, count)
}

// 保留窗过期：完成态行过期后同键按新请求受理。
func TestRetentionExpiry(t *testing.T) {
	db, clock := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err)
	clock.Advance(Retention + time.Minute)
	_, err = call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err, "an expired record must be treated as absent")
	assert.Equal(t, 2, count)
}

// 认领过期（崩溃遗留 inflight）：过期即重新认领，不永久 409。
func TestClaimExpiryRecoversOrphan(t *testing.T) {
	db, clock := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	// 模拟崩溃遗留：直接落一条 inflight 行（handler 从未完成）。
	require.NoError(t, idempotency.New(db.Clock()).
		Claim(context.Background(), db.Runner(), "k1", createProjectMethod, "stale-fp", ClaimTTL))
	clock.Advance(ClaimTTL + time.Second)

	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err, "an expired claim must be reclaimed, not rejected forever")
	assert.Equal(t, 1, count)
}

// 无键放行：不落记录，语义不受影响。
func TestNoKeyPassthrough(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	_, err := call(t, e, "", projReq("shop"), handler)
	require.NoError(t, err)
	_, err = call(t, e, "", projReq("shop"), handler)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "no key means no idempotency, by design (optional commitment)")
	_, err = e.repo.Get(context.Background(), db.Runner(), "any")
	assert.ErrorIs(t, err, state.ErrNotFound, "no records may be written for keyless requests")
}

// 双源一致性：body 自带幂等键与头不一致 → 冲突；一致 → 正常。
func TestDualSourceConsistency(t *testing.T) {
	db, _ := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	req := &deliveryv1.DeployRequest{AppId: "01JD0A", IdempotencyKey: "body-key"}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(HeaderKey, "header-key"))
	_, err := e.Unary()(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/fleetly.delivery.v1.DeploymentsService/Deploy"}, handler)
	require.Error(t, err)
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))
	assert.Equal(t, 0, count, "a dual-source mismatch must be rejected before execution")

	req.IdempotencyKey = "header-key"
	_, err = e.Unary()(ctx, req, &grpc.UnaryServerInfo{FullMethod: "/fleetly.delivery.v1.DeploymentsService/Deploy"}, handler)
	require.NoError(t, err, "matching header and body values are the documented coherent form")
	assert.Equal(t, 1, count)
}

// Sweep 清理保留窗外行（janitor 观测面）。
func TestSweepRemovesExpired(t *testing.T) {
	db, clock := statertest.New(t)
	e := NewEnforcer(db, slog.New(slog.DiscardHandler))
	handler := func(context.Context, any) (any, error) { return projResp("01JD0P"), nil }
	_, err := call(t, e, "k1", projReq("shop"), handler)
	require.NoError(t, err)

	n, err := e.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n, "within the retention window nothing is swept")

	clock.Advance(Retention + time.Minute)
	n, err = e.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	_, err = e.repo.Get(context.Background(), db.Runner(), "k1")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
