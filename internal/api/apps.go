package api

import (
	"context"
	"errors"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// AppsService 实现 server.v1.AppsService（T2.17）：列表带派生状态、详情
// 带 placement 与最近部署、删除走 tombstone 生命周期（state 现有状态机）。
type AppsService struct {
	serverv1.UnimplementedAppsServiceServer
	st *state.Store
}

// NewAppsService 构造 AppsService。
func NewAppsService(st *state.Store) *AppsService { return &AppsService{st: st} }

// ListApps 列出应用（active + deleting；deleted tombstone 不进默认列表
// ——GetApp 可显式查看，列表面向运营主视图）。
func (s *AppsService) ListApps(ctx context.Context, req *serverv1.ListAppsRequest) (*serverv1.ListAppsResponse, error) {
	apps, err := s.st.ListActiveApps(ctx)
	if err != nil {
		return nil, err
	}
	if limit := int(req.GetLimit()); limit > 0 && limit < len(apps) {
		apps = apps[:limit]
	}
	out := make([]*serverv1.AppView, 0, len(apps))
	for _, app := range apps {
		derived, err := s.derivedState(ctx, app.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &serverv1.AppView{
			Id:           app.ID,
			Name:         app.Name,
			Lifecycle:    string(app.Lifecycle),
			DerivedState: derived,
			CreatedAt:    timestamppb.New(app.CreatedAt),
			UpdatedAt:    timestamppb.New(app.UpdatedAt),
		})
	}
	return &serverv1.ListAppsResponse{Apps: out}, nil
}

// GetApp 应用详情：placement / 最近部署 / 派生状态正交细节。
func (s *AppsService) GetApp(ctx context.Context, req *serverv1.GetAppRequest) (*serverv1.GetAppResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	derived, err := s.derivedState(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	resp := &serverv1.GetAppResponse{
		Id:           app.ID,
		Name:         app.Name,
		Lifecycle:    string(app.Lifecycle),
		DerivedState: derived,
		CreatedAt:    timestamppb.New(app.CreatedAt),
		UpdatedAt:    timestamppb.New(app.UpdatedAt),
	}
	if p, err := s.st.GetPlacement(ctx, app.ID); err == nil {
		resp.Placement = placementView(p)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return nil, err
	}
	rows, err := s.st.ListAppDeployments(ctx, app.ID, 5)
	if err != nil {
		return nil, err
	}
	resp.RecentDeployments = deploymentViews(rows)
	return resp, nil
}

// DeleteApp 推进 active → deleting（tombstone 第一拍）：保留期内名字仍
// 占用、不复活；与审计同事务 fail-closed。actor 记调用方 token（审计
// action = api.AppsService.DeleteApp，方法级；不发明新事件）。
func (s *AppsService) DeleteApp(ctx context.Context, req *serverv1.DeleteAppRequest) (*serverv1.DeleteAppResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetName())
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.MarkAppDeleting(ctx, app.ID); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, auditEntry(ctx, "app:"+app.ID, `{"lifecycle":"deleting"}`))
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrInvalidLifecycleTransition):
			return nil, conflict("app not deletable from current lifecycle: " + app.Name)
		default:
			return nil, err
		}
	}
	return &serverv1.DeleteAppResponse{Name: app.Name, Lifecycle: string(state.LifecycleDeleting)}, nil
}

// derivedState 读面即时派生应用状态（state-model §2.10 纯函数，与引擎/CLI
// 共用 engine.DeriveAppState 同一实现）。
func (s *AppsService) derivedState(ctx context.Context, appID string) (string, error) {
	facts, err := appFacts(ctx, s.st, appID)
	if err != nil {
		return "", err
	}
	return engine.DeriveAppState(facts), nil
}

// appFacts 读取派生输入事实（placement + 部署窗口）。
func appFacts(ctx context.Context, st *state.Store, appID string) (engine.AppFacts, error) {
	facts := engine.AppFacts{}
	if p, err := st.GetPlacement(ctx, appID); err == nil {
		facts.PlacementState = string(p.State)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return facts, err
	}
	rows, err := st.ListAppDeployments(ctx, appID, 25)
	if err != nil {
		return facts, err
	}
	if len(rows) > 0 {
		facts.Latest = rows[0]
	}
	for _, r := range rows {
		if r.Status == state.DeploySucceeded {
			facts.LatestSucceeded = r
			break
		}
	}
	return facts, nil
}

// placementView 构造 placement 投影。
func placementView(p state.Placement) *serverv1.PlacementView {
	v := &serverv1.PlacementView{
		PlatformNodeId: p.PlatformNodeID,
		LabelRef:       p.LabelRef,
		State:          string(p.State),
		Reason:         p.Reason,
		Source:         string(p.Source),
	}
	if !p.PinnedAt.IsZero() {
		v.PinnedAt = timestamppb.New(p.PinnedAt)
	}
	v.CreatedAt = timestamppb.New(p.CreatedAt)
	v.UpdatedAt = timestamppb.New(p.UpdatedAt)
	return v
}

// deploymentViews 构造部署投影列表。
func deploymentViews(rows []state.DeployRecord) []*serverv1.DeploymentView {
	out := make([]*serverv1.DeploymentView, 0, len(rows))
	for _, r := range rows {
		out = append(out, deploymentView(r))
	}
	return out
}

func deploymentView(r state.DeployRecord) *serverv1.DeploymentView {
	v := &serverv1.DeploymentView{
		Id:              r.ID,
		App:             r.AppName,
		Kind:            r.Kind,
		Status:          string(r.Status),
		Phase:           r.Phase,
		RevisionId:      r.RevisionID,
		ErrorCode:       r.ErrorCode,
		Verdict:         r.Verdict,
		Recovery:        r.Recovery,
		SubstrateHalted: r.SubstrateHalted,
		DowntimeMs:      r.DowntimeMS,
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
	}
	if !r.FirstHealthyAt.IsZero() {
		v.FirstHealthyAt = timestamppb.New(r.FirstHealthyAt)
	}
	return v
}

// auditEntryFromPrincipal 构造审计条目骨架（actor 恒 human——API 无法区分
// 人类/AI 代理，token 备注/-ID 承载可追溯性；state-model §2.9 actor 词表
// 不扩）。
func auditEntry(ctx context.Context, target, diff string) state.AuditEntry {
	actorTokenID := ""
	if p, ok := PrincipalFromContext(ctx); ok {
		actorTokenID = p.TokenID
	}
	return state.AuditEntry{
		Actor:        "human",
		ActorTokenID: actorTokenID,
		Action:       actionFromContext(ctx),
		Target:       target,
		Result:       "ok",
		DiffSummary:  diff,
	}
}

// actionFromContext 由拦截器注入的方法名推导审计 action（api.<Service>.<Method>）。
func actionFromContext(ctx context.Context) string {
	if a, ok := ctx.Value(actionKey{}).(string); ok {
		return a
	}
	return "api.unknown"
}

type actionKey struct{}
