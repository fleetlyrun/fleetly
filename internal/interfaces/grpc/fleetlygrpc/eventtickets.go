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
	"encoding/json"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 接口实现锚：confirm 票面消费端口（governance 拦截器与 engine 共用
// 本票据店实例——单进程票据店语义）。
var _ governance.ConfirmTicketStore = (*eventTicketStore)(nil)

// eventTicketTTL 是票据存活窗（秒级；兑换即删）。
const eventTicketTTL = 60 * time.Second

// 票据用途（兑换路径绑定面）。
const (
	ticketPurposeEvents  = "events"  // SSE 事件流（ADR-0026 原面）
	ticketPurposeExec    = "exec"    // exec 会话 WS 流（ADR-0049）
	ticketPurposeBrowse  = "browse"  // browse 会话 entry 兑换（ADR-0051）
	ticketPurposeConfirm = "confirm" // 变更确认票（B1，设计稿 §1.4 甲'；peek/烧两步原语 + tombstone，零 redeem）
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
	// confirmTombstones 是 confirm 票烧毁后的短窗已消费标记（token →
	// tombstone 过期时刻）：使「已确认过」与「过期/伪造」可分辨（不同
	// 辨因 → 不同错误信息，设计稿 §1.4 烧票契约 #4）。
	confirmTombstones map[string]time.Time
}

