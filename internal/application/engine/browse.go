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

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
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

// browses 是 browse 会话域实例态（活体注册表；行是台账）。P3 批 4 收编：
// browseDomain → browses（复数普通名词零后缀，ADR-0075 命名法则）；Engine
// 字段名 browse 保持不变（域字段壳收口留批 8）。
type browses struct {
	mu       sync.Mutex
	sessions map[string]*browseSession
	// ensure 是 per-会话 Ensure 签名备忘（ensureMemo 协议；键 = 会话 ID）。
	ensure map[string]ensureMemo
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
func (e *Engine) browseHost(sessionID string) string {
	return "browse-" + strings.ToLower(sessionID) + "." + e.opts.Browse.HostSuffix
}

// BrowseEntryURL 铸会话入口 URL（api 响应面；票据由调用方拼接——单用途
// 值不进本公式）。
func (e *Engine) BrowseEntryURL(sessionID string) string {
	scheme := "http"
	if e.opts.Browse.TLSMode == "auto" {
		scheme = "https"
	}
	return scheme + "://" + e.browseHost(sessionID) + "/v1/browse/entry"
}

// BrowseConfigured 报告 browse 面装配可用（api 受理面先拒 E_BROWSE_DISABLED）。
func (e *Engine) BrowseConfigured() bool { return e.opts.Browse.Configured() }

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
func (e *Engine) RegisterBrowseSession(in BrowseInput) (BrowseSessionInfo, error) {
	b, ok := dbbrowser.For(in.EngineName)
	if !ok {
		return BrowseSessionInfo{}, fmt.Errorf("engine %q has no browse browser", in.EngineName)
	}
	grant, err := browseGrantBytes()
	if err != nil {
		return BrowseSessionInfo{}, fmt.Errorf("mint browse grant: %w", err)
	}
	now := e.clock.Now()
	s := &browseSession{
		id: in.SessionID, teamID: in.TeamID, projectID: in.ProjectID,
		databaseID: in.DatabaseID, engineName: in.EngineName, browser: b,
		readOnly: in.ReadOnly, grant: grant,
		createdAt: now, expiresAt: now.Add(BrowseHardTTL), lastTouch: now,
		databaseRow: in.DatabaseName,
	}
	e.browse.mu.Lock()
	e.browse.sessions[in.SessionID] = s
	e.browse.mu.Unlock()
	e.KickBrowse()
	e.PublishRoutesNow()
	return BrowseSessionInfo{
		ID: s.id, Browser: b.Name(), ReadOnly: s.readOnly,
		Enforcement: b.ReadOnlyEnforcement(),
		ExpiresIn:   int32(BrowseHardTTL / time.Second),
	}, nil
}

// KickBrowse 唤醒 browse 收敛环（api 受理面消费）。
func (e *Engine) KickBrowse() { e.browseLoop.Kick() }

// BrowseCountByTeam 统计 per-Team 在册会话数（quota 判定输入；api 层消费）。
func (e *Engine) BrowseCountByTeam(teamID string) int {
	e.browse.mu.Lock()
	defer e.browse.mu.Unlock()
	n := 0
	for _, s := range e.browse.sessions {
		if s.teamID == teamID {
			n++
		}
	}
	return n
}

// BrowseCheckQuota 是受理面 quota 闸（在册数 + 本受理 ≤ 上限）。
func (e *Engine) BrowseCheckQuota(teamID string) error {
	if e.BrowseCountByTeam(teamID)+1 > BrowseMaxSessionsPerTeam {
		return ErrBrowseQuota
	}
	return nil
}

// BrowseSessionGrant 兑换一枚新 grant（entry 烧票后调用：铸 cookie 值 +
// 续活）。会话不存在/已收口 → ok=false（401）。
func (e *Engine) BrowseSessionGrant(sessionID string) (cookieValue string, maxAge int, ok bool) {
	e.browse.mu.Lock()
	defer e.browse.mu.Unlock()
	s, in := e.browse.sessions[sessionID]
	if !in || e.clock.Now().After(s.expiresAt) {
		return "", 0, false
	}
	s.lastTouch = e.clock.Now()
	remain := time.Until(s.expiresAt)
	if remain < 0 {
		remain = 0
	}
	return sessionID + "." + s.grant, int(remain / time.Second), true
}

// BrowseValidateGrant 校验 cookie 值的 grant（authorize 每请求调用；
// 常量时间比对 + 续活）。
func (e *Engine) BrowseValidateGrant(sessionID, grant string) bool {
	e.browse.mu.Lock()
	defer e.browse.mu.Unlock()
	s, in := e.browse.sessions[sessionID]
	if !in {
		return false
	}
	now := e.clock.Now()
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
func (e *Engine) browseCapabilityRoutes(ctx context.Context) []capability.Route {
	if !e.opts.Browse.Configured() {
		return nil
	}
	gwURL, err := url.Parse(e.opts.Browse.GatewayURL)
	if err != nil || gwURL.Host == "" {
		e.log.Error("browse routes: gateway url malformed", "url", e.opts.Browse.GatewayURL)
		return nil
	}
	e.browse.mu.Lock()
	sessions := make([]*browseSession, 0, len(e.browse.sessions))
	for _, s := range e.browse.sessions {
		sessions = append(sessions, s)
	}
	e.browse.mu.Unlock()
	var out []capability.Route
	for _, s := range sessions {
		host := e.browseHost(s.id)
		tls := e.opts.Browse.TLSMode
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
		backend, ok := e.browseBackendAddr(ctx, s)
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
			Auth:        &capability.RouteAuth{Address: e.opts.Browse.GatewayURL + "/v1/browse/authorize"},
		})
	}
	return out
}

// browseBackendAddr 解析会话工具容器的网内地址（期望集 = 最近成功投影）。
func (e *Engine) browseBackendAddr(ctx context.Context, s *browseSession) (string, bool) {
	e.browse.mu.Lock()
	w := s.workload
	e.browse.mu.Unlock()
	if w.ID == "" {
		return "", false
	}
	ns := capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id}
	eps, err := e.runtime.Addresses(ctx, ns, []capability.Workload{w})
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
func (e *Engine) browseRoutesFingerprint() string {
	e.browse.mu.Lock()
	defer e.browse.mu.Unlock()
	ids := make([]string, 0, len(e.browse.sessions))
	for id := range e.browse.sessions {
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
