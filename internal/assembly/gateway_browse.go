package assembly

// gateway 的 browse 原生入口（F3.6，ADR-0051 决策 5）两件：
//
//   - GET /v1/browse/entry    Launcher Ticket 兑换（?session=&ticket=）：
//     烧票（单用途）→ 铸 host-only cookie（flt_browse=<sid>.<grant>，
//     HttpOnly、MaxAge=会话剩余）→ 302 /（进入 ForwardAuth 门禁的工具
//     路由）。该路径经 browse 会话的免门禁 ephemeral Route 到达（规则
//     更长优先级更高），自身安全面 = 票据本身。
//   - GET /v1/browse/authorize  ForwardAuth 校验目标（traefik 每请求
//     侧呼；原始 Cookie 头透传——2026-10-07 v3.6 实证）：校验 cookie 的
//     grant（常量时间比对 + 续活）→ 200/401。
//
// 与 relay/exec-stream/platform-binary 同一挂法族：root mux 精确路径 +
// gateway 回落；不进 swagger（原生入口）。

import (
	"net/http"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
)

// browseCookieName 是会话 cookie 的常量名（entry 铸 / authorize 读——
// 双端同文件单源；host-only 无 Domain 属性，不跨子域泄漏）。
const browseCookieName = "flt_browse"

// browse 原生入口路径常量（browseCapabilityRoutes 的 entry 路由同源形态）。
const (
	browseEntryPath     = "/v1/browse/entry"
	browseAuthorizePath = "/v1/browse/authorize"
)

// BrowseGate 是 browse 原生入口的消费面（assembly 装配用；exec 的
// ExecStreamSource 同款挂法）。
type BrowseGate struct {
	s *fleetlygrpc.Services
}

// NewBrowseGate 构造。
func NewBrowseGate(s *fleetlygrpc.Services) *BrowseGate { return &BrowseGate{s: s} }

// redeemTicket 兑换 browse 票据（purpose+会话绑定、单用途）。
func (g *BrowseGate) redeemTicket(sessionID, ticket string) bool {
	return g.s.RedeemBrowseTicket(sessionID, ticket)
}

// grant 铸一枚新 cookie 面值（engine 注册表）。
func (g *BrowseGate) grant(sessionID string) (cookieValue string, maxAge int, ok bool) {
	return g.s.Engine.BrowseSessionGrant(sessionID)
}

// validate 校验 cookie 面值（engine 注册表；常量时间比对 + 续活）。
func (g *BrowseGate) validate(sessionID, grantValue string) bool {
	return g.s.Engine.BrowseValidateGrant(sessionID, grantValue)
}

// mountBrowse 挂载 browse 原生入口（entry + authorize）。
func mountBrowse(h http.Handler, gate *BrowseGate) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case browseEntryPath:
			if r.Method != http.MethodGet {
				writeExecStatus(w, http.StatusMethodNotAllowed, "get_only", "the browse entry endpoint accepts GET only")
				return
			}
			serveBrowseEntry(w, r, gate)
		case browseAuthorizePath:
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeExecStatus(w, http.StatusMethodNotAllowed, "get_only", "the browse authorize endpoint accepts GET only")
				return
			}
			serveBrowseAuthorize(w, r, gate)
		default:
			h.ServeHTTP(w, r)
		}
	})
}

// serveBrowseEntry：票据兑换 → cookie → 302 /。缺参/坏票/会话不在册一律
// 401 最小事实（匿名面不区分成因——票据是唯一凭证面）。
func serveBrowseEntry(w http.ResponseWriter, r *http.Request, gate *BrowseGate) {
	sessionID := r.URL.Query().Get("session")
	ticket := r.URL.Query().Get("ticket")
	if sessionID == "" || ticket == "" {
		writeExecStatus(w, http.StatusUnauthorized, "bad_ticket", "session and ticket query parameters are required")
		return
	}
	if !gate.redeemTicket(sessionID, ticket) {
		writeExecStatus(w, http.StatusUnauthorized, "bad_ticket", "ticket is invalid, expired, or already used")
		return
	}
	cookieValue, maxAge, ok := gate.grant(sessionID)
	if !ok {
		writeExecStatus(w, http.StatusUnauthorized, "session_unavailable", "browse session is no longer active")
		return
	}
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124：Secure 刻意缺省 false——browse 路由缺省明文 none（ADR-0051 决策 5）；tls=auto 形态下 cookie 面 Secure 化随 TLS 批次收口
		// cookie 名单源（entry 铸 / authorize 读，双端同文件）。
		Name:     browseCookieName,
		Value:    cookieValue,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusFound)
}

// serveBrowseAuthorize：ForwardAuth 校验面（traefik 侧呼）。无有效 cookie
// 一律 401（无正文细节——对匿名面只呈现"未授权"）。
func serveBrowseAuthorize(w http.ResponseWriter, r *http.Request, gate *BrowseGate) {
	c, err := r.Cookie(browseCookieName)
	if err != nil || c.Value == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	sid, grant, ok := splitBrowseCookie(c.Value)
	if !ok || !gate.validate(sid, grant) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// splitBrowseCookie 拆 <sid>.<grant>（形态不符 = 无效）。
func splitBrowseCookie(v string) (sid, grant string, ok bool) {
	i := strings.IndexByte(v, '.')
	if i <= 0 || i == len(v)-1 {
		return "", "", false
	}
	return v[:i], v[i+1:], true
}
