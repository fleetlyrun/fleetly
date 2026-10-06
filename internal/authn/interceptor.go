package authn

// authn/authz 拦截器（F0.6 执法接管）：Bearer 解析 → sha256 查表 →
// scope 执法（SERVER 面）→ Identity 进 ctx（审计 actor 与服务层消费）。
// fail-closed：无策略的方法拒之（启动期 AssertAllRegisteredHavePolicy 已
// 把守，此处是纵深防御）；PUBLIC 面豁免仅 system 状态面与 whoami/邀请
// 接受；PUBLIC 面携带无效/已吊销凭证按匿名放行（status 不被脏凭证阻塞）。

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/lynx-go/grpcapi/authz"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/state/membership"
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

// narrowingAuditInterval 是收窄拒绝审计的节流间隔（ADR-0038：拒绝路径可
// 能被重试循环反复命中——同 Token 60s 内只落一条审计行，同 lastUsed 形态）。
const narrowingAuditInterval = 60 * time.Second

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
	db          *state.DB
	policy      *authz.PolicySet
	log         *slog.Logger
	roles       *rolerepo.Repo
	users       *user.Repo
	tokens      *tokenrepo.Repo
	memberships *membershiprepo.Repo
	audits      *audit.Repo
	vocab       []string
	lastMu      sync.Mutex
	lastUsed    map[string]time.Time
	lastNarrow  map[string]time.Time
}

// NewAuthenticator 构造（vocab 是 scope 词表——assembly 单一源注入）。
func NewAuthenticator(db *state.DB, policy *authz.PolicySet, vocab []string, log *slog.Logger) *Authenticator {
	return &Authenticator{
		db: db, policy: policy, log: log,
		roles:       rolerepo.New(db.Clock()),
		users:       user.New(db.Clock()),
		tokens:      tokenrepo.New(db.Clock()),
		memberships: membershiprepo.New(db.Clock()),
		audits:      audit.New(db.Clock()),
		vocab:       vocab, lastUsed: map[string]time.Time{}, lastNarrow: map[string]time.Time{},
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

// withRequestIdentity 注入身份 + 审计 Team 轴（ADR-0035：已认证请求的
// 审计行 Team 从调用方身份统一铸入；匿名面不标注——审计行落 ”=平台级）。
func withRequestIdentity(ctx context.Context, id *Identity) context.Context {
	ctx = WithIdentity(ctx, id)
	if id != nil {
		ctx = audit.WithTeam(ctx, id.TeamID)
	}
	return ctx
}

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
		return withRequestIdentity(ctx, id), nil
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
		return withRequestIdentity(ctx, id), nil
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
		// 分诊（Q-24）：行不存在 = 凭证无效（正常拒绝面，静默）；其余 =
		// 存储故障——伪装成"无效凭证"会吞掉故障真相，必须以 E_INTERNAL
		// 大声失败（PUBLIC 面按匿名放行的既有语义不受影响）。
		if !errors.Is(err, state.ErrNotFound) {
			a.log.Error("authn: token lookup failed", "err", err)
			return nil, apperr.New("E_INTERNAL", "token lookup failed").WithCause(err)
		}
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
		u, err := a.users.Get(ctx, a.db.Runner(), tok.UserID)
		if err != nil {
			// creator 已删 = Token 终态失效（ADR-0038）。FK 把守下正常不可达
			//（删用户须先吊销名下 Token），DB 手工清理与未来删除面放行时兜底。
			if errors.Is(err, state.ErrNotFound) {
				a.auditNarrowing(ctx, tok, "creator_deleted")
				return nil, a.narrowingErr(tok, "creator_deleted",
					"the token's creator account no longer exists, so the token no longer carries any authority")
			}
			a.log.Error("authn: creator lookup failed", "token", tok.Name, "err", err)
			return nil, apperr.New("E_INTERNAL", "token creator lookup failed").WithCause(err)
		}
		id.UserName = u.Name
		// 实时收窄（ADR-0038 / P6 T1）：有属主 Token 的有效授权 =
		// min(声明, creator 当前)——creator 在 Token 所在 Team 的 membership
		// 角色是"当前授权"真源，逐请求求交。
		if err := a.narrowToCreator(ctx, tok, id); err != nil {
			return nil, err
		}
	}
	return id, nil
}

