package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/grpc/codes"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// GitDeploySource 是 DeployFromGit RPC 的 compose 源端口（实现方在
// internal/gitserver——compose 真源在其 bare 仓库对象库；方向纪律：api
// 定义端口、不感知实现类型）。
type GitDeploySource interface {
	// DeployFromGitPush 以 git push 语义入队部署：每次调用建部署记录
	// （显式用户动作，不去重——幂等口径绑定在票面）；返回记录与校验期
	// 警告。actorTokenID 记录钩子回调 token（可空）。
	DeployFromGitPush(ctx context.Context, app, sha, ref, actorTokenID string) (state.DeployRecord, []compose.Warning, error)
}

// DeploymentsService 实现 server.v1.DeploymentsService（T2.17）。
//
// Deploy 契约（proto 取舍注记）：compose 内容字节 + app 名入队，返回
// deployment id 后立即返回；终态由客户端轮询 GetDeployment 或消费事件流。
// compose 落临时文件后走受控子集校验（compose.Load），文件路径随行落库
// ——引擎 preparing 阶段重载复核（同 CLI 形态；临时文件生命周期 = 引擎
// 消费完成，v0.1 不做清理回收，遗留记录）。
type DeploymentsService struct {
	serverv1.UnimplementedDeploymentsServiceServer
	st  *state.Store
	git GitDeploySource
}

// NewDeploymentsService 构造 DeploymentsService（git 源端口可 nil——
// DeployFromGit 届时显式不可用，进程内夹具形态）。
func NewDeploymentsService(st *state.Store, git GitDeploySource) *DeploymentsService {
	return &DeploymentsService{st: st, git: git}
}

// ListDeployments 按应用列部署（created_at 倒序）。
func (s *DeploymentsService) ListDeployments(ctx context.Context, req *serverv1.ListDeploymentsRequest) (*serverv1.ListDeploymentsResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.st.ListAppDeployments(ctx, app.ID, limit)
	if err != nil {
		return nil, err
	}
	return &serverv1.ListDeploymentsResponse{Deployments: deploymentViews(rows)}, nil
}

// GetDeployment 单条部署。
func (s *DeploymentsService) GetDeployment(ctx context.Context, req *serverv1.GetDeploymentRequest) (*serverv1.GetDeploymentResponse, error) {
	rec, err := s.st.GetDeployment(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, state.ErrDeploymentNotFound) {
			return nil, notFound("deployment not found: " + req.GetId())
		}
		return nil, err
	}
	return &serverv1.GetDeploymentResponse{Deployment: deploymentView(rec)}, nil
}

// Deploy 入队部署（同 CLI：入队前受控子集校验——compose 违约不动底座、
// 不入队；deployment.queued 事件与审计同事务 fail-closed）。校验期警告随
// 响应带出（T2.18：CLI 改经 RPC 入队后保留警告的人读呈现）。
func (s *DeploymentsService) Deploy(ctx context.Context, req *serverv1.DeployRequest) (*serverv1.DeployResponse, error) {
	// 入队前受控子集校验：内容落临时文件（引擎 preparing 重载复核同路径）。
	dir, err := os.MkdirTemp("", "fleetly-compose-")
	if err != nil {
		return nil, fmt.Errorf("create compose temp dir: %w", err)
	}
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, req.GetCompose(), 0o600); err != nil { //nolint:gosec // G306：compose 内容非密钥，0600 保守
		return nil, fmt.Errorf("write compose temp file: %w", err)
	}
	spec, warnings, err := compose.Load(ctx, path)
	if err != nil {
		return nil, err // apperr（E_COMPOSE_*）原样透传
	}

	app, err := ensureApp(ctx, s.st, spec.Name)
	if err != nil {
		return nil, err
	}
	rec, err := s.st.CreateDeployment(ctx, state.DeployRecord{
		AppID:       app.ID,
		AppName:     spec.Name,
		Kind:        "deploy",
		SpecHash:    spec.SpecHash,
		ComposePath: path,
	})
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if _, err := tx.AppendEvent(ctx, state.Event{
			Name:    "deployment.queued",
			Subject: "deployment:" + rec.ID,
			Payload: `{"deployment":"` + rec.ID + `","app":"` + spec.Name + `"}`,
		}); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, auditEntry(ctx, "deployment:"+rec.ID,
			`{"app":"`+spec.Name+`","spec_hash":"`+spec.SpecHash+`","source":"api"}`))
	})
	if err != nil {
		return nil, err
	}
	return &serverv1.DeployResponse{
		DeploymentId: rec.ID,
		App:          app.Name,
		Status:       string(state.DeployQueued),
		Warnings:     composeWarnings(warnings),
	}, nil
}

