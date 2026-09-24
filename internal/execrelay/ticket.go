package execrelay

// 一次性终端接入 ticket（设计 §2.5「浏览器无自定义 WS 头的诚实解」）：
// POST /v1/terminal/tickets（Bearer + terminal scope）签发 → 60s 过期、单
// 次使用、绑（token, app, service）三元组 → GET /v1/terminal?ticket=...
// WS 升级时消费。长效 token 不进 URL——ticket 是 128bit 随机一次性凭据。
//
// 存储 = 进程内存表（migrations 无涉——设计验收标准 3「连接表/ticket 在
// 内存」）；过期清扫惰性（Redeem/Create 顺手清）+ 定向保底（容量水位触发）。

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ticket 哨兵错误（api 面映射：Invalid → 退化 401；Used/Expired → 退化
// 409/401——一次性语义的重放拒绝）。
var (
	// ErrTicketInvalid 是 ticket 不存在（含伪造）。
	ErrTicketInvalid = errors.New("execrelay: terminal ticket invalid")
	// ErrTicketExpired 是 ticket 已过 60s 有效窗。
	ErrTicketExpired = errors.New("execrelay: terminal ticket expired")
	// ErrTicketUsed 是 ticket 已被消费（一次性——重放拒绝）。
	ErrTicketUsed = errors.New("execrelay: terminal ticket already used")
)

// ticketTTL 是 ticket 有效窗（设计 §2.5 原文 60s；TicketStore.ttl 注入缝
// ——单测缩短驱动过期路径）。
const ticketTTL = TicketTTLSeconds * time.Second

// TicketTTLSeconds 是 ticket 有效窗秒数（响应投影/文档锚——与 ticketTTL
// 同源单一事实）。
const TicketTTLSeconds = 60

// ticketMaxLive 是内存表的水位上限（清扫触发阈值——防御性：签发面在
// terminal scope 之后，恶意灌表的空间已被 scope 收敛；此为第二道闸）。
const ticketMaxLive = 4096

// TicketBinding 是 ticket 的绑定三元组 + 消费上下文。
type TicketBinding struct {
	// Ticket 是凭据本体（回读给测试/日志脱敏面——生产出口只经签发响应）。
	Ticket string
	// TokenID 是签发者 API token（并发限额与审计 actor 的键）。
	TokenID string
	// App / Service 是目标（WS 升级时控制面据此选 task——用户不选容器）。
	// App 是请求侧应用名（展示/审计面）；Swarm 服务名推导用 AppLabel
	//（v0.3 三段限定形 team/prj/app——rbac-teams §4.3，签发面已解析）。
	App       string
	AppLabel  string
	Service   string
	// Expires 是过期时刻（UTC）。
	Expires time.Time
}

// SwarmAppLabel 返回服务名推导用的 app 标识（限定形优先——存量绑定无
// AppLabel 时回落裸名，兼容单测直构形态）。
func (b TicketBinding) SwarmAppLabel() string {
	if b.AppLabel != "" {
		return b.AppLabel
	}
	return b.App
}

// TicketStore 是一次性 ticket 内存表。
type TicketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	tickets map[string]TicketBinding
}

// NewTicketStore 构造（ttl<=0 回落 ticketTTL）。
func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = ticketTTL
	}
	return &TicketStore{ttl: ttl, now: time.Now, tickets: make(map[string]TicketBinding)}
}

// Create 签发一张 ticket（绑 token+app+service；60s 后过期——设计原文）。
// app 标识 = 裸名（SwarmAppLabel 回落形态；生产签发走 CreateLabeled）。
func (ts *TicketStore) Create(tokenID, app, service string) TicketBinding {
	return ts.CreateLabeled(tokenID, app, "", service)
}

// CreateLabeled 签发带限定形 app 标识的 ticket（appLabel = team/prj/app，
// v0.3 流标签口径——hub 侧 Swarm 服务名推导的参数源）。
func (ts *TicketStore) CreateLabeled(tokenID, app, appLabel, service string) TicketBinding {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sweepLocked()
	for len(ts.tickets) >= ticketMaxLive {
		// 水位保底：清最旧（到期时间最早）一张再放行——签发永不因内存表
		// 满而失败（失败的签发会卡 Console 流程；丢的是最老的一张待用票）。
		oldest := ""
		var oldestExp time.Time
		for k, v := range ts.tickets {
			if oldest == "" || v.Expires.Before(oldestExp) {
				oldest, oldestExp = k, v.Expires
			}
		}
		delete(ts.tickets, oldest)
	}
	b := TicketBinding{
		Ticket:   newTicketSecret(),
		TokenID:  tokenID,
		App:      app,
		AppLabel: appLabel,
		Service:  service,
		Expires:  ts.now().Add(ts.ttl),
	}
	ts.tickets[b.Ticket] = b
	return b
}

// Redeem 消费 ticket（一次性）：未命中/过期/已用各自报错——调用方统一
// 对外 401/409 文案，不区分「不存在」与「已用」（不泄漏状态细节）。
func (ts *TicketStore) Redeem(ticket string) (TicketBinding, error) {
	if ticket == "" {
		return TicketBinding{}, ErrTicketInvalid
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b, ok := ts.tickets[ticket]
	if !ok {
		return TicketBinding{}, ErrTicketUsed
	}
	// 一次性语义：先摘表再校验过期——同 ticket 的并发二连必有一个落空
	//（map delete 即消费点，单锁内原子）。
	delete(ts.tickets, ticket)
	if ts.now().After(b.Expires) {
		return TicketBinding{}, ErrTicketExpired
	}
	return b, nil
}

// Len 是在表 ticket 数（测试/状态面）。
func (ts *TicketStore) Len() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.tickets)
}

// sweepLocked 清过期票（调用方持锁）。
func (ts *TicketStore) sweepLocked() {
	now := ts.now()
	for k, v := range ts.tickets {
		if now.After(v.Expires) {
			delete(ts.tickets, k)
		}
	}
}

// newTicketSecret 铸造 128bit URL-safe 随机凭据（ulid 参与——同一纳秒内
// 的两次 rand 碰撞无意义，此处纯 rand 已足；ID 前缀提升日志可读性）。
func newTicketSecret() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand 失败是平台级故障（宁可用 ULID 兜底也不空串签发——
		// 实际不可达路径，防御式回落）。
		return "tkt_" + ulid.Make().String()
	}
	return "tkt_" + base64.RawURLEncoding.EncodeToString(raw)
}
