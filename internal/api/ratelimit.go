package api

import (
	"sync"
	"time"
)

// rateLimiter 是 per-token 令牌桶（T2.17 最小实现：默认宽松、防滥用为
// 主——不是准入控制的业务语义）。桶按 token ID 建立；长期不活跃桶在表
// 超阈值时惰性清除。
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // 每秒补充令牌数
	burst   float64 // 桶容量
	now     func() time.Time
}

// 默认限流参数（宽松：正常交互远达不到；防脚本失控/重放风暴）。
const (
	defaultRatePerSec = 50
	defaultBurst      = 200
	// maxBuckets 是惰性清理阈值（单机规模 token 数远小于此）。
	maxBuckets = 10000
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(ratePerSec, burst int) *rateLimiter {
	if ratePerSec <= 0 {
		ratePerSec = defaultRatePerSec
	}
	if burst <= 0 {
		burst = defaultBurst
	}
	return &rateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    float64(ratePerSec),
		burst:   float64(burst),
		now:     time.Now,
	}
}

// allow 消费一个令牌（不足拒绝）。
func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) >= maxBuckets {
		l.pruneLocked(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked 清除长期不活跃桶（满桶时间 = idle 期内不可能积累出消费能力
// 之外的状态，可安全丢弃）。
func (l *rateLimiter) pruneLocked(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last) > time.Duration(l.burst/l.rate)*time.Second+time.Minute {
			delete(l.buckets, k)
		}
	}
}
