package idem

// 幂等执法器 hermetic 单测（ADR-0024 验收锚）：重放/异体冲突/在途冲突/
// 失败释放/保留窗过期/认领过期/无键放行/双源一致性/清理。

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	idemrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/idempotency"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

const createProjectMethod = "/fleetly.structure.v1.ProjectsService/CreateProject"

// call 经拦截器驱动一次不带调用方身份的请求（anonymous 作用域）；handler
// 返回固定响应（计数由闭包持有）。
func call(t *testing.T, e *Enforcer, key string, req proto.Message, handler grpc.UnaryHandler) (any, error) {
	t.Helper()
	return callAs(t, e, key, nil, req, handler)
}

// callAs 经拦截器驱动一次携带调用方身份的请求（scope 由 authn.Identity
// 派生：TokenID 优先，其次 UserID；nil = 无身份，anonymous 作用域）。
func callAs(t *testing.T, e *Enforcer, key string, id *authn.Identity, req proto.Message, handler grpc.UnaryHandler) (any, error) {
	t.Helper()
	ctx := context.Background()
	if key != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(HeaderKey, key))
	}
	if id != nil {
		ctx = authn.WithIdentity(ctx, id)
	}
	return e.Unary()(ctx, req, &grpc.UnaryServerInfo{FullMethod: createProjectMethod}, handler)
}

func projReq(name string) *structurev1.CreateProjectRequest {
	return &structurev1.CreateProjectRequest{Name: name, TeamId: "default"}
}

func projResp(id string) *structurev1.CreateProjectResponse {
	return &structurev1.CreateProjectResponse{Project: &structurev1.Project{Id: id, Name: "shop"}}
}

// codeOf 提取 apperr 信封错误码（本地 *Error 或 status 形态皆可——出口
// 拦截器之外单测驱动时是本地形态；无信封时给 gRPC code 兜底标签）。
func codeOf(t *testing.T, err error) string {
	t.Helper()
	if e, ok := apperrstatus.FromError(err); ok {
		return e.Code()
	}
	st, _ := status.FromError(err)
	return "grpc:" + st.Code().String()
}

// projectIDOf 取响应里的项目 id（跨调用方隔离断言用）。
func projectIDOf(t *testing.T, resp any) string {
	t.Helper()
	msg, ok := resp.(*structurev1.CreateProjectResponse)
	require.True(t, ok, "handler response must be a CreateProjectResponse")
	return msg.GetProject().GetId()
}

// 同键同体重放返回同一响应，handler 只执行一次。
func TestReplaySameBodyReturnsSameResponse(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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

// callerScope 组成：Token 优先于 User，无身份回退 anonymous（H11 /
// R14-H4 的作用域语义单源）。
func TestCallerScopeComposition(t *testing.T) {
	assert.Equal(t, "anonymous", callerScope(context.Background()), "no identity = anonymous scope")
	assert.Equal(t, "token:tok-a", callerScope(authn.WithIdentity(context.Background(),
		&authn.Identity{TokenID: "tok-a", UserID: "user-1", TeamID: "team-a"})),
		"token identity takes precedence over its creator user")
	assert.Equal(t, "user:user-1", callerScope(authn.WithIdentity(context.Background(),
		&authn.Identity{UserID: "user-1", TeamID: "team-a"})),
		"non-token identity falls back to the user dimension")
}

// 同 Token 同键同体 → 重放（作用域收窄不放松同一调用方的幂等承诺）。
func TestSameTokenReplays(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
	tok := &authn.Identity{TokenID: "tok-a", TeamID: "team-a"}
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	r1, err := callAs(t, e, "k1", tok, projReq("shop"), handler)
	require.NoError(t, err)
	r2, err := callAs(t, e, "k1", tok, projReq("shop"), handler)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "same token + key + body must replay without re-executing the handler")
	assert.True(t, proto.Equal(r1.(proto.Message), r2.(proto.Message)),
		"replay must return the stored response verbatim")
}

