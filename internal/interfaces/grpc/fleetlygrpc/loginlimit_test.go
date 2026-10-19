package fleetlygrpc

// Login 限速器单测（R14-M1 限速半边）：固定窗阈值、成功清零、双维度
// 隔离（用户名桶跨来源、来源桶跨用户名）与有界内存/惰性清扫。

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// fakeLoginClock 是 limiter 的假时钟（窗口推进由测试显式驱动）。
type fakeLoginClock struct{ now time.Time }

func (c *fakeLoginClock) Now() time.Time          { return c.now }
func (c *fakeLoginClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestLoginLimiter() (*loginLimiter, *fakeLoginClock) {
	clock := &fakeLoginClock{now: time.Unix(1767225600, 0).UTC()}
	return newLoginLimiter(clock), clock
}

// requireRateLimited 断言 E_RATE_LIMITED 信封与 retry 语义。
func requireRateLimited(t *testing.T, err error) *apperr.Error {
	t.Helper()
	var ae *apperr.Error
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "E_RATE_LIMITED", ae.Code())
	wait, ok := ae.RetryAfter()
	require.True(t, ok, "rate-limit error must carry RetryAfter")
	assert.GreaterOrEqual(t, wait, time.Second, "retry window is at least 1s")
	assert.NotEmpty(t, ae.Suggestion())
	return ae
}

func TestLoginLimiterFixedWindowAndSuccessReset(t *testing.T) {
	lim, _ := newTestLoginLimiter()
	for i := 0; i < loginUsernameBudget; i++ {
		require.NoError(t, lim.begin("10.0.0.1", "alice"), "attempt %d must pass", i+1)
	}
	requireRateLimited(t, lim.begin("10.0.0.1", "alice"))

	// 成功登录清零两桶：再给满预算，仍要到第 budget+1 次才拒。
	lim.reset("10.0.0.1", "alice")
	for i := 0; i < loginIPBudget; i++ {
		require.NoError(t, lim.begin("10.0.0.1", "alice"), "post-reset attempt %d must pass", i+1)
	}
	requireRateLimited(t, lim.begin("10.0.0.1", "alice"))
}

func TestLoginLimiterDualWindow(t *testing.T) {
	lim, clock := newTestLoginLimiter()
	for i := 0; i < loginUsernameBudget; i++ {
		require.NoError(t, lim.begin("10.0.0.1", "alice"))
	}
	// IP 窗（1m）滑过后，账号窗（5m）仍在 → 仍拒（换/等短窗不放行）。
	clock.advance(loginIPWindow + time.Second)
	requireRateLimited(t, lim.begin("10.0.0.1", "alice"))
	// 账号窗也滑过 → 恢复。
	clock.advance(loginUsernameWindow)
	require.NoError(t, lim.begin("10.0.0.1", "alice"))
}

func TestLoginLimiterDimensionsAreIndependent(t *testing.T) {
	lim, _ := newTestLoginLimiter()
	for i := 0; i < loginUsernameBudget; i++ {
		require.NoError(t, lim.begin("10.0.0.1", "alice"))
	}
	requireRateLimited(t, lim.begin("10.0.0.1", "alice"))

	// 用户名维度跨来源：换 IP 不能继续猜同一账号。
	requireRateLimited(t, lim.begin("10.0.0.2", "alice"))

	// 来源维度与用户名维度互不污染：新来源 + 新用户名不受影响。
	require.NoError(t, lim.begin("10.0.0.2", "bob"))
	require.NoError(t, lim.begin("10.0.0.3", "carol"))
}

func TestLoginLimiterBucketsAreBoundedAndSwept(t *testing.T) {
	lim, clock := newTestLoginLimiter()
	// 同一时刻大量唯一键：触顶后新键落 overflow 桶，表大小严格有界。
	for i := 0; i < loginMaxBuckets*2; i++ {
		_ = lim.begin(fmt.Sprintf("10.0.%d.%d", i/256, i%256), fmt.Sprintf("user-%d", i))
	}
	assert.LessOrEqual(t, len(lim.buckets), loginMaxBuckets, "bucket table must stay bounded")
	assert.Contains(t, lim.buckets, "overflow:ip")
	assert.Contains(t, lim.buckets, "overflow:user")

	// 过期惰性清扫：推进超过最长窗口后，一次 begin 把过期精确键清掉，
	// 只余常驻 overflow 与新键。
	clock.advance(loginUsernameWindow + time.Second)
	require.NoError(t, lim.begin("10.9.9.9", "fresh"))
	assert.LessOrEqual(t, len(lim.buckets), 4, "expired buckets must be swept lazily")
}