func newEventTicketStore(clock state.Clock) *eventTicketStore {
	return &eventTicketStore{clock: clock, tickets: map[string]ticketEntry{}, confirmTombstones: map[string]time.Time{}}
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

// redeemPayload 单用途兑换并回带面内绑定锚（events 票据把 Team 可见域
// 铸在 payload——兑换面无身份 ctx，过滤锚以铸造时烙印为准，P0-2）：命中、
// 未过期、purpose 匹配即删并放行；其余一律拒绝（语义同 redeem，对匿名面
// 只呈现"票据无效"）。
func (s *eventTicketStore) redeemPayload(purpose, token string) (string, bool) {
	if token == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tickets[token]
	if !ok {
		return "", false
	}
	if e.purpose != purpose || !s.clock.Now().Before(e.expiry) {
		return "", false
	}
	delete(s.tickets, token)
	return e.payload, true
}

// sweepLocked 顺带清理过期票据（铸造路径惰性执行，无独立 janitor 面）。
// confirm 的 tombstone 短窗同扫（窗口过即彻底遗忘——「已确认过」只需在
// 重发高发窗内可辨因）。
func (s *eventTicketStore) sweepLocked() {
	now := s.clock.Now()
	for t, e := range s.tickets {
		if now.After(e.expiry) {
			delete(s.tickets, t)
		}
	}
	for t, exp := range s.confirmTombstones {
		if now.After(exp) {
			delete(s.confirmTombstones, t)
		}
	}
}

// issueWithTTL 铸造指定 TTL 的票据（browse 面 120s——冷启动余量；ADR-0051
// 决策 2：exec 60s 不够浏览器首次拉镜像 + 用户点击的窗口）。
func (s *eventTicketStore) issueWithTTL(purpose, payload string, ttl time.Duration) (string, time.Duration, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	s.mu.Lock()
	s.sweepLocked()
	s.tickets[token] = ticketEntry{expiry: s.clock.Now().Add(ttl), purpose: purpose, payload: payload}
	s.mu.Unlock()
	return token, ttl, nil
}

// ---- Confirm Ticket 原语（B1，设计稿 §1.4 甲'）----
//
// 与既有三 purpose 的 redeem（兑换即删单步）分立：confirm 票是两步原语
// ——peek（验不删）与 discard（烧）。验票/烧票分置两段分工（拦截器通用
// 验票 peek → engine 受理临界区内容绑定后再 peek + discard），redeem 单步
// 形态会把「admission 拒（queue full）」也变成烧票（违反烧票契约：冻结
// 成功而 admission 拒时票保留可重试）。

// confirmTicketTTL 是 confirm 票存活窗（设计稿 §1.4：600s）。
const confirmTicketTTL = 600 * time.Second

// confirmTombstoneTTL 是烧毁后的已消费标记保留窗（设计稿 §1.4：短窗；
// 120s 覆盖并发同票第二请求与幂等降级窗的重发高发段）。
const confirmTombstoneTTL = 120 * time.Second

// issueConfirm 铸造一枚 confirm 票（结构化票面 JSON 序列化进 payload——
// 按票据店既有 payload 机制适配；PreviewDeploy/PreviewTeardown 的铸票
// 调用归 W2a，本原语同时是 W2a 与测试的铸造面）。
func (s *eventTicketStore) issueConfirm(payload governance.ConfirmTicketPayload) (string, time.Duration, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	return s.issueWithTTL(ticketPurposeConfirm, string(body), confirmTicketTTL)
}

// MintConfirmTicket 铸造一枚 confirm 票（W2a 预览管线与测试的铸造面；
// 消费端口 = governance.ConfirmTicketStore，两段分工共享本店单实例）。
func (s *Services) MintConfirmTicket(payload governance.ConfirmTicketPayload) (string, time.Duration, error) {
	return s.eventTickets.issueConfirm(payload)
}

// PeekConfirm 验票不删：命中、未过期、purpose=confirm 返回票面与 Hit；
// 在册但过期返回 Expired（时间维度不新鲜，与伪造分腿）；命中 tombstone
// 返回 Consumed（已确认过）；其余 Unknown（不存在/跨面票——沿票据店
// 「不区分理由」纪律，与 Consumed 的互辨才是 tombstone 的目的）。
func (s *eventTicketStore) PeekConfirm(ticket string) (governance.ConfirmTicketPayload, governance.ConfirmPeekStatus) {
	var zero governance.ConfirmTicketPayload
	if ticket == "" {
		return zero, governance.ConfirmPeekUnknown
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, burned := s.confirmTombstones[ticket]; burned {
		if s.clock.Now().Before(exp) {
			return zero, governance.ConfirmPeekConsumed
		}
		// 窗过即忘（惰性清理，同 sweep 纪律）：「已确认过」只在重发高发
		// 窗内可辨因，窗外与伪造不可分——沿票据店「不区分理由」纪律。
		delete(s.confirmTombstones, ticket)
	}
	e, ok := s.tickets[ticket]
	if !ok || e.purpose != ticketPurposeConfirm {
		return zero, governance.ConfirmPeekUnknown
	}
	if !s.clock.Now().Before(e.expiry) {
		return zero, governance.ConfirmPeekExpired
	}
	var payload governance.ConfirmTicketPayload
	if err := json.Unmarshal([]byte(e.payload), &payload); err != nil {
		// 票面损坏按伪造拒绝（铸造面单源序列化，损坏即异常态）。
		return zero, governance.ConfirmPeekUnknown
	}
	return payload, governance.ConfirmPeekHit
}

// DiscardConfirm 烧票：命中、未过期、purpose=confirm 即删并落 tombstone
// （confirmTombstoneTTL 短窗）；返回是否真烧。已烧/过期/不存在 = false
// 且不重复落 tombstone（幂等：第二烧不刷新窗口）。
func (s *eventTicketStore) DiscardConfirm(ticket string) bool {
	if ticket == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tickets[ticket]
	if !ok || e.purpose != ticketPurposeConfirm || !s.clock.Now().Before(e.expiry) {
		return false
	}
	delete(s.tickets, ticket)
	s.confirmTombstones[ticket] = s.clock.Now().Add(confirmTombstoneTTL)
	return true
}

// ConfirmTickets 暴露 confirm 票据面（组合根接线用：assembly 把同一票据
// 店实例注入 governance.ConfirmGuard 拦截器与 engine 受理临界区——两段
// 分工共享单店，否则拦截器验的票与 engine 烧的票分属两个世界）。
func (s *Services) ConfirmTickets() governance.ConfirmTicketStore {
	return s.eventTickets
}