// 同键同体不同 Token → 各自执行（不重放）：键空间按 principal 隔离，
// H11/R14-H4 的跨 Token 串台在拦截器层闭合。
func TestCrossTokenSameKeyDoesNotReplay(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
	tokenA := &authn.Identity{TokenID: "tok-a", TeamID: "team-a"}
	tokenB := &authn.Identity{TokenID: "tok-b", TeamID: "team-b"}
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp(fmt.Sprintf("resp-%d", count)), nil
	}
	rA, err := callAs(t, e, "shared-key", tokenA, projReq("shop"), handler)
	require.NoError(t, err)
	rB, err := callAs(t, e, "shared-key", tokenB, projReq("shop"), handler)
	require.NoError(t, err, "a different token must not be blocked by the other token's key")
	assert.Equal(t, 2, count, "each token executes its own handler instead of replaying the other's response")
	assert.Equal(t, "resp-1", projectIDOf(t, rA))
	assert.Equal(t, "resp-2", projectIDOf(t, rB), "the second token must not receive the first token's response")

	// 各自的键空间仍可重放自己的响应。
	rA2, err := callAs(t, e, "shared-key", tokenA, projReq("shop"), handler)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, "resp-1", projectIDOf(t, rA2))
}

// 跨 Team 不同 Token 同键同体：B 队拿到自己的响应而非 A 队的（明文
// secret 类响应的泄露面在键空间层被切断）。
func TestCrossTeamDifferentTokensDoNotLeak(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
	memberA := &authn.Identity{TokenID: "tok-a", TeamID: "team-a"}
	memberB := &authn.Identity{TokenID: "tok-b", TeamID: "team-b"}
	count := 0
	handler := func(ctx context.Context, _ any) (any, error) {
		count++
		id, _ := authn.FromContext(ctx)
		return projResp("team:" + id.TeamID), nil
	}
	rA, err := callAs(t, e, "ci-token-1", memberA, projReq("ci"), handler)
	require.NoError(t, err)
	rB, err := callAs(t, e, "ci-token-1", memberB, projReq("ci"), handler)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "the same key+body across teams must not dedupe into one execution")
	assert.Equal(t, "team:team-a", projectIDOf(t, rA))
	assert.Equal(t, "team:team-b", projectIDOf(t, rB), "team B must never receive team A's stored response")

	// B 的重放仍是 B 自己的完成态行。
	rB2, err := callAs(t, e, "ci-token-1", memberB, projReq("ci"), handler)
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.True(t, proto.Equal(rB.(proto.Message), rB2.(proto.Message)))
}

// 同 token 同键异体 → 仍 409（作用域收窄不放松同主体内的单请求语义）。
func TestSameTokenDifferentBodyConflicts(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
	tok := &authn.Identity{TokenID: "tok-a", TeamID: "team-a"}
	count := 0
	handler := func(context.Context, any) (any, error) {
		count++
		return projResp("01JD0P"), nil
	}
	_, err := callAs(t, e, "k1", tok, projReq("shop"), handler)
	require.NoError(t, err)
	_, err = callAs(t, e, "k1", tok, projReq("other"), handler)
	require.Error(t, err)
	assert.Equal(t, "E_IDEMPOTENCY_KEY_CONFLICT", codeOf(t, err))
	assert.Equal(t, 1, count)
}

// 在途同键 → 冲突（in progress）；完成后同键重试 → 重放。
func TestInflightConflictThenReplay(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	db, clock := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	db, clock := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
	// 模拟崩溃遗留：直接落一条 inflight 行（handler 从未完成；无身份调用
	// = anonymous 作用域，与下方 call 的派生一致）。
	require.NoError(t, idemrepo.New(db.Clock(), db.Runner()).
		Claim(context.Background(), callerScope(context.Background()), "k1", createProjectMethod, "stale-fp", ClaimTTL))
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
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	_, err = e.repo.Get(context.Background(), callerScope(context.Background()), "any")
	assert.ErrorIs(t, err, state.ErrNotFound, "no records may be written for keyless requests")
}

// 双源一致性：body 自带幂等键与头不一致 → 冲突；一致 → 正常。
func TestDualSourceConsistency(t *testing.T) {
	db, _ := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	db, clock := statetest.New(t)
	e := NewEnforcer(db, idemrepo.New(db.Clock(), db.Runner()), slog.New(slog.DiscardHandler))
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
	_, err = e.repo.Get(context.Background(), callerScope(context.Background()), "k1")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
