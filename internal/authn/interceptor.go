package authn

// authn/authz 拦截器（F0.6 执法接管）：Bearer 解析 → sha256 查表 →
// scope 执法（SERVER 面）→ Identity 进 ctx（审计 actor 与服务层消费）。
// fail-closed：无策略的方法拒之（启动期 AssertAllRegisteredHavePolicy 已
// 把守，此处是纵深防御）；PUBLIC 面豁免仅 system 状态面与 whoami/邀请
// 接受；PUBLIC 面携带无效/已吊销凭证按匿名放行（status 不被脏凭证阻塞）。

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lynx-go/grpcapi/authz"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	rolerepo "github.com/fleetlyrun/fleetly/internal/state/role"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/user"
)

// HeaderClientSource 是 CLI 自标识头（值 "cli"）：审计来源 api/cli 区分的
// 唯一锚（SDK 的 CLI 消费方随拨号附头）。
const HeaderClientSource = "x-fleetly-client"

// bearerPrefix 是 Authorization 头的标准前缀。
const bearerPrefix = "Bearer "

// lastUsedInterval 是 last_used_at 节流间隔（F0.6：勿每请求写库——
// SQLite 单写者减压；进程内 per-Token 时间戳，重启丢失可接受）。
const lastUsedInterval = 60 * time.Second

// Identity 是一次成功解析的操作者身份（ctx 注入形态；审计 actor、
// WhoAmI、服务层归属判定共用）。
type Identity struct {
	TokenID   string
	TokenName string
	TeamID    string
	UserID    string
	UserName  string
	RoleID    string
	RoleName  string
	Scopes    []identity.Scope // colon 原始形态（未展开蕴含）

	scopeSet authz.ScopeSet // dot 展开形态（执法用；resolve 时一次物化）
}

// Actor 返回审计 actor 形态（有属主用户以用户名计，否则 Token 名计）。
func (id *Identity) Actor() string {
	if id != nil && id.UserName != "" {
		return "user:" + id.UserName
	}
	if id != nil && id.TokenName != "" {
		return "token:" + id.TokenName
	}
	return ""
}

// ScopeStrings 返回 colon 形态 scope 列表（WhoAmI 展示）。
func (id *Identity) ScopeStrings() []string { return identity.ScopeStrings(id.Scopes) }

type ctxKey struct{}

// FromContext 取 ctx 中的身份（匿名 = false）。
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(*Identity)
	return id, ok && id != nil
}

// Authenticator 是拦截器依赖集（assembly 与 apitest 夹具共用构造）。
type Authenticator struct {
	db       *state.DB
	policy   *authz.PolicySet
	log      *slog.Logger
	roles    *rolerepo.Repo
	users    *user.Repo
	tokens   *tokenrepo.Repo
	vocab    []string
	lastMu   sync.Mutex
	lastUsed map[string]time.Time
}

// NewAuthenticator 构造（vocab 是 scope 词表——assembly 单一源注入）。
func NewAuthenticator(db *state.DB, policy *authz.PolicySet, vocab []string, log *slog.Logger) *Authenticator {
	return &Authenticator{
		db: db, policy: policy, log: log,
		roles:  rolerepo.New(db.Clock()),
		users:  user.New(db.Clock()),
		tokens: tokenrepo.New(db.Clock()),
		vocab:  vocab, lastUsed: map[string]time.Time{},
	}
}

// Unary 返回 unary 拦截器（SlotAuth 槽位）。
func (a *Authenticator) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		next, err := a.guard(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(next, req)
	}
}

// Stream 返回流式拦截器（同款执法；StreamLogs 面不留洞）。
func (a *Authenticator) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		next, err := a.guard(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: next})
	}
}

// wrappedStream 把执法后的 ctx 带进 handler（ServerStream ctx 只读，
// 包装是标准形态）。
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// guard 是执法主体：解析 → 面档判定 → scope 检查 → identity 注入。
func (a *Authenticator) guard(ctx context.Context, fullMethod string) (context.Context, error) {
	policy, ok := a.policy.Get(fullMethod)
	if !ok {
		// 启动断言把守下的不可达分支：纵深防御，拒且大声。
		a.log.Error("authn: method without policy (invariant broken)", "method", fullMethod)
		return nil, apperr.New("E_INTERNAL", "authorization policy is missing for this method").
			WithContext("method", fullMethod)
	}

	id, credErr := a.resolve(ctx)
	switch policy.Access {
	case authz.AccessPublic:
		// 公开面：无效凭证按匿名（status/version 不被脏凭证阻塞）；
		// 解析成功的身份照常注入（whoami 消费）。
		if credErr != nil {
			id = nil
		}
		return withIdentity(ctx, id), nil
	case authz.AccessServer:
		if credErr != nil {
			return nil, credErr
		}
		if id == nil {
			return nil, apperr.New("E_UNAUTHENTICATED",
				"this method requires a token; pass one via 'fleetly login' or the authorization header")
		}
		rule := a.policy.ScopeRule(fullMethod)
		if rule == nil {
			a.log.Error("authn: SERVER method without scope rule (invariant broken)", "method", fullMethod)
			return nil, apperr.New("E_INTERNAL", "authorization scope is missing for this method").
				WithContext("method", fullMethod)
		}
		if !id.scopeSet.Satisfies(*rule) {
			return nil, apperr.New("E_FORBIDDEN",
				"token %q lacks required scope %s:%s", id.TokenName, rule.Resource, rule.Op).
				WithContext("token", id.TokenName).
				WithContext("required_scope", rule.Resource+":"+string(rule.Op))
		}
		a.touchLastUsed(id)
		return withIdentity(ctx, id), nil
	default:
		// END_USER/PERMISSION/SYSTEM 档 v1 未启用：fail-closed 拒。
		return nil, apperr.New("E_FORBIDDEN", "this method is not available in the current release").
			WithContext("method", fullMethod)
	}
}

