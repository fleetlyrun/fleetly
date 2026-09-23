package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// AuthService 实现 server.v1.AuthService（v0.3 W1，rbac-teams 设计 §2.1/
// §2.2/§5）：自助注册（窗口规则 + 首用户全量落位在 state.RegisterUser 单
// 事务内）、口令登录、服务端会话（Cookie fleetly_session）。
//
// 会话下发：Register/Login 经 gRPC 响应 header metadata（键 "set-cookie"）
// ——gateway 面由 OutgoingHeaderMatcher（internal/runtime/gateway.go）映射回
// HTTP Set-Cookie；gRPC 直连面无 cookie 语义，明文会话值随 metadata 出现
// （CLI 不消费会话——会话是浏览器面凭据，设计 §2.2）。
//
// 限流：注册/登录按 email+来源 IP 双键限流（设计 §2.1，10 次/分钟——防撞
// 库，无验证码依赖）。限流在业务判定**之前**（fail-closed：先限后做）。
//
// 审计：auth.registered / auth.login / auth.login_failed / auth.logout 在
// 流程面落档（设计 §6；state 层保持纯原语不写 auth.*——sessions.go 同口径），
// actor = user:<id>（登录失败未验证身份，actor=human、target 带 email）。
type AuthService struct {
	serverv1.UnimplementedAuthServiceServer
	st *state.Store
	// authLimiter 是注册/登录共用的双键限流器（email:... 与 ip:... 各一桶
	// ——任一耗尽即拒，撞库者无法用单 email 换多 IP 配额）。
	authLimiter *rateLimiter
	// now 是可注入时钟（会话过期计算；测试驱动）。
	now func() time.Time
}

// 会话 TTL（rbac-teams 设计 §2.2：7 天滑动 + 30 天绝对上限；config
// auth.session_ttl_hours 可调项未随本票落 config 面——W2 Console 会话面
// 收口，当前实现取设计缺省 7 天固定期）。
const sessionTTL = 7 * 24 * time.Hour

// 注册/登录限流参数（设计 §2.1：如 10 次/分钟）。
const (
	authRatePerMin  = 10
	authRateBurst   = 10
	authRatePerSecF = float64(authRatePerMin) / 60.0
)

// NewAuthService 构造 AuthService。
func NewAuthService(st *state.Store) *AuthService {
	return &AuthService{
		st:          st,
		authLimiter: &rateLimiter{buckets: make(map[string]*tokenBucket), rate: authRatePerSecF, burst: authRateBurst, now: time.Now},
		now:         time.Now,
	}
}

// Register 自助注册（窗口规则在 state.RegisterUser 事务内原子判定）：
// 成功 = 用户 + 个人队 + 默认项目落位（首用户另含平台管理员置位与
// bootstrap 吊销）+ 会话下发。
func (s *AuthService) Register(ctx context.Context, req *serverv1.RegisterRequest) (*serverv1.RegisterResponse, error) {
	if !s.allowAuth(ctx, req.GetEmail()) {
		return nil, statusEnvelope(codes.ResourceExhausted, "rate limit exceeded for registration")
	}
	rr, err := s.st.RegisterUser(ctx, state.RegisterWrite{
		Email:       req.GetEmail(),
		Password:    req.GetPassword(),
		DisplayName: req.GetDisplayName(),
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrRegistrationClosed):
			return nil, apperr.New("E_REGISTRATION_CLOSED",
				"self-service registration is closed; ask a platform administrator to create the account").
				WithContext("reason", "registration_closed")
		case errors.Is(err, state.ErrEmailTaken):
			return nil, conflict("email already registered: " + req.GetEmail())
		default:
			return nil, err
		}
	}
	if _, err := s.createSession(ctx, rr.User.ID); err != nil {
		return nil, err
	}
	if err := s.writeAuthAudit(ctx, "auth.registered", rr.User.ID, "ok", "user:"+rr.User.ID); err != nil {
		return nil, err
	}
	return &serverv1.RegisterResponse{User: userView(rr.User)}, nil
}

// Login 口令登录：认证（不泄漏存在性）→ 会话下发。失败入审计
// （auth.login_failed，result=error，不落口令——设计 §6）。
func (s *AuthService) Login(ctx context.Context, req *serverv1.LoginRequest) (*serverv1.LoginResponse, error) {
	if !s.allowAuth(ctx, req.GetEmail()) {
		return nil, statusEnvelope(codes.ResourceExhausted, "rate limit exceeded for login")
	}
	u, err := s.st.AuthenticateUser(ctx, req.GetEmail(), req.GetPassword())
	if err != nil {
		if errors.Is(err, state.ErrInvalidCredentials) {
			if auditErr := s.writeAuthAudit(ctx, "auth.login_failed", "",
				"error", "email:"+loginTargetEmail(req.GetEmail())); auditErr != nil {
				return nil, auditErr
			}
			return nil, statusEnvelope(codes.Unauthenticated, "invalid email or password")
		}
		return nil, err
	}
	if _, err := s.createSession(ctx, u.ID); err != nil {
		return nil, err
	}
	if err := s.writeAuthAudit(ctx, "auth.login", u.ID, "ok", u.ID); err != nil {
		return nil, err
	}
	return &serverv1.LoginResponse{User: userView(u)}, nil
}

