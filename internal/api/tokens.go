package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
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

// RevokeToken 吊销（幂等；不存在 404）。
func (s *TokensService) RevokeToken(ctx context.Context, req *serverv1.RevokeTokenRequest) (*serverv1.RevokeTokenResponse, error) {
	if err := s.st.RevokeToken(ctx, req.GetId(), callerTokenID(ctx)); err != nil {
		if errors.Is(err, state.ErrTokenNotFound) {
			return nil, notFound("token not found: " + req.GetId())
		}
		return nil, err
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
