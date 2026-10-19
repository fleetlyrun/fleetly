package fleetlygrpc

// Login 口令面进程内固定窗限速（R14-M1 限速半边，S13）。定位：纵深防御
// 的一层，不是唯一防线——真实部署仍须外部入口限流（WAF/反向代理/网关）
// 与"控制面只在内网监听"的暴露约束；本限速器不跨副本、重启清零、不依赖
// 外部存储（控制面单进程 + SQLite 单写者是 v1 部署事实，与 governance
// 的 per-Token 限速同款边界）。
//
// 两个维度同时执法（任一超限即拒）：
//   - per-source：按 gRPC peer IP（取不到 peer 时退化到 x-forwarded-for
//     首段/全局 unknown 桶）——抑制单机爆破与撞库；
//   - per-username：归一化用户名（trim + lower）——抑制换源 IP 后对同一
//     账号的持续猜测（撞库的稀缺资源是账号而非来源）。
//
// 计数口径：begin 先于凭证校验占位（并发突发不会集体穿过"检查后再加"的
// 窗口），凭证通过即 reset 清零两桶——净效果是"只对失败尝试计数，成功
// 登录清零"。被限速拒绝的请求不再消耗计数（固定窗，不强延长）。
//
// 内存有界：桶表上限 loginMaxBuckets，惰性清扫过期桶；仍满时新键落到
// 维度共享的 overflow 桶（照常扣预算，不因造键获得无限尝试，也不让进程
// 内存随随机用户名/IP 无界增长）。

import (
	"context"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

const (
	// 单来源 5 次失败/1 分钟：覆盖正常手误（一次登录通常 ≤2 次重试），
	// 又把单机在线猜测压到约 7200 次/天——对高熵口令远不足以构成可行
	// 搜索；窗口短，共享 NAT 的团队在窗口滑过后即可恢复，不会整队锁死。
	loginIPWindow = time.Minute
	loginIPBudget = 5
	// 单账号 5 次失败/5 分钟：窗口长于 IP 桶，换源 IP 不能重置同一账号
	// 的预算（约 1440 次/天/账号）；成功登录清零，正常用户仍有自助恢复
	// 空间。
	loginUsernameWindow = 5 * time.Minute
	loginUsernameBudget = 5
	// 桶表上限（有界内存）：4096 个键 ≈ 亚 MB 级；登录面低频，正常规模
	// 远达不到。攻击者以随机用户名/来源冲表时靠惰性清扫 + overflow 桶
	// 兜底（见文件头）。
	loginMaxBuckets = 4096
	// 惰性清扫节流：至多每 30s 全表扫一次过期桶；调用频率低，摊销可忽略。
	loginSweepInterval = 30 * time.Second
)

// loginBucket 是单个键的固定窗计数。
type loginBucket struct {
	window time.Duration
	start  time.Time
	used   int
}

// loginLimiter 是 Login 口令面的双桶限速器（clock 注入——单测用假时钟
// 驱动窗口推进；生产注入组合根时钟）。
type loginLimiter struct {
	clock state.Clock

	mu        sync.Mutex
	buckets   map[string]*loginBucket
	lastSweep time.Time
}

// newLoginLimiter 构造限速器（Services 构造期调用一次）。overflow 桶预置
// 且不参与清扫：桶表触顶时新键有现成去处，表大小严格不超过 loginMaxBuckets。
func newLoginLimiter(clock state.Clock) *loginLimiter {
	return &loginLimiter{
		clock: clock,
		buckets: map[string]*loginBucket{
			"overflow:ip":   {window: loginIPWindow},
			"overflow:user": {window: loginUsernameWindow},
		},
	}
}

// begin 在一次登录尝试前占位：两桶都未超限时各计一次并放行；任一超限
// 返回 E_RATE_LIMITED（带 retry_after_seconds 与 RetryInfo 语义，REST
// 429 的 Retry-After 由此生成）。retry 取两桶剩余期的较大者——任一维度
// 先解锁都不能放行。
func (l *loginLimiter) begin(source, username string) error {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, false)

	ip := l.bucketLocked("ip:"+source, "overflow:ip", loginIPWindow, now)
	user := l.bucketLocked("user:"+username, "overflow:user", loginUsernameWindow, now)

	var wait time.Duration
	blocked := false
	if ip.used >= loginIPBudget {
		blocked = true
		wait = maxDuration(wait, ip.window-now.Sub(ip.start))
	}
	if user.used >= loginUsernameBudget {
		blocked = true
		wait = maxDuration(wait, user.window-now.Sub(user.start))
	}
	if blocked {
		return loginRateLimited(wait)
	}
	ip.used++
	user.used++
	return nil
}

