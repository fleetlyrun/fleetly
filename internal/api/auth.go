// Package api 是 fleetly 控制面 v0.1 API 服务实现与拦截器链（T2.17；
// 架构 D21：proto 唯一真源，鉴权/限流在 gRPC 拦截器链）。
//
// 鉴权模型（architecture §4.2 安全基线）：Authorization: Bearer <token> →
// tokens 表哈希比对（常量时间二次校验）→ scope 判定（read ⊂ deploy ⊂
// admin，方法级映射在本包 scope.go）——**无 token / 错 token 全部 401**，
// scope 不足 403。错误码取舍：401/403 无专用稳定码，用信封退化形态
// （code 空 + grpc code 机械映射 HTTP），与 FZ-2 口径一致（本阶段
// internal/errcode 不在允许改动清单，不加码）。
package api

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// scope 词表（state-model §2.9 / architecture §4.2：read/deploy/admin）。
const (
	ScopeRead   = "read"
	ScopeDeploy = "deploy"
	ScopeAdmin  = "admin"
)

// containsScope 报告 scope 集（逗号分隔存储形态）是否蕴含所需 scope
// （admin ⊃ deploy ⊃ read）。
func containsScope(scopes, need string) bool {
	for _, s := range strings.Split(scopes, ",") {
		switch strings.TrimSpace(s) {
		case ScopeAdmin:
			return true
		case ScopeDeploy:
			if need == ScopeRead || need == ScopeDeploy {
				return true
			}
		case ScopeRead:
			if need == ScopeRead {
				return true
			}
		}
	}
	return false
}

// Principal 是通过鉴权的调用方身份（handler 侧审计 actor_token_id 消费）。
type Principal struct {
	TokenID string
	Scopes  []string
}

type principalKey struct{}

// PrincipalFromContext 取调用方身份（未鉴权路径不存在——拦截器链强制）。
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Authenticator 是 token 认证器（state 层哈希认证 + scope 判定 + per-token
// 限流——限流在认证成功后判定，身份先于限流成立）。
type Authenticator struct {
	st      *state.Store
	limiter *rateLimiter
}

// NewAuthenticator 构造认证器（内置缺省参数限流器：宽松防滥用）。
func NewAuthenticator(st *state.Store) *Authenticator {
	return &Authenticator{st: st, limiter: newRateLimiter(0, 0)}
}

// Authenticate 校验 Bearer 凭据并返回身份；失败返回 grpc status（401/403
// 信封退化形态）。required scope 判定在 authorize（拦截器按方法映射调用）。
func (a *Authenticator) Authenticate(ctx context.Context, authorization string) (Principal, error) {
	token := bearerToken(authorization)
	if token == "" {
		return Principal{}, statusEnvelope(codes.Unauthenticated, "missing bearer token")
	}
	tok, err := a.st.AuthenticateToken(ctx, token)
	if err != nil {
		// ErrTokenInvalid / ErrTokenRevoked / 其他读取故障：统一 401（不
		// 泄漏存在性与内部状态；读取故障按拒绝处理属 fail-closed）。
		return Principal{}, statusEnvelope(codes.Unauthenticated, "invalid or revoked token")
	}
	if a.limiter != nil && !a.limiter.allow(tok.ID) {
		return Principal{}, statusEnvelope(codes.ResourceExhausted, "rate limit exceeded for token")
	}
	return Principal{TokenID: tok.ID, Scopes: strings.Split(tok.Scopes, ",")}, nil
}