// resolve 解析凭证。返回 (nil, nil) = 匿名；(nil, err) = 凭证无效/吊销。
func (a *Authenticator) resolve(ctx context.Context) (*Identity, error) {
	secret := bearerFromContext(ctx)
	if secret == "" {
		return nil, nil
	}
	if identity.TokenKind(secret) != "platform" {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid token format")
	}
	sha := identity.HashToken(secret)
	tok, err := a.tokens.GetBySHA256(ctx, a.db.Runner(), sha)
	if err != nil {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid token")
	}
	if tok.Revoked {
		return nil, apperr.New("E_UNAUTHENTICATED", "token has been revoked").
			WithContext("reason", "revoked")
	}
	role, err := a.roles.Get(ctx, a.db.Runner(), tok.RoleID)
	if err != nil {
		// 角色失联是引用完整性破坏（FK 把守下的不可达）：按无效凭证拒。
		a.log.Error("authn: token references missing role", "token", tok.Name, "role", tok.RoleID)
		return nil, apperr.New("E_UNAUTHENTICATED", "token role is unavailable")
	}
	scopes, err := identity.ParseScopes(role.Scopes, a.vocab)
	if err != nil {
		a.log.Error("authn: role carries scopes outside vocabulary", "role", role.Name, "err", err)
		return nil, apperr.New("E_UNAUTHENTICATED", "token role carries invalid scopes")
	}
	set, err := identity.DotForm(scopes)
	if err != nil {
		return nil, apperr.New("E_UNAUTHENTICATED", "token role carries invalid scopes")
	}
	id := &Identity{
		TokenID: tok.ID, TokenName: tok.Name, TeamID: tok.TeamID,
		UserID: tok.UserID, RoleID: role.ID, RoleName: role.Name,
		Scopes: scopes, scopeSet: set,
	}
	if tok.UserID != "" {
		if u, err := a.users.Get(ctx, a.db.Runner(), tok.UserID); err == nil {
			id.UserName = u.Name
		}
		// 用户行缺失（FK 把守下的不可达）：用户名留空，actor 落 token 形态。
	}
	return id, nil
}

// touchLastUsed 节流记录（内存时间戳 + 独立轻 UPDATE；错误只记日志——
// 记录失败不阻断请求）。
func (a *Authenticator) touchLastUsed(id *Identity) {
	now := time.Now()
	a.lastMu.Lock()
	if last, ok := a.lastUsed[id.TokenID]; ok && now.Sub(last) < lastUsedInterval {
		a.lastMu.Unlock()
		return
	}
	a.lastUsed[id.TokenID] = now
	a.lastMu.Unlock()
	if err := a.tokens.TouchLastUsed(context.Background(), a.db.Runner(), id.TokenID); err != nil {
		a.log.Warn("authn: record last_used_at failed", "token", id.TokenName, "err", err)
	}
}

// bearerFromContext 提取 Authorization: Bearer 头（缺失返回空串）。
func bearerFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, v := range md.Get("authorization") {
		if strings.HasPrefix(v, bearerPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(v, bearerPrefix))
		}
	}
	return ""
}

// ClientSourceFromContext 判定审计来源：CLI 自标识头 → cli，否则 api
// （engine 内部动作不经本函数——它自有 system 来源）。
func ClientSourceFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "api"
	}
	for _, v := range md.Get(HeaderClientSource) {
		if v == "cli" {
			return "cli"
		}
	}
	return "api"
}

func withIdentity(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, id)
}

// auditOverride 是非请求路径触发面的审计标注（webhook 接收面：操作者
// 不是任何平台身份——actor 落 hook:<App 名>、source 落 webhook）。
type auditOverride struct {
	actor  string
	source audit.Source
}

type overrideKey struct{}

// WithAuditOverride 覆盖审计 actor/source（engine 与服务层经
// ActorFromContext/SourceFromContext 消费；请求路径不携带本标注）。
func WithAuditOverride(ctx context.Context, actor string, source audit.Source) context.Context {
	return context.WithValue(ctx, overrideKey{}, auditOverride{actor: actor, source: source})
}

// ActorFromContext 返回审计 actor（匿名空串；override 优先）。
func ActorFromContext(ctx context.Context) string {
	if ov, ok := ctx.Value(overrideKey{}).(auditOverride); ok && ov.actor != "" {
		return ov.actor
	}
	if id, ok := FromContext(ctx); ok {
		return id.Actor()
	}
	return ""
}

// SourceFromContext 返回审计来源：CLI 自标识头 → cli，经 gRPC 面的其余
// 调用 → api（engine 自治动作不经本函数——自有 system 来源；override
// 优先于两者）。
func SourceFromContext(ctx context.Context) audit.Source {
	if ov, ok := ctx.Value(overrideKey{}).(auditOverride); ok && ov.source != "" {
		return ov.source
	}
	if ClientSourceFromContext(ctx) == "cli" {
		return audit.SourceCLI
	}
	return audit.SourceAPI
}