// narrowToCreator 把 Identity 的 scope 面收窄到 creator 当前授权（原地改
// id.Scopes/scopeSet）。全失（membership 消失/交集为空）返回 403 带原因
// （Agent 可判定"找管理员"而非"重新认证"——P6 裁决）。
func (a *Authenticator) narrowToCreator(ctx context.Context, tok *tokenrepo.Token, id *Identity) error {
	m, err := a.memberships.GetByUser(ctx, a.db.Runner(), tok.UserID, tok.TeamID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			a.log.Error("authn: creator membership lookup failed", "token", tok.Name, "err", err)
			return apperr.New("E_INTERNAL", "token creator membership lookup failed").WithCause(err)
		}
		a.auditNarrowing(ctx, tok, "creator_not_in_team")
		return a.narrowingErr(tok, "creator_not_in_team",
			"the token's creator is no longer a member of the token's team, so the token no longer carries any authority")
	}
	if m.RoleID == tok.RoleID {
		return nil // 同角色：creator 授权恒覆盖声明，求交 = 声明（免读快路径）
	}
	crole, err := a.roles.Get(ctx, a.db.Runner(), m.RoleID)
	if err != nil {
		// membership 角色失联是引用完整性破坏（FK 把守下的不可达）：大声失败
		// 而非静默放行（收窄不可判定时拒绝是 fail-closed）。
		a.log.Error("authn: creator membership references missing role", "token", tok.Name, "role", m.RoleID)
		return apperr.New("E_INTERNAL", "token creator membership references an unavailable role")
	}
	grants, err := identity.ParseScopes(crole.Scopes, a.vocab)
	if err != nil {
		a.log.Error("authn: creator membership role carries scopes outside vocabulary", "role", crole.Name, "err", err)
		return apperr.New("E_INTERNAL", "token creator role carries invalid scopes")
	}
	effective := identity.Meet(id.Scopes, grants)
	if len(effective) == 0 {
		a.auditNarrowing(ctx, tok, "empty_scope_intersection")
		return a.narrowingErr(tok, "empty_scope_intersection",
			"the token's declared scopes no longer intersect its creator's current access, so the token no longer carries any authority")
	}
	if len(effective) != len(id.Scopes) {
		a.log.Info("authn: token narrowed to creator authority",
			"token", tok.Name, "declared", len(id.Scopes), "effective", len(effective))
	}
	set, err := identity.DotForm(effective)
	if err != nil {
		return apperr.New("E_UNAUTHENTICATED", "token role carries invalid scopes")
	}
	id.Scopes = effective
	id.scopeSet = set
	return nil
}

// narrowingErr 铸收窄 403（带原因与去向建议——收窄是管理面事实，不是认证
// 失败；E_UNAUTHENTICATED 会误导 Agent 走重新认证）。
func (a *Authenticator) narrowingErr(tok *tokenrepo.Token, reason, detail string) error {
	return apperr.New("E_FORBIDDEN", "%s (token %q)", detail, tok.Name).
		WithContext("token", tok.Name).
		WithContext("reason", reason).
		WithSuggestion("The token's authority is capped by its creator's current access (ADR-0038). Ask a team admin to restore the creator's access, or revoke this token and mint a new one.")
}

// auditNarrowing 落"收窄生效"审计行（节流：同 Token 60s 一条——重试循环
// 不刷屏；audit.Append 的 Team 轴由 ctx 注入，拦截器已带调用方 Team）。
func (a *Authenticator) auditNarrowing(ctx context.Context, tok *tokenrepo.Token, reason string) {
	now := time.Now()
	a.lastMu.Lock()
	if last, ok := a.lastNarrow[tok.ID]; ok && now.Sub(last) < narrowingAuditInterval {
		a.lastMu.Unlock()
		return
	}
	a.lastNarrow[tok.ID] = now
	a.lastMu.Unlock()
	entry := &audit.Entry{
		ID:       ulid.Make().String(),
		Actor:    "token:" + tok.Name,
		Source:   SourceFromContext(ctx),
		Action:   "token.narrowed_denied",
		Resource: "token/" + tok.ID,
		AfterFP:  reason,
	}
	if err := a.audits.Append(audit.WithTeam(ctx, tok.TeamID), a.db.Runner(), entry); err != nil {
		a.log.Warn("authn: narrowing audit append failed", "token", tok.Name, "err", err)
	}
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

// WithIdentity 注入身份（请求路径由 guard 内部使用；导出面供拦截器链
// 下游与测试夹具注入——WithAuditOverride 同族）。
func WithIdentity(ctx context.Context, id *Identity) context.Context {
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

// HasScope 报告身份是否携带指定 scope（服务内动态提权门消费——静态
// method_auth 注解只能表达 RPC 的最低门，写档形态按请求值运行时校验；
// ADR-0051 决策 6：browse 的 read_write/无执法方言要求 databases:write）。
func (id *Identity) HasScope(resource string, op authz.ScopeOp) bool {
	return id != nil && id.scopeSet.Satisfies(authz.ScopeRule{Resource: resource, Op: op})
}
