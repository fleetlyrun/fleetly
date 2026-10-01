package fleetlygrpc

// eventTicketStore 是 SSE 订阅路径的一次性短时票据（ADR-0026）：浏览器
// EventSource 不能设自定义头——Bearer 先换票据（秒级 TTL、单用途、限
// 订阅路径），SSE 以 query 参数携带。泄漏面受控：进访问日志的暴露窗口
// = TTL 秒级、不可重放、不可作他用（不是 Bearer）。
//
// 进程内存储（map + 互斥）：控制面单实例（架构 §8 HA 口径诚实暴露），
// 重启即清空——票据寿命本就是秒级，无持久化必要。

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// eventTicketTTL 是票据存活窗（秒级；兑换即删）。
const eventTicketTTL = 60 * time.Second

// eventTicketStore 是票据铸造/兑换面。
type eventTicketStore struct {
	mu      sync.Mutex
	clock   state.Clock
	tickets map[string]time.Time // token → 过期时刻
}

func newEventTicketStore(clock state.Clock) *eventTicketStore {
	return &eventTicketStore{clock: clock, tickets: map[string]time.Time{}}
}

// issue 铸造一枚新票据（crypto/rand 32 字节 base64url）。
func (s *eventTicketStore) issue() (string, time.Duration, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	s.sweepLocked()
	s.tickets[token] = s.clock.Now().Add(eventTicketTTL)
	s.mu.Unlock()
	return token, eventTicketTTL, nil
}

// redeem 单用途兑换：命中且未过期即删并放行；未命中/过期/重复使用一律
// 拒绝（不区分——对匿名面只呈现"票据无效"）。
func (s *eventTicketStore) redeem(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.tickets[token]
	if !ok {
		return false
	}
	delete(s.tickets, token)
	return s.clock.Now().Before(expiry)
}

// sweepLocked 顺带清理过期票据（铸造路径惰性执行，无独立 janitor 面）。
func (s *eventTicketStore) sweepLocked() {
	now := s.clock.Now()
	for t, expiry := range s.tickets {
		if now.After(expiry) {
			delete(s.tickets, t)
		}
	}
}
