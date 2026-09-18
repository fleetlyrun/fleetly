package api

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// EnvService 实现 server.v1.EnvService（T2.17）：Set/Remove 走 pending
// 语义（state 层内置），Get 明文解密（admin scope 在拦截器链强制），
// List 恒不出值。值加密边界：入站明文 → box.Encrypt 落库；出站解密仅在
// GetEnv 显式路径。
type EnvService struct {
	serverv1.UnimplementedEnvServiceServer
	st  *state.Store
	box *secrets.Box
}

// NewEnvService 构造 EnvService。
func NewEnvService(st *state.Store, box *secrets.Box) *EnvService {
	return &EnvService{st: st, box: box}
}

// SetEnv 设置平台 env（pending；值密文落库，审计在 state 层 fail-closed）。
func (s *EnvService) SetEnv(ctx context.Context, req *serverv1.SetEnvRequest) (*serverv1.SetEnvResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	ciphertext, err := s.box.Encrypt([]byte(req.GetValue()))
	if err != nil {
		return nil, err
	}
	if _, err := s.st.SetAppEnv(ctx, app.ID, req.GetKey(), string(ciphertext), "platform"); err != nil {
		return nil, err
	}
	return &serverv1.SetEnvResponse{App: app.Name, Key: req.GetKey(), Status: string(state.EnvStatusPending)}, nil
}

// GetEnv 读回明文（admin scope 专用路径；密文解密失败按内部错误透出）。
func (s *EnvService) GetEnv(ctx context.Context, req *serverv1.GetEnvRequest) (*serverv1.GetEnvResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	row, err := s.st.GetAppEnv(ctx, app.ID, req.GetKey())
	if err != nil {
		if errors.Is(err, state.ErrEnvNotFound) {
			return nil, notFound("env var not found: " + req.GetKey())
		}
		return nil, err
	}
	plaintext, err := s.box.Decrypt([]byte(row.Value))
	if err != nil {
		return nil, err
	}
	return &serverv1.GetEnvResponse{
		App:    app.Name,
		Key:    row.Key,
		Value:  string(plaintext),
		Status: string(row.Status),
	}, nil
}

// ListEnv 键名/来源/状态位（值恒脱敏）。
func (s *EnvService) ListEnv(ctx context.Context, req *serverv1.ListEnvRequest) (*serverv1.ListEnvResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	rows, err := s.st.ListAppEnv(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.EnvVarView, 0, len(rows))
	for _, row := range rows {
		out = append(out, &serverv1.EnvVarView{
			Key:       row.Key,
			Source:    row.Source,
			Status:    string(row.Status),
			CreatedAt: timestamppb.New(row.CreatedAt),
			UpdatedAt: timestamppb.New(row.UpdatedAt),
		})
	}
	return &serverv1.ListEnvResponse{EnvVars: out}, nil
}

// RemoveEnv 删除平台 env（幂等性不做：键不存在 404；台账立即删行，运行
// 实例 env 快照随下次部署更新）。
func (s *EnvService) RemoveEnv(ctx context.Context, req *serverv1.RemoveEnvRequest) (*serverv1.RemoveEnvResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	if err := s.st.DeleteAppEnv(ctx, app.ID, req.GetKey()); err != nil {
		if errors.Is(err, state.ErrEnvNotFound) {
			return nil, notFound("env var not found: " + req.GetKey())
		}
		return nil, err
	}
	return &serverv1.RemoveEnvResponse{App: app.Name, Key: req.GetKey(), Status: string(state.EnvStatusPending)}, nil
}
