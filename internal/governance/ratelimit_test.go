package governance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
)

// withToken 把身份注入 ctx（拦截器消费 authn.FromContext）。
func withToken(ctx context.Context, tokenID string) context.Context {
	return authn.WithIdentity(ctx, &authn.Identity{TokenID: tokenID, TokenName: tokenID})
}

// invoke 经拦截器跑一次 handler（fullMethod 决定是否落执法面）。
func invoke(t *testing.T, lim *RateLimiter, ctx context.Context, fullMethod string) error {
	t.Helper()
	called := false
	_, err := lim.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: fullMethod},
		func(context.Context, any) (any, error) { called = true; return nil, nil })
	if err == nil {
		require.True(t, called, "handler must run on the pass-through path")
	}
	return err
}

func TestRateLimiterFixedWindow(t *testing.T) {
	_, clock := statetest.New(t)
	lim := NewRateLimiter(clock, time.Minute, 3)
	ctx := withToken(context.Background(), "01JD0TOK000000000000000001")
	const createTask = "/fleetly.automation.v1.TasksService/CreateTask"

	// 预算内放行 + 计数。
	for i := 0; i < 3; i++ {
		require.NoError(t, invoke(t, lim, ctx, createTask))
	}
	// 第 4 次 → E_RATE_LIMITED（信封 retry_after_seconds + RetryInfo）。
	err := invoke(t, lim, ctx, createTask)
	require.Error(t, err)
	ae, ok := apperr.FromError(err)
	require.True(t, ok)
	assert.Equal(t, "E_RATE_LIMITED", ae.Code())
	st := ae.ToGRPCStatus()
	require.Len(t, st.Details(), 2, "envelope detail + RetryInfo detail")

	// 窗口翻转 → 计数复位。
	clock.Advance(61 * time.Second)
	require.NoError(t, invoke(t, lim, ctx, createTask))

	// per-Token 隔离：另一 Token 不受影响。
	require.NoError(t, invoke(t, lim, withToken(context.Background(), "01JD0TOK000000000000000002"), createTask))
}

func TestRateLimiterScopeExemptions(t *testing.T) {
	_, clock := statetest.New(t)
	lim := NewRateLimiter(clock, time.Minute, 1)
	const readVerb = "/fleetly.automation.v1.TasksService/ListTasks"

	// 非创建型动词不计数。
	ctx := withToken(context.Background(), "01JD0TOK000000000000000003")
	require.NoError(t, invoke(t, lim, ctx, readVerb))
	require.NoError(t, invoke(t, lim, ctx, readVerb))

	// 无 Token 身份（PUBLIC 面：webhook 接收等）不计数。
	require.NoError(t, invoke(t, lim, context.Background(),
		"/fleetly.delivery.v1.HooksService/ReceiveWebhook"))
	require.NoError(t, invoke(t, lim, context.Background(),
		"/fleetly.delivery.v1.HooksService/ReceiveWebhook"))
}
