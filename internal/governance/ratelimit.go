// Package governance 承载治理刹车执法面（ADR-0017 附录 A，F1.9）：创建
// 速率限制（per-Token 固定窗、内存计数）与变更冻结窗（change freeze
// 拦截器）。拦截器形态对齐 internal/idem：一份实现覆盖整个执法面，装配
// 服务器与 apitest 夹具共用同链（assembly.NewInterceptors 单一真源）。
package governance

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/idem"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 速率缺省（ADR-0017 附录 A.2）：60s 固定窗、每 Token 120 次。控制面
// 单进程（SQLite 单写者）是 v1 部署事实——计数器住内存，无跨进程协同
// 需求；重启清零（诚实边界：重启 = 窗口重置，重启是运维权力非攻击面）。
const (
	DefaultRateWindow = 60 * time.Second
	DefaultRateBudget = 120
)

// tokenBucket 是单 Token 的固定窗计数（windowStart + 已用次数）。
type tokenBucket struct {
	windowStart time.Time
	used        int
}

// RateLimiter 是 per-Token 创建速率限制器。动词面与幂等执法面同源
// （idem.EnforcedMethods 编译期引用——创建型动词单一清单已由 ADR-0024
// 建立，豁免联动随豁免条目走）。计数口径：仅"实际执行"计数——拦截器
// 位于幂等执法器之后（重放不达此处）、被拒请求不消耗预算、无 Token
// 身份的调用（webhook 接收面等 PUBLIC 动词）不计数。
type RateLimiter struct {
	clock  state.Clock
	window time.Duration
	budget int

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// NewRateLimiter 构造限制器（window/budget 非正值回退缺省；测试注入
// 假时钟与缩小的窗）。
func NewRateLimiter(clock state.Clock, window time.Duration, budget int) *RateLimiter {
	if window <= 0 {
		window = DefaultRateWindow
	}
	if budget <= 0 {
		budget = DefaultRateBudget
	}
	return &RateLimiter{clock: clock, window: window, budget: budget, buckets: map[string]*tokenBucket{}}
}

// Unary 返回拦截器：非创建型动词 / 无 Token 身份原样放行。计数发生在
// handler 执行之前（受理与否都算一次创建尝试——受理拒绝同样是失控信号）。
func (l *RateLimiter) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !idem.EnforcedMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		id, ok := authn.FromContext(ctx)
		if !ok || id.TokenID == "" {
			return handler(ctx, req)
		}
		if err := l.allow(id.TokenID); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// allow 判定并计数。超限返回 E_RATE_LIMITED：信封带 retry_after_seconds
// 上下文，status 附加 RetryInfo detail（REST 429 的 Retry-After 头由此
// 生成）。桶 map 以 TokenID 为键：进程生命期内 Token 总量小（小团队口径），
// 吊销后残留桶仅数十字节，不做主动清扫。
func (l *RateLimiter) allow(tokenID string) error {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[tokenID]
	if !ok || now.Sub(b.windowStart) >= l.window {
		l.buckets[tokenID] = &tokenBucket{windowStart: now, used: 1}
		return nil
	}
	if b.used >= l.budget {
		wait := l.window - now.Sub(b.windowStart)
		secs := int64(math.Ceil(wait.Seconds()))
		if secs < 1 {
			secs = 1
		}
		return apperr.New("E_RATE_LIMITED",
			"token %s has issued %d create requests in the current window (limit %d per %s)",
			tokenID, b.used, l.budget, l.window).
			WithContext("retry_after_seconds", strconv.FormatInt(secs, 10)).
			WithRetryAfter(time.Duration(secs) * time.Second)
	}
	b.used++
	return nil
}
