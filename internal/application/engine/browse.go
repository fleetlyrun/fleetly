package engine

// Browse 会话域（F3.6，ADR-0051）：per-Database 数据浏览器按需实例。
// 会话非资源行（exec 同语义——受理回显即全部读面），回收台账持久化在
// browse_sessions 行（browse 载体是外部活体，daemon 重启后注册表丢失而
// 载体仍在跑——行是重注册与到点回收的锚）。数据流：受理（api 层四件
// 一拍落行）→ RegisterBrowseSession（注册表 + Kick）→ browseLoop 投影
// 浏览器 Workload（方言单源 dbbrowser；挂 Database 所在 Project 全部活跃
// 网；材料经 SecretFiles）→ ephemeral 双 Route 合并进 publishRoutes。
//
// 回收：硬 TTL 30min + 空闲 10min（entry/authorize 接触即续活——门禁
// 流量就是活性信号）→ Runtime.Remove + 删行。重启后 grant 重铸（旧
// cookie 失效，用户重开——诚实行为，不做 cookie 迁移）。
//
// 文件分组（P3 批 4）：本文件留受理/grant/路由发布面；收敛环（browseStep
// 对账族）见 browseloop.go。
//
// P4 wave-2a（ADR-0088 决策 2/3）：Browse 域接收者下域——本文件与
// browseloop.go 的方法自 *Engine 迁至 *browses，方法体内核引用改写为卫星
// 字段/机制端口；Engine 裸嵌入 browses（嵌入提升，browseStep 仍在环表、
// gRPC Browse 受理/grant 面与 routepublish 的 ephemeral 路由合并面零改）。
// 卫星依赖显式逐字段，禁回引 *Engine（enginebackref R1/R2 执法）。

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

// BrowseConfig 是 browse 面的配置快照（Options 注入；HostSuffix 或
// GatewayURL 空 = 面停用，ADR-0051 决策 5）。
type BrowseConfig struct {
	HostSuffix string
	GatewayURL string
	TLSMode    string // none（缺省）| auto
}

// Configured 报告 browse 面是否装配可用。
func (c BrowseConfig) Configured() bool { return c.HostSuffix != "" && c.GatewayURL != "" }

