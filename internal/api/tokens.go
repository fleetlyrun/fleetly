package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TokensService 实现 server.v1.TokensService（T2.17；state-model §2.9）。
// 哈希存储：明文只在 CreateToken 响应出现一次；List 只出备注/scope/
// 哈希前缀。scope 集（admin 蕴含 deploy 蕴含 read）在 containsScope 判定。
type TokensService struct {
	serverv1.UnimplementedTokensServiceServer
	st *state.Store
}

// NewTokensService 构造 TokensService。
func NewTokensService(st *state.Store) *TokensService {
	return &TokensService{st: st}
}

// tokenPrefix 是明文 token 前缀（可识别性；本体 = 24 随机字节 hex）。
const tokenPrefix = "flt_"

// CreateToken 生成并落库新 token：明文仅本次响应可见。
func (s *TokensService) CreateToken(ctx context.Context, req *serverv1.CreateTokenRequest) (*serverv1.CreateTokenResponse, error) {
	plaintext, err := generateToken()
	if err != nil {
		return nil, err
	}
	caller := callerTokenID(ctx)
	tok, err := s.st.CreateToken(ctx, state.TokenWrite{
		Hash:         state.HashToken(plaintext),
		Name:         req.GetNote(),
		Scopes:       normalizeScopes(req.GetScopes()),
		ActorTokenID: caller,
	})
	if err != nil {
		return nil, err
	}
	return &serverv1.CreateTokenResponse{
		Id:        tok.ID,
		Token:     plaintext, // 仅此一次
		Note:      tok.Name,
		Scopes:    strings.Split(tok.Scopes, ","),
		CreatedAt: tstamp(tok.CreatedAt),
	}, nil
}

// ListTokens 在册 token 列表（无敏感投影；完整哈希与明文永不回读）。
func (s *TokensService) ListTokens(ctx context.Context, _ *serverv1.ListTokensRequest) (*serverv1.ListTokensResponse, error) {
	rows, err := s.st.ListTokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.TokenView, 0, len(rows))
	for _, t := range rows {
		out = append(out, &serverv1.TokenView{
			Id:         t.ID,
			Note:       t.Name,
			Scopes:     strings.Split(t.Scopes, ","),
			HashPrefix: t.HashPrefix,
			CreatedAt:  tstamp(t.CreatedAt),
			LastUsedAt: tstamp(t.LastUsedAt),
			RevokedAt:  tstamp(t.RevokedAt),
		})
	}
	return &serverv1.ListTokensResponse{Tokens: out}, nil
}

// RevokeToken 吊销（幂等；不存在 404）。M4-6 最后管理员守卫：吊销后平台
// 必须仍存在 ≥1 枚未吊销 admin token——依次吊销全部 admin 会使平台锁死
// （重启也不补种 bootstrap：HasAnyToken 已见 token 行，一次性语义），最后
// 一枚的吊销被守卫拒绝（E_TOKEN_LAST_ADMIN 409，提示先创建新 token）。
// 守卫判定与吊销在 state 层同一事务内闭合（RevokeTokenGuardLastAdmin）。
func (s *TokensService) RevokeToken(ctx context.Context, req *serverv1.RevokeTokenRequest) (*serverv1.RevokeTokenResponse, error) {
	err := s.st.RevokeTokenGuardLastAdmin(ctx, req.GetId(), callerTokenID(ctx), func(scopes string) bool {
		return containsScope(scopes, ScopeAdmin)
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrTokenNotFound):
			return nil, notFound("token not found: " + req.GetId())
		case errors.Is(err, state.ErrTokenLastAdmin):
			return nil, apperr.New("E_TOKEN_LAST_ADMIN",
				"token %s is the last non-revoked admin token; revoking it would leave the platform unmanageable (a restart does not re-seed the bootstrap token)",
				req.GetId()).
				WithContext("token", req.GetId()).
				WithContext("reason", "last_admin")
		default:
			return nil, err
		}
	}
	return &serverv1.RevokeTokenResponse{Id: req.GetId(), RevokedAt: "revoked"}, nil
}

// generateToken 生成明文 token（crypto/rand 24 字节 → 48 hex）。
func generateToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("api: generate token: %w", err)
	}
	return tokenPrefix + hex.EncodeToString(buf), nil
}

// GenerateBootstrapAdminToken 生成并落库 admin token（T2.17 安装引导；
// 明文返回一次，库内只存哈希）。审计 ActorTokenID 空 = 自举（无调用方
// token）。已存在任意 token 时调用方应跳过（HasAnyToken 谓词）。
func GenerateBootstrapAdminToken(ctx context.Context, st *state.Store, note string) (string, error) {
	plaintext, err := generateToken()
	if err != nil {
		return "", err
	}
	if _, err := st.CreateToken(ctx, state.TokenWrite{
		Hash:   state.HashToken(plaintext),
		Name:   note,
		Scopes: ScopeAdmin,
	}); err != nil {
		return "", err
	}
	return plaintext, nil
}

// normalizeScopes 归一 scope 集（去重、保序）。
func normalizeScopes(in []string) string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// callerTokenID 取调用方 token ID（审计 actor_token_id；bootstrap 场景
// 可空）。
func callerTokenID(ctx context.Context) string {
	if p, ok := PrincipalFromContext(ctx); ok {
		return p.TokenID
	}
	return ""
}