// bearerToken 解析 "Bearer <token>" 头（大小写不敏感 scheme；其余形态
// 一律视为缺失——token 本体不含空格）。
func bearerToken(authorization string) string {
	parts := strings.SplitN(strings.TrimSpace(authorization), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// authorize 判定 scope：不足 → 403（PermissionDenied，信封退化形态）。
func (a *Authenticator) authorize(p Principal, required string) error {
	if containsScope(strings.Join(p.Scopes, ","), required) {
		return nil
	}
	return statusEnvelope(codes.PermissionDenied,
		fmt.Sprintf("token scope insufficient (requires %s)", required))
}

// ── 拦截器 ───────────────────────────────────────────────────────────────────

// authExemptPrefixes 豁免前缀：框架服务（health/reflection）与显式公开
// 方法（SystemService.Ping——T2.17 契约：Ping 与 healthz 豁免鉴权）。
var authExemptPrefixes = []string{
	"/grpc.health.v1.",
	"/grpc.reflection.",
}

// authExemptMethods 显式豁免的完整方法名。
var authExemptMethods = map[string]bool{
	"/fleetly.server.v1.SystemService/Ping": true,
}

func authExempt(fullMethod string) bool {
	if authExemptMethods[fullMethod] {
		return true
	}
	for _, prefix := range authExemptPrefixes {
		if strings.HasPrefix(fullMethod, prefix) {
			return true
		}
	}
	return false
}

// UnaryAuthInterceptor 一元鉴权拦截器（链位：最外层，先于限流与
// protovalidate）。
func (a *Authenticator) UnaryAuthInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if authExempt(info.FullMethod) {
			return handler(ctx, req)
		}
		required, ok := RequiredScope(info.FullMethod)
		if !ok {
			// 未登记方法 fail-closed：按 admin 拒绝路径处理（新方法漏登记
			// 时宁拒勿放——拦截器链是安全边界）。
			required = ScopeAdmin
		}
		p, err := a.authenticateAndAuthorize(ctx, info.FullMethod, required)
		if err != nil {
			return nil, err
		}
		ctx = context.WithValue(ctx, principalKey{}, p)
		ctx = context.WithValue(ctx, actionKey{}, methodAction(info.FullMethod))
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor 流式鉴权拦截器（Follow/Watch 双流同矩阵）。
func (a *Authenticator) StreamAuthInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if authExempt(info.FullMethod) {
			return handler(srv, ss)
		}
		required, ok := RequiredScope(info.FullMethod)
		if !ok {
			required = ScopeAdmin
		}
		p, err := a.authenticateAndAuthorize(ss.Context(), info.FullMethod, required)
		if err != nil {
			return err
		}
		ctx := context.WithValue(ss.Context(), principalKey{}, p)
		ctx = context.WithValue(ctx, actionKey{}, methodAction(info.FullMethod))
		return handler(srv, &authorizedStream{ServerStream: ss, ctx: ctx})
	}
}

// authenticateAndAuthorize 从 metadata 取凭据走认证 + scope 判定。
func (a *Authenticator) authenticateAndAuthorize(ctx context.Context, fullMethod, required string) (Principal, error) {
	p, err := a.Authenticate(ctx, authorizationFromMetadata(ctx))
	if err != nil {
		return Principal{}, err
	}
	if err := a.authorize(p, required); err != nil {
		return Principal{}, err
	}
	return p, nil
}

// authorizationFromMetadata 取 Authorization 头（gateway 转发形态
// "authorization" 优先；grpcgateway- 前缀形态兜底）。
func authorizationFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, key := range []string{"authorization", "grpcgateway-authorization"} {
		if vals := md.Get(key); len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

// authorizedStream 覆写 Context 的 ServerStream（把 Principal 注入流式
// handler 的 ctx——grpc.ServerStream.Context() 不可直接替换，经包装实现）。
type authorizedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authorizedStream) Context() context.Context { return s.ctx }

// statusEnvelope 构造退化信封错误：**不附加 ErrorResponse detail**——
// gateway 侧 EnvelopeFromGRPCStatus 对无 detail status 走退化路径（code
// 留空 + GRPCCodeToHTTP 机械映射 401/403/429），与 FZ-2 口径一致。若在
// 此附加 code 空的信封 detail，反而会被注册表映射兜底成 500（detail 内
// code 空时 HTTPStatus 无从判定）——因此退化形态 = 纯 status，信封由
// gateway 渲染。
func statusEnvelope(c codes.Code, message string) error {
	return status.Error(c, message)
}