// Logout 注销当前会话（删行 + 清 cookie；会话行已消失按幂等成功——旧
// cookie 重复注销不报错）。仅会话凭据可调（Bearer 面无会话可注销）。
func (s *AuthService) Logout(ctx context.Context, _ *serverv1.LogoutRequest) (*serverv1.LogoutResponse, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.SessionID == "" {
		return nil, statusInvalidArgument("logout requires a session cookie credential")
	}
	if err := s.st.RevokeSession(ctx, p.SessionID); err != nil && !errors.Is(err, state.ErrSessionNotFound) {
		return nil, err
	}
	clearSessionCookie(ctx)
	if err := s.writeAuthAudit(ctx, "auth.logout", p.UserID, "ok", "user:"+p.UserID); err != nil {
		return nil, err
	}
	return &serverv1.LogoutResponse{}, nil
}

// LogoutAll 全部注销：删除当前用户全部会话行（含本会话）。
func (s *AuthService) LogoutAll(ctx context.Context, _ *serverv1.LogoutAllRequest) (*serverv1.LogoutAllResponse, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return nil, statusInvalidArgument("logout-all requires a user credential (session or user PAT)")
	}
	n, err := s.st.RevokeUserSessions(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	clearSessionCookie(ctx)
	if err := s.writeAuthAudit(ctx, "auth.logout", p.UserID, "ok", "user:"+p.UserID); err != nil {
		return nil, err
	}
	return &serverv1.LogoutAllResponse{SessionsRevoked: n}, nil
}

// Me 当前身份投影：user + 所属团队与角色（团队 slug/name 补全按 team_id
// 逐行取——团队面列表原语属 W2，本票只读投影量级 = 单用户团队数）。
func (s *AuthService) Me(ctx context.Context, _ *serverv1.MeRequest) (*serverv1.MeResponse, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return nil, statusInvalidArgument("me requires a user credential (session or user PAT)")
	}
	u, err := s.st.GetUser(ctx, p.UserID)
	if err != nil {
		if errors.Is(err, state.ErrUserNotFound) {
			// 属主行缺失（理论不可达：state 无用户物理删除原语）按凭据失效
			// 处理——fail-closed。
			return nil, statusEnvelope(codes.Unauthenticated, "invalid credential")
		}
		return nil, err
	}
	memberships, err := s.st.ListUserMemberships(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	teams := make([]*serverv1.TeamMembership, 0, len(memberships))
	for _, m := range memberships {
		t, err := s.st.GetTeam(ctx, m.TeamID)
		if err != nil {
			if errors.Is(err, state.ErrTeamNotFound) {
				continue // 成员关系残留而团队行缺失：跳过（不阻塞身份投影）
			}
			return nil, err
		}
		teams = append(teams, &serverv1.TeamMembership{
			TeamId:   t.ID,
			TeamSlug: t.Slug,
			TeamName: t.Name,
			Role:     m.Role,
		})
	}
	return &serverv1.MeResponse{User: userView(u), Teams: teams}, nil
}

// AcceptInvite 消费一次性邀请（W1 落面——邀请的产生面随 W2
// TeamsService.CreateInvite 落地；本方法合法消费 state.ConsumeInvite 原语）。
func (s *AuthService) AcceptInvite(ctx context.Context, req *serverv1.AcceptInviteRequest) (*serverv1.AcceptInviteResponse, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return nil, statusInvalidArgument("accepting an invite requires a user credential (session or user PAT)")
	}
	inv, err := s.st.ConsumeInvite(ctx, req.GetToken(), p.UserID, p.UserID, callerTokenID(ctx))
	if err != nil {
		if errors.Is(err, state.ErrInviteInvalid) {
			// E_INVITE_INVALID 稳定码随 W2 TeamsService（rbac-teams §5 错误
			// 码清单）登记——W1 退化信封 409（不泄漏邀请具体状态）。
			return nil, conflict("invite is invalid, expired, or already used")
		}
		return nil, err
	}
	t, err := s.st.GetTeam(ctx, inv.TeamID)
	if err != nil {
		return nil, err
	}
	return &serverv1.AcceptInviteResponse{
		TeamId:   t.ID,
		TeamSlug: t.Slug,
		TeamName: t.Name,
		Role:     inv.Role,
	}, nil
}

