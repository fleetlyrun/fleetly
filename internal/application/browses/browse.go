// Package browses 是 Browse 会话域卫星（F3.6，ADR-0051）：per-Database
// 数据浏览器按需实例。会话非资源行（exec 同语义——受理回显即全部读面），
// 回收台账持久化在 browse_sessions 行（browse 载体是外部活体，daemon
// 重启后注册表丢失而载体仍在跑——行是重注册与到点回收的锚）。数据流：
// 受理（api 层四件一拍）→ RegisterBrowseSession（注册表 + Kick）→
// 收敛环投影浏览器 Workload（方言单源 dbbrowser；挂 Database 所在
// Project 全部活跃网；材料经 SecretFiles）→ ephemeral 双 Route 合并进
// publishRoutes。
//
// 回收：硬 TTL 30min + 空闲 10min（entry/authorize 接触即续活——门禁
// 流量就是活性信号）→ Runtime.Remove + 删行。重启后 grant 重铸（旧
// cookie 失效，用户重开——诚实行为，不做 cookie 迁移）。
//
// 文件分组（P3 批 4）：本文件留受理/grant/路由发布面；收敛环（Step
// 对账族）见 browseloop.go。
//
// P4 wave-2a（ADR-0088 决策 2/3）：Browse 域接收者下域。P5 wave-3
// （ADR-0088 决策 2 P5 拆包形态，2026-11-01）：嵌入已解开——卫星独立成
// 包（internal/application/browses），核经构造注入持有（construct.go 装配
// 卫星 New）；gRPC Browse 受理/grant 面与 routepublish 的 ephemeral 路由
// 合并面经核门面委托（消费点零改）；memo 协议契约（Memo/EnsureFresh/
// Remember/Forget）在 internal/application/engine/contract。禁回引
// *engine.Engine（enginebackref 跨包执法）。
package browses

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// browse 限额常量（ADR-0051 决策 1；编译期常量先例 = execMaxSessionsPerTeam 族）。
const (
	// BrowseMaxSessionsPerTeam 是 per-Team 并发 browse 会话上限（浏览器
	// 载体是常驻容器，比 exec 会话更重——上限取 exec 的一半；api 受理面
	// 的拒绝文案共用本常量）。
	BrowseMaxSessionsPerTeam = 4
	// BrowseHardTTL 是会话硬时效（受理起算；api 层铸台账行的 expires_at
	// 共用本常量）。
	BrowseHardTTL = 30 * time.Minute
)

// browse 域哨兵（api 层映射稳定 errcode）。
var (
	// ErrBrowseQuota 是 per-Team 并发 browse 会话超限。
	ErrBrowseQuota = errors.New("too many concurrent browse sessions for team")
	// ErrBrowseSessionNotFound 是会话不存在或已收口（entry/authorize 面）。
	ErrBrowseSessionNotFound = errors.New("browse session not found")
)

// BrowseConfig 是 browse 面的配置快照（engine Options.Data.Browse 注入；
// HostSuffix 或 GatewayURL 空 = 面停用，ADR-0051 决策 5）。P5 wave-3
// 随域拆包迁入——核侧经类型别名零漂移（options/assembly 构造面零改）。
type BrowseConfig struct {
	HostSuffix string
	GatewayURL string
	TLSMode    string // none（缺省）| auto
}

// Configured 报告 browse 面是否装配可用。
func (c BrowseConfig) Configured() bool { return c.HostSuffix != "" && c.GatewayURL != "" }