// browses 是 browse 会话域卫星实例态（活体注册表；行是台账）。P3 批 4 收编：
// browseDomain → browses（复数普通名词零后缀，ADR-0075 命名法则）；P4
// wave-2a 嵌入改形（ADR-0088 决策 2/3）：接收者已下域 *browses（域内短名
// b——browser 局部改名 browser 防活体遮蔽，backups=bk 同款裁定），方法经
// 嵌入提升对核/接口/装配消费面零变化；依赖显式逐字段 + 机制端口注入
// （construct.go 装配）。禁挂 *Engine 回引（enginebackref 守卫执法）。
type browses struct {
	mu       sync.Mutex
	sessions map[string]*browseSession
	// ensure 是 per-会话 Ensure 签名备忘（ensureMemo 协议；键 = 会话 ID）。
	ensure map[string]ensureMemo

	// 显式依赖（实测消费面，P4 wave-2a 逐字段注入）：runtime 是载体动词
	// 消费面（Ensure/Remove/Addresses——核副本双持，P5 过渡形态）；repos
	// 是台账与依赖行读（Browse 台账/Databases 库名回读/Secrets 凭证解封，
	// transact.Stores 整体注入，ADR-0088 决策 3）；cipher 是凭证 Secret
	// 解封（核副本双持——解析面/Secret 面等核消费者仍读核字段）；log/
	// clock 域内日志与节拍。
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
	// 继续命中）；boundedStep 是步带界端口（ManagedStepTimeout 事实面，
	// ensure.go 统一入口）；ensureFresh/ensureRemember/ensureForget 是
	// memo 端口（ensureMu 单锁留核、memo map 随域，ADR-0088 决策 4）；
	// projectTeam/projectNetworkFactsNamesOnly 是 facts 端口（解析面五族
	// 住 resolve.go，resolutionface 锚不动——backups 同款）；
	// recordOwners/recordExpected 是观测窄接口（obs/expect 域登记函数，
	// 禁止卫星直写内部）；kickBrowse 是受理后的收敛环唤醒（Loop 挂载留核，
	// KickTasks 同款）；publishRoutesNow 是 ephemeral 路由的即时发布
	//（routepublish 真源留核）。
	lockMaintenance              func() func()
	boundedStep                  func(ctx context.Context) (context.Context, context.CancelFunc)
	ensureFresh                  func(memo map[string]ensureMemo, key, sig string, now time.Time) (ensureMemo, bool)
	ensureRemember               func(memo map[string]ensureMemo, key string, rec ensureMemo)
	ensureForget                 func(memo map[string]ensureMemo, key string)
	projectTeam                  func(ctx context.Context, projectID string) (string, error)
	projectNetworkFactsNamesOnly func(ctx context.Context, projectID string) []string
	recordOwners                 func(gen uint64, ws []capability.Workload, ownerOf func(capability.Workload) workloadOwner)
	recordExpected               func(owner workloadOwner, gen uint64)
	kickBrowse                   func()
	publishRoutesNow             func()
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

// BrowseInput 是会话注册输入（api 层在受理事务提交后注入）。
type BrowseInput struct {
	SessionID    string
	TeamID       string
	ProjectID    string
	DatabaseID   string
	DatabaseName string
	EngineName   string
	ReadOnly     bool
}

// BrowseSessionInfo 是注册产品（api 层回显面）。
type BrowseSessionInfo struct {
	ID          string
	Browser     string
	ReadOnly    bool
	Enforcement dbbrowser.Enforcement
	ExpiresIn   int32
}

// browseHost 铸会话路由主机名（browse-<lower(id)>.<suffix>；host 字符集
// 由 ValidateRouteHost 把守——ULID lower + 操作者后缀）。
func (b *browses) browseHost(sessionID string) string {
	return "browse-" + strings.ToLower(sessionID) + "." + b.browseConfig().HostSuffix
}

// BrowseEntryURL 铸会话入口 URL（api 响应面；票据由调用方拼接——单用途
// 值不进本公式）。
func (b *browses) BrowseEntryURL(sessionID string) string {
	scheme := "http"
	if b.browseConfig().TLSMode == "auto" {
		scheme = "https"
	}
	return scheme + "://" + b.browseHost(sessionID) + "/v1/browse/entry"
}

// BrowseConfigured 报告 browse 面装配可用（api 受理面先拒 E_BROWSE_DISABLED）。
func (b *browses) BrowseConfigured() bool { return b.browseConfig().Configured() }

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
func (b *browses) RegisterBrowseSession(in BrowseInput) (BrowseSessionInfo, error) {
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

// KickBrowse 唤醒 browse 收敛环（api 受理面消费）。
func (e *Engine) KickBrowse() { e.browseLoop.Kick() }

// BrowseCountByTeam 统计 per-Team 在册会话数（quota 判定输入；api 层消费）。
func (b *browses) BrowseCountByTeam(teamID string) int {
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
func (b *browses) BrowseCheckQuota(teamID string) error {
	if b.BrowseCountByTeam(teamID)+1 > BrowseMaxSessionsPerTeam {
		return ErrBrowseQuota
	}
	return nil
}

// BrowseSessionGrant 兑换一枚新 grant（entry 烧票后调用：铸 cookie 值 +
// 续活）。会话不存在/已收口 → ok=false（401）。
func (b *browses) BrowseSessionGrant(sessionID string) (cookieValue string, maxAge int, ok bool) {
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
func (b *browses) BrowseValidateGrant(sessionID, grant string) bool {
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

// browseOwner 铸 browse 域归属（观测路由 + 看门狗）。
func browseOwner(id string) workloadOwner { return workloadOwner{ownerBrowse, id} }

// BrowseDNSName 铸 per-会话稳定 DNS 名（项目网内调试面；DatabaseDNSName
// 同款公式）。
func BrowseDNSName(sessionID string) string {
	return "browse-" + strings.ToLower(sessionID)
}

// browseCapabilityRoutes 铸在册会话的 ephemeral 双 Route（publishRoutes
// 合并面；后端经 Runtime.Addresses 解析——解析不到跳过该会话的路由）。
func (b *browses) browseCapabilityRoutes(ctx context.Context) []capability.Route {
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
func (b *browses) browseBackendAddr(ctx context.Context, s *browseSession) (string, bool) {
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

// browseRoutesFingerprint 是会话集指纹（publishRoutes 签名面——会话增删
// 不改 route 行，签名必须覆盖 ephemeral 路由的输入）。
func (b *browses) browseRoutesFingerprint() string {
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