// GetRegistrationState 登录页开关注册入口（设计 §5/§7）：无用户窗口恒开；
// 其后 = auth.registration（缺省 closed）。
func (s *AuthService) GetRegistrationState(ctx context.Context, _ *serverv1.GetRegistrationStateRequest) (*serverv1.GetRegistrationStateResponse, error) {
	hasUsers, err := s.st.HasAnyUser(ctx)
	if err != nil {
		return nil, err
	}
	if !hasUsers {
		return &serverv1.GetRegistrationStateResponse{Open: true, HasUsers: false}, nil
	}
	settings, err := s.st.LoadAuthSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetRegistrationStateResponse{
		Open:     settings.Registration == state.AuthRegistrationOpen,
		HasUsers: true,
	}, nil
}

// ── 内部 helpers ─────────────────────────────────────────────────────────────

// allowAuth 消费注册/登录限流配额（email + 来源 IP 双键，任一耗尽即拒）。
func (s *AuthService) allowAuth(ctx context.Context, email string) bool {
	if s.authLimiter == nil {
		return true
	}
	if !s.authLimiter.allow("email:" + loginTargetEmail(email)) {
		return false
	}
	return s.authLimiter.allow("ip:" + clientIPFromContext(ctx))
}

// createSession 落会话行并写 Set-Cookie 响应头（明文只出现这一次）。
func (s *AuthService) createSession(ctx context.Context, userID string) (string, error) {
	_, plaintext, err := s.st.CreateSession(ctx, userID, s.now().UTC().Add(sessionTTL))
	if err != nil {
		return "", err
	}
	setSessionCookie(ctx, plaintext, int(sessionTTL/time.Second))
	return plaintext, nil
}

// writeAuthAudit 落 auth.* 审计（独立小事务——认证流程面审计；actor =
// user:<id>，失败路径 actor=human、target 承载登录标识）。审计失败随流程
// 失败（fail-closed；登录成功而审计失败的窗口以整笔回滚语义从宽——不回滚
// 已生效的会话/注册，返回错误让调用方感知）。
func (s *AuthService) writeAuthAudit(ctx context.Context, action, userID, result, target string) error {
	actor := "human"
	if userID != "" {
		actor = "user:" + userID
	}
	return s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:        actor,
			ActorTokenID: callerTokenID(ctx),
			Action:       action,
			Target:       target,
			Result:       result,
		})
	})
}

// loginTargetEmail 归一限流与审计用的登录标识（小写去空白；畸形输入按
// 原样收敛进键空间——不影响有界性）。
func loginTargetEmail(email string) string {
	out := make([]byte, 0, len(email))
	for _, r := range email {
		c := byte(r)
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' || c == '\t' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// clientIPFromContext 取来源 IP（X-Forwarded-For 首值优先——自托管部署在
// 反代后的形态；直连形态回落 gRPC peer host；均不可得用 "unknown" 占位
// ——限流键空间保持有界）。
func clientIPFromContext(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-forwarded-for"); len(vals) > 0 && vals[0] != "" {
			host, _, err := net.SplitHostPort(vals[0])
			if err != nil {
				host = vals[0]
			}
			return host
		}
	}
	if pr, ok := peer.FromContext(ctx); ok && pr.Addr != nil {
		host, _, err := net.SplitHostPort(pr.Addr.String())
		if err != nil {
			return pr.Addr.String()
		}
		return host
	}
	return "unknown"
}

// setSessionCookie 写 Set-Cookie 响应头（HttpOnly + SameSite=Lax + Path=/；
// Secure 随 TLS 模式——gRPC handler 侧拿不到 gateway 的 TLS 上下文，该位
// 挂账 W2 Console 会话面随 TLS 模式注入，rbac-teams §2.2）。
func setSessionCookie(ctx context.Context, plaintext string, maxAge int) {
	_ = grpc.SetHeader(ctx, metadata.Pairs("set-cookie", fmt.Sprintf(
		"%s=%s; Path=/; HttpOnly; SameSite=Lax; Max-Age=%d",
		sessionCookieName, plaintext, maxAge)))
}

// clearSessionCookie 写过期 Set-Cookie（注销/全部注销后浏览器立即清掉）。
func clearSessionCookie(ctx context.Context) {
	_ = grpc.SetHeader(ctx, metadata.Pairs("set-cookie", fmt.Sprintf(
		"%s=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0", sessionCookieName)))
}

// userView 是 state.User → proto 投影（口令哈希不在任何通道）。
func userView(u state.User) *serverv1.UserView {
	return &serverv1.UserView{
		Id:              u.ID,
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		IsPlatformAdmin: u.IsPlatformAdmin,
		CreatedAt:       tstamp(u.CreatedAt),
		DisabledAt:      tstamp(u.DisabledAt),
	}
}

// generateTemporaryPassword 生成临时口令（crypto/rand 16 字节 → 32 hex；
// 明文仅随创建/重置响应出现一次，不入库/审计/日志）。
func generateTemporaryPassword() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("api: generate temporary password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
