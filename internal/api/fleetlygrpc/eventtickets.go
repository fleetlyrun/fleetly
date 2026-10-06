package fleetlygrpc

// eventTicketStore 是一次性短时票据（ADR-0026；F3.2 泛化为 purpose+payload
// 绑定，ADR-0049 决策 3）：浏览器 EventSource/WebSocket 不能设自定义头——
// Bearer 先换票据（秒级 TTL、单用途、限兑换路径），流入口以 query 参数
// 携带。泄漏面受控：进访问日志的暴露窗口 = TTL 秒级、不可重放、不可作
// 他用（不是 Bearer）；purpose 校验使 events 票据不能兑换 exec 流（跨面
// 重放拒绝）。
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

// 票据用途（兑换路径绑定面）。
const (
	ticketPurposeEvents = "events" // SSE 事件流（ADR-0026 原面）
	ticketPurposeExec   = "exec"   // exec 会话 WS 流（ADR-0049）
)

// ticketEntry 是一枚在册票据。
type ticketEntry struct {
	expiry  time.Time
	purpose string
	payload string // 面内绑定锚（exec = session ID；空 = 无绑定）
}

// eventTicketStore 是票据铸造/兑换面。
type eventTicketStore struct {
	mu      sync.Mutex
	clock   state.Clock
	tickets map[string]ticketEntry
}

func newEventTicketStore(clock state.Clock) *eventTicketStore {
	return &eventTicketStore{clock: clock, tickets: map[string]ticketEntry{}}
}

// issue 铸造一枚新票据（crypto/rand 32 字节 base64url；purpose+payload
// 兑换时校验）。
func (s *eventTicketStore) issue(purpose, payload string) (string, time.Duration, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	s.sweepLocked()
	s.tickets[token] = ticketEntry{expiry: s.clock.Now().Add(eventTicketTTL), purpose: purpose, payload: payload}
	s.mu.Unlock()
	return token, eventTicketTTL, nil
}

// redeem 单用途兑换：命中、未过期、purpose/payload 匹配即删并放行；
// 其余一律拒绝（不区分——对匿名面只呈现"票据无效"）。消费只在成功
// 兑换时发生：载荷不匹配/过期的尝试不烧票据（错会话试兑不构成对真会
// 话的拒绝服务）。
func (s *eventTicketStore) redeem(purpose, payload, token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tickets[token]
	if !ok {
		return false
	}
	if e.purpose != purpose || e.payload != payload || !s.clock.Now().Before(e.expiry) {
		return false
	}
	delete(s.tickets, token)
	return true
}

// sweepLocked 顺带清理过期票据（铸造路径惰性执行，无独立 janitor 面）。
func (s *eventTicketStore) sweepLocked() {
	now := s.clock.Now()
	for t, e := range s.tickets {
		if now.After(e.expiry) {
			delete(s.tickets, t)
		}
	}
}
