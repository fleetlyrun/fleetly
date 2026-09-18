package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// GitKeysService 实现 server.v1.GitKeysService（T2.19）：平台管理的 git
// 公钥（SSH push 认证；v0.1 全局级，admin scope 管理）。公钥指纹入库
// （SHA256，ssh-keygen -lf 同格式）；私钥永不经过平台。生命周期动作审计
// 在 state 层与业务写同事务 fail-closed（gitkey.add / gitkey.remove）。
type GitKeysService struct {
	serverv1.UnimplementedGitKeysServiceServer
	st *state.Store
}

// NewGitKeysService 构造 GitKeysService。
func NewGitKeysService(st *state.Store) *GitKeysService {
	return &GitKeysService{st: st}
}

// AddGitKey 解析 authorized_keys 单行并入库（指纹唯一——重复注册 409 退化
// 信封）。
func (s *GitKeysService) AddGitKey(ctx context.Context, req *serverv1.AddGitKeyRequest) (*serverv1.AddGitKeyResponse, error) {
	line := strings.TrimSpace(req.GetPublicKey())
	if strings.ContainsAny(line, "\n\r") {
		return nil, statusInvalidArgument("public key must be a single authorized_keys line")
	}
	pub, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, statusInvalidArgument("parse public key: not an authorized_keys line (" + err.Error() + ")")
	}
	keyType := pub.Type()
	// 备注缺省取 comment（authorized_keys 第三字段——常见即设备标识）。
	note := req.GetNote()
	if note == "" {
		note = strings.TrimSpace(comment)
	}
	if len(note) > 200 {
		note = note[:200]
	}
	// 入库存原始行（options/形态原样保留）；认证匹配只看指纹——原始行的
	// 解析形态与指纹由本处解析产出，存储即留痕。
	key, err := s.st.CreateGitKey(ctx, state.GitKeyWrite{
		Fingerprint:  gossh.FingerprintSHA256(pub),
		PublicKey:    line,
		KeyType:      keyType,
		Note:         note,
		ActorTokenID: callerTokenID(ctx),
	})
	if err != nil {
		if errors.Is(err, state.ErrGitKeyExists) {
			return nil, conflict("git key already registered (same fingerprint)")
		}
		return nil, err
	}
	return &serverv1.AddGitKeyResponse{
		Id:          key.ID,
		Fingerprint: key.Fingerprint,
		KeyType:     key.KeyType,
		Note:        key.Note,
		CreatedAt:   tstamp(key.CreatedAt),
	}, nil
}

// ListGitKeys 在册公钥列表（公钥为公开材料可回读）。
func (s *GitKeysService) ListGitKeys(ctx context.Context, _ *serverv1.ListGitKeysRequest) (*serverv1.ListGitKeysResponse, error) {
	rows, err := s.st.ListGitKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.GitKeyView, 0, len(rows))
	for _, k := range rows {
		out = append(out, &serverv1.GitKeyView{
			Id:          k.ID,
			Fingerprint: k.Fingerprint,
			KeyType:     k.KeyType,
			Note:        k.Note,
			CreatedAt:   tstamp(k.CreatedAt),
		})
	}
	return &serverv1.ListGitKeysResponse{Keys: out}, nil
}

// RemoveGitKey 删除公钥（幂等语义：不存在 404 退化信封；删除即时生效
// ——在推连接不受影响，新握手即拒绝）。
func (s *GitKeysService) RemoveGitKey(ctx context.Context, req *serverv1.RemoveGitKeyRequest) (*serverv1.RemoveGitKeyResponse, error) {
	if err := s.st.RemoveGitKey(ctx, req.GetId(), callerTokenID(ctx)); err != nil {
		if errors.Is(err, state.ErrGitKeyNotFound) {
			return nil, notFound(fmt.Sprintf("git key not found: %s", req.GetId()))
		}
		return nil, err
	}
	return &serverv1.RemoveGitKeyResponse{Id: req.GetId()}, nil
}