// DeployFromGit 是 git push(SSH) 触发入口的入队面（T2.19）：post-receive
// 钩子经 loopback REST 携带 hook token（deploy scope）调用。compose 字节
// 由服务端从 bare 仓库自取（真源在 git 对象库，不信任客户端传字节）；sha
// 为 40 位 commit（protovalidate 形状 + 服务端十六进制复核）。审计在源端
// 实现（git.push_deploy，actor=system + 钩子 token id）。
func (s *DeploymentsService) DeployFromGit(ctx context.Context, req *serverv1.DeployFromGitRequest) (*serverv1.DeployFromGitResponse, error) {
	if s.git == nil {
		return nil, statusEnvelope(codes.Internal, "git deploy source not configured")
	}
	if !gitSHAValid(req.GetSha()) {
		return nil, statusInvalidArgument("sha must be 40 hex chars")
	}
	rec, warnings, err := s.git.DeployFromGitPush(ctx, req.GetApp(), req.GetSha(), req.GetRef(), callerTokenID(ctx))
	if err != nil {
		return nil, err // apperr（E_COMPOSE_*）原样透传；其余按信封退化
	}
	return &serverv1.DeployFromGitResponse{
		DeploymentId: rec.ID,
		App:          rec.AppName,
		Status:       string(rec.Status),
		Warnings:     composeWarnings(warnings),
	}, nil
}

// gitSHAValid 复核 commit 形态（40 位小写十六进制；protovalidate 只约束
// 长度）。
func gitSHAValid(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, c := range sha {
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

// CancelDeployment 置位取消（受限语义：未切流可取消；曾健康/终态 409
// ——引擎侧二次校验为权威）。
func (s *DeploymentsService) CancelDeployment(ctx context.Context, req *serverv1.CancelDeploymentRequest) (*serverv1.CancelDeploymentResponse, error) {
	rec, err := s.st.GetDeployment(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, state.ErrDeploymentNotFound) {
			return nil, notFound("deployment not found: " + req.GetId())
		}
		return nil, err
	}
	if !rec.FirstHealthyAt.IsZero() || rec.Status == state.DeployObserving || rec.Status.Terminal() {
		return nil, apperrConflict(rec)
	}
	set := true
	if err := s.st.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{CancelRequested: &set}); err != nil {
		return nil, err
	}
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, auditEntry(ctx, "deployment:"+rec.ID, `{"cancel_requested":true,"source":"api"}`))
	}); err != nil {
		return nil, err
	}
	return &serverv1.CancelDeploymentResponse{Id: rec.ID, Status: string(rec.Status)}, nil
}

// RollbackDeployment 回滚入队（engine.EnqueueRollback：目标解析失败
// E_ROLLBACK_NO_TARGET；事件/审计在引擎入队路径内置）。
func (s *DeploymentsService) RollbackDeployment(ctx context.Context, req *serverv1.RollbackDeploymentRequest) (*serverv1.RollbackDeploymentResponse, error) {
	if _, err := resolveApp(ctx, s.st, req.GetApp()); err != nil {
		return nil, err
	}
	rec, err := engine.EnqueueRollback(ctx, s.st, engine.RollbackInput{
		AppName:          req.GetApp(),
		TargetRevisionID: req.GetTargetRevisionId(),
		Actor:            "human",
	})
	if err != nil {
		return nil, err // apperr（E_ROLLBACK_NO_TARGET 等）原样透传
	}
	return &serverv1.RollbackDeploymentResponse{
		DeploymentId: rec.ID,
		App:          req.GetApp(),
		Status:       string(rec.Status),
	}, nil
}

// apperrConflict 构造「已切流不可 cancel」的 409（与 CLI 同文案口径，
// 走 E_STATE_VERSION_CONFLICT 注册码——语义匹配，不新增码）。
func apperrConflict(rec state.DeployRecord) error {
	return apperr.New("E_STATE_VERSION_CONFLICT",
		"deployment %s 已切流或已是终态（%s），不可 cancel：曾健康请改用 rollback", rec.ID, rec.Status).
		WithContext("deployment", rec.ID).
		WithContext("reason", "already_switched")
}

// ensureApp 取应用行；不存在则创建（部署常是应用的第一个平台动作，与
// CLI 同语义：应用随首次部署自动创建）。
func ensureApp(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, state.ErrAppNotFound) {
		return state.App{}, err
	}
	created, err := st.CreateApp(ctx, "", name)
	if err != nil {
		return state.App{}, err
	}
	return created, nil
}