// reset 在凭证通过后清零两桶（成功登录不消耗预算）。只删精确键：overflow
// 桶是共享的攻击流量计数，不因单次成功而清空。两键可能因桶表满映射到
// overflow，此时精确键不存在，delete 无副作用。
func (l *loginLimiter) reset(source, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, "ip:"+source)
	delete(l.buckets, "user:"+username)
}

// bucketLocked 取键的当前窗口桶（过期就地重置；缺键则新建）。桶表满时
// 先强制清扫过期项，仍满则退到该维度的共享 overflow 桶——保证表大小
// 有限且新键仍扣同一份预算。
func (l *loginLimiter) bucketLocked(key, overflowKey string, window time.Duration, now time.Time) *loginBucket {
	if b, ok := l.buckets[key]; ok {
		if now.Sub(b.start) >= b.window {
			b.start, b.used = now, 0
		}
		return b
	}
	if len(l.buckets) >= loginMaxBuckets {
		l.sweepLocked(now, true)
		if len(l.buckets) >= loginMaxBuckets {
			key = overflowKey
		}
	}
	if b, ok := l.buckets[key]; ok {
		if now.Sub(b.start) >= b.window {
			b.start, b.used = now, 0
		}
		return b
	}
	b := &loginBucket{window: window, start: now}
	l.buckets[key] = b
	return b
}

// sweepLocked 惰性清理过期桶（force=false 时节流到 loginSweepInterval；
// force=true 用于桶表触顶时的即时腾位）。
func (l *loginLimiter) sweepLocked(now time.Time, force bool) {
	if !force && !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < loginSweepInterval {
		return
	}
	l.lastSweep = now
	for key, b := range l.buckets {
		if key == "overflow:ip" || key == "overflow:user" {
			continue // 预置兜底桶常驻（见 newLoginLimiter）
		}
		if now.Sub(b.start) >= b.window {
			delete(l.buckets, key)
		}
	}
}

// loginRateLimited 铸超限错误：复用既有 E_RATE_LIMITED（ResourceExhausted
// → 429；不新增 errcode），suggestion 按口令面改写（注册表默认文案面向
// 创建请求）。
func loginRateLimited(wait time.Duration) error {
	secs := int64(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return apperr.New("E_RATE_LIMITED",
		"too many failed sign-in attempts for this source or account; retry after %d seconds", secs).
		WithContext("retry_after_seconds", strconv.FormatInt(secs, 10)).
		WithRetryAfter(time.Duration(secs) * time.Second).
		WithSuggestion("Wait for retry_after_seconds before retrying; a successful sign-in resets both counters.")
}

// loginSource 解析限速的来源维度：gRPC peer 地址优先（真实部署恒有）；
// 无 peer（进程内直调/测试传输）退化到元数据 x-forwarded-for 首段，再退
// 化到全局 unknown 桶。peer 在场时绝不采信可伪造的转发头。
func loginSource(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host := hostOfAddr(p.Addr.String()); host != "" {
			return host
		}
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, v := range md.Get("x-forwarded-for") {
			first, _, _ := strings.Cut(v, ",")
			if host := strings.TrimSpace(first); host != "" {
				return host
			}
		}
	}
	return "unknown"
}

// hostOfAddr 取 host:port 的 host（无端口的形态——bufconn 等——原样返回）。
func hostOfAddr(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// normalizeLoginUsername 是用户名桶的归一化：trim + lower（大小写变体
// 不产生平行预算；实际用户行匹配仍走原值 GetByName）。
func normalizeLoginUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// maxDuration 返回较大者（窗口剩余期取两桶最大值）。
func maxDuration(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}