// Browses 是 Browse 会话域卫星实例态（活体注册表；行是台账）。P3 批 4
// 收编：browseDomain → browses（复数普通名词零后缀，ADR-0075 命名法则）；
// P4 wave-2a 嵌入改形（ADR-0088 决策 2/3）→ P5 wave-3 拆包（决策 2 P5
// 形态）：接收者域内短名 b（browser 局部改名 browser 防活体遮蔽，
// backups=bk 同款裁定）；依赖显式逐字段 + 机制端口注入（construct.go
// 装配）。memo 表 ensure 随域持有（map 本体在组合根铸造注入——
// metastatewrite D3 的「表只在组合根/meta init 出生」纪律）。禁挂
// *Engine 回引（enginebackref 守卫执法）。
type Browses struct {
	mu       sync.Mutex
	sessions map[string]*browseSession
	// ensure 是 per-会话 Ensure 签名备忘（ensureMemo 协议；键 = 会话 ID；
	// 协议契约在 contract.Memo——ensureMu 单锁留核）。
	ensure map[string]contract.Memo

	// 显式依赖（实测消费面）：runtime 是载体动词消费面（Ensure/Remove/
	// Addresses——核副本双持，P5 过渡形态）；repos 是台账与依赖行读
	//（Browse 台账/Databases 库名回读/Secrets 凭证解封，transact.Stores
	// 整体注入，ADR-0088 决策 3）；cipher 是凭证 Secret 解封（核副本双持
	//——解析面/Secret 面等核消费者仍读核字段）；log/clock 域内日志与节拍。
	runtime capability.Runtime
	repos   transact.Stores
	cipher  *material.Cipher
	log     *slog.Logger
	clock   state.Clock
	// browseConfig 是 Browse 面配置的事实端口（闭包逐拍读核 opts.Data.
	// Browse——live 读语义，通则五：卫星不持 Options 副本；HostSuffix/
	// GatewayURL/TLSMode 三字段的单点快照面）。
	browseConfig func() BrowseConfig

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// lockMaintenance 是 maintenance 端口读半边（Ensure/Remove 与网络重建
	// 串行化，ADR-0046；端口名对齐锁入口方法名集——maintenanceguard 判据
	// 继续命中；签名零 engine 类型=基础形态直持，无须命名下沉）；
	// boundedStep 是步带界端口（ManagedStepTimeout 事实面，ensure.go
	// 统一入口，签名纯 context=基础形态直持）；ensureFresh/ensureRemember/
	// ensureForget 是 memo 端口（签名单源 engine/contract——ensureMu 单锁留核、
	// memo map 随域，ADR-0088 决策 4）；projectTeam/
	// projectNetworkFactsNamesOnly 是 facts 端口（解析面五族住核
	// resolve.go，resolutionface 锚不动——backups 同款）；recordOwners/
	// recordExpected 是观测窄接口（obs/expect 域登记函数的 browse 域窄化
	// 签名——归属域判别留核（browseOwner 铸造在绑定闭包侧），卫星只消费
	// 会话轴：workloadOwner 不全域下沉的 P5 wave-3 裁定，首颗需要它的星
	// 以语义窄化消费——appOwnerOf 基础类型化同款思路）；kickBrowse 是
	// 受理后的收敛环唤醒（Loop 挂载留核，KickTasks 同款）；
	// publishRoutesNow 是 ephemeral 路由的即时发布（routepublish 真源
	// 留核）。
	lockMaintenance              func() func()
	boundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh                  contract.EnsureFresh
	ensureRemember               contract.EnsureRemember
	ensureForget                 contract.EnsureForget
	projectTeam                  func(ctx context.Context, projectID string) (string, error)
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	recordOwners                 func(sessionID string, gen uint64, ws []capability.Workload)
	recordExpected               func(sessionID string, gen uint64)
	kickBrowse                   func()
	publishRoutesNow             func()
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值/闭包；EnsureMemo 是随域 memo 表
// 本体——组合根铸造注入（metastatewrite D3：表只在组合根/meta init 出生））。
type Deps struct {
	Runtime      capability.Runtime
	Repos        transact.Stores
	Cipher       *material.Cipher
	Log          *slog.Logger
	Clock        state.Clock
	BrowseConfig func() BrowseConfig

	LockMaintenance              func() func()
	BoundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	EnsureFresh                  contract.EnsureFresh
	EnsureRemember               contract.EnsureRemember
	EnsureForget                 contract.EnsureForget
	ProjectTeam                  func(ctx context.Context, projectID string) (string, error)
	ProjectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	// RecordOwners/RecordExpected 是观测窄接口的 browse 域窄化端口
	//（核绑定闭包内铸造 browseOwner——域判别留核）。
	RecordOwners     func(sessionID string, gen uint64, ws []capability.Workload)
	RecordExpected   func(sessionID string, gen uint64)
	KickBrowse       func()
	PublishRoutesNow func()

	// EnsureMemo 是 per-会话 Ensure 备忘表本体（组合根 make 后注入）。
	EnsureMemo map[string]contract.Memo
}

// New 构造 Browse 会话域卫星（不启动——环步由 engine 核驱动：环表 step
// 经卫星方法值 e.browses.Step）。
func New(deps Deps) *Browses {
	b := &Browses{
		sessions:                     make(map[string]*browseSession),
		runtime:                      deps.Runtime,
		repos:                        deps.Repos,
		cipher:                       deps.Cipher,
		log:                          deps.Log,
		clock:                        deps.Clock,
		browseConfig:                 deps.BrowseConfig,
		lockMaintenance:              deps.LockMaintenance,
		boundedStep:                  deps.BoundedStep,
		ensureFresh:                  deps.EnsureFresh,
		ensureRemember:               deps.EnsureRemember,
		ensureForget:                 deps.EnsureForget,
		projectTeam:                  deps.ProjectTeam,
		projectNetworkFactsNamesOnly: deps.ProjectNetworkFactsNamesOnly,
		recordOwners:                 deps.RecordOwners,
		recordExpected:               deps.RecordExpected,
		kickBrowse:                   deps.KickBrowse,
		publishRoutesNow:             deps.PublishRoutesNow,
		ensure:                       deps.EnsureMemo,
	}
	return b
}

// browseSession 是一个在册会话（活体；grant 是 cookie 面值——重启重铸）。
type browseSession struct {
	id          string
	teamID      string
	projectID   string
	databaseID  string
	engineName  string
	browser     dbbrowser.Browser
	readOnly    bool
	grant       string // base64url 32B（cookie 值的后半段）
	createdAt   time.Time
	expiresAt   time.Time
	lastTouch   time.Time
	workload    capability.Workload // 最近成功投影（路由后端解析的期望集）
	databaseRow string              // 库名（凭证 Secret 铸名 database:<name> 的输入）
}

// BrowseInput 是会话注册输入（api 层在受理事务提交后注入）。P5 wave-3
// 随域拆包迁入——核侧经类型别名零漂移（gRPC 受理面零改）。
type BrowseInput struct {
	SessionID    string
	TeamID       string
	ProjectID    string
	DatabaseID   string
	DatabaseName string
	EngineName   string
	ReadOnly     bool
}

// BrowseSessionInfo 是注册产品（api 层回显面）。同上随域迁入。
type BrowseSessionInfo struct {
	ID          string
	Browser     string
	ReadOnly    bool
	Enforcement dbbrowser.Enforcement
	ExpiresIn   int32
}

// browseHost 铸会话路由主机名（browse-<lower(id)>.<suffix>；host 字符集
// 由 ValidateRouteHost 把守——ULID lower + 操作者后缀）。
func (b *Browses) browseHost(sessionID string) string {
	return "browse-" + strings.ToLower(sessionID) + "." + b.browseConfig().HostSuffix
}

// BrowseEntryURL 铸会话入口 URL（api 响应面；票据由调用方拼接——单用途
// 值不进本公式）。
func (b *Browses) BrowseEntryURL(sessionID string) string {
	scheme := "http"
	if b.browseConfig().TLSMode == "auto" {
		scheme = "https"
	}
	return scheme + "://" + b.browseHost(sessionID) + "/v1/browse/entry"
}

// BrowseConfigured 报告 browse 面装配可用（api 受理面先拒 E_BROWSE_DISABLED）。
func (b *Browses) BrowseConfigured() bool { return b.browseConfig().Configured() }

// browseGrantBytes 铸随机 grant 材料。
func browseGrantBytes() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// RegisterBrowseSession 注册一个刚受理的会话（api 层在 commit 后调用）。
// 注册不可失败（行已落——本调用后崩溃由 browseLoop 从行恢复，这是台账
// 存在的理由）。返回注册产品供回显。
func (b *Browses) RegisterBrowseSession(in BrowseInput) (BrowseSessionInfo, error) {
	browser, ok := dbbrowser.For(in.EngineName)
	if !ok {
		return BrowseSessionInfo{}, fmt.Errorf("engine %q has no browse browser", in.EngineName)
	}
	grant, err := browseGrantBytes()
	if err != nil {
		return BrowseSessionInfo{}, fmt.Errorf("mint browse grant: %w", err)
	}
	now := b.clock.Now()
	s := &browseSession{
		id: in.SessionID, teamID: in.TeamID, projectID: in.ProjectID,
		databaseID: in.DatabaseID, engineName: in.EngineName, browser: browser,
		readOnly: in.ReadOnly, grant: grant,
		createdAt: now, expiresAt: now.Add(BrowseHardTTL), lastTouch: now,
		databaseRow: in.DatabaseName,
	}
	b.mu.Lock()
	b.sessions[in.SessionID] = s
	b.mu.Unlock()
	b.kickBrowse()
	b.publishRoutesNow()
	return BrowseSessionInfo{
		ID: s.id, Browser: browser.Name(), ReadOnly: s.readOnly,
		Enforcement: browser.ReadOnlyEnforcement(),
		ExpiresIn:   int32(BrowseHardTTL / time.Second),
	}, nil
}

// BrowseCountByTeam 统计 per-Team 在册会话数（quota 判定输入；api 层消费）。
func (b *Browses) BrowseCountByTeam(teamID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, s := range b.sessions {
		if s.teamID == teamID {
			n++
		}
	}
	return n
}

// BrowseCheckQuota 是受理面 quota 闸（在册数 + 本受理 ≤ 上限）。
func (b *Browses) BrowseCheckQuota(teamID string) error {
	if b.BrowseCountByTeam(teamID)+1 > BrowseMaxSessionsPerTeam {
		return ErrBrowseQuota
	}
	return nil
}

// BrowseSessionGrant 兑换一枚新 grant（entry 烧票后调用：铸 cookie 值 +
// 续活）。会话不存在/已收口 → ok=false（401）。
func (b *Browses) BrowseSessionGrant(sessionID string) (cookieValue string, maxAge int, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, in := b.sessions[sessionID]
	if !in || b.clock.Now().After(s.expiresAt) {
		return "", 0, false
	}
	s.lastTouch = b.clock.Now()
	remain := time.Until(s.expiresAt)
	if remain < 0 {
		remain = 0
	}
	return sessionID + "." + s.grant, int(remain / time.Second), true
}

// BrowseValidateGrant 校验 cookie 值的 grant（authorize 每请求调用；
// 常量时间比对 + 续活）。
func (b *Browses) BrowseValidateGrant(sessionID, grant string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, in := b.sessions[sessionID]
	if !in {
		return false
	}
	now := b.clock.Now()
	if now.After(s.expiresAt) || now.Sub(s.lastTouch) > browseIdleTTL {
		return false
	}
	if !constantTimeEq(s.grant, grant) {
		return false
	}
	s.lastTouch = now
	return true
}

// constantTimeEq 是 grant 比对的常量时间闸（crypto/subtle 面的字符串形）。
func constantTimeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// BrowseDNSName 铸 per-会话稳定 DNS 名（项目网内调试面；DatabaseDNSName
// 同款公式，公式真源在 engine/contract）。P5 wave-3 随域拆包迁入。
func BrowseDNSName(sessionID string) string {
	return "browse-" + strings.ToLower(sessionID)
}

// BrowseCapabilityRoutes 铸在册会话的 ephemeral 双 Route（核 routepublish
// 的 publishRoutes 合并面消费；后端经 Runtime.Addresses 解析——解析不到
// 跳过该会话的路由）。P5 wave-3 拆包导出（原 browseCapabilityRoutes，
// 核消费经卫星指针）。
func (b *Browses) BrowseCapabilityRoutes(ctx context.Context) []capability.Route {
	cfg := b.browseConfig()
	if !cfg.Configured() {
		return nil
	}
	gwURL, err := url.Parse(cfg.GatewayURL)
	if err != nil || gwURL.Host == "" {
		b.log.Error("browse routes: gateway url malformed", "url", cfg.GatewayURL)
		return nil
	}
	b.mu.Lock()
	sessions := make([]*browseSession, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.mu.Unlock()
	var out []capability.Route
	for _, s := range sessions {
		host := b.browseHost(s.id)
		tls := cfg.TLSMode
		// ① 票据兑换入口（免门禁：规则更长，优先级高于 Host-only 路由）。
		out = append(out, capability.Route{
			Host: host, Path: "/v1/browse/entry",
			Target:  capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id},
			Process: s.browser.Name(), Port: 8080,
			Protocol:    capability.ProtocolHTTP,
			TLS:         tls,
			BackendAddr: gwURL.Host,
		})
		// ② 工具路由（ForwardAuth 门禁；后端 = 容器网内地址）。
		backend, ok := b.browseBackendAddr(ctx, s)
		if !ok {
			continue // 冷启动未解析：本拍不发布（诚实 404 窗，下拍收敛）
		}
		out = append(out, capability.Route{
			Host:    host,
			Target:  capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id},
			Process: s.browser.Name(), Port: s.browser.Port(),
			Protocol:    capability.ProtocolHTTP,
			TLS:         tls,
			BackendAddr: backend,
			// ?session= 是会话绑定的 ForwardAuth 参数（H4）：authorize 面据此
			// 比对 cookie 内 sid——没有它，任一有效 browse cookie 可穿过他人
			// browse host 的门禁（entry 路由不带 Auth，不受影响）。
			Auth: &capability.RouteAuth{
				Address: cfg.GatewayURL + "/v1/browse/authorize?session=" + url.QueryEscape(s.id),
			},
		})
	}
	return out
}

// browseBackendAddr 解析会话工具容器的网内地址（期望集 = 最近成功投影）。
func (b *Browses) browseBackendAddr(ctx context.Context, s *browseSession) (string, bool) {
	b.mu.Lock()
	w := s.workload
	b.mu.Unlock()
	if w.ID == "" {
		return "", false
	}
	ns := capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id}
	eps, err := b.runtime.Addresses(ctx, ns, []capability.Workload{w})
	if err != nil {
		return "", false
	}
	for _, ep := range eps {
		if ep.Addr != "" {
			return fmt.Sprintf("%s:%d", ep.Addr, ep.Port), true
		}
	}
	return "", false
}

// BrowseRoutesFingerprint 是会话集指纹（核 routepublish 的 publishRoutes
// 签名面——会话增删不改 route 行，签名必须覆盖 ephemeral 路由的输入）。
// P5 wave-3 拆包导出（原 browseRoutesFingerprint，核消费经卫星指针）。
func (b *Browses) BrowseRoutesFingerprint() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.sessions))
	for id := range b.sessions {
		ids = append(ids, id)
	}
	sorted := append([]string{}, ids...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return strings.Join(sorted, ",")
}
