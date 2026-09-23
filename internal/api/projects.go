package api

import (
	"context"
	"errors"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
)

// ProjectsService 实现 server.v1.ProjectsService（v0.3 W2-S1，rbac-teams
// 设计 §3.3/§3.4/§5）：项目与队内覆写成员面。
//
// 项目面权限（本票自含，W2-S4 通用角色门之前的实现切面）：
//   - 全部方法要求用户凭据（requireTeamUser——机具令牌恒 403）；
//   - 读面：归属团队成员（可见性零级联——可见项目集 = 团队归属，D-W0-2）；
//     平台管理员只读放行；
//   - 项目生命周期写面（创建/改名/删除）= 团队 owner（§3.2 矩阵「项目
//     创建/删除 → owner」；改名未入矩阵，保守取 owner——票面偏差记录）；
//   - 队内覆写成员面 = 团队 admin/owner（§3.3 明文）；覆写角色不授予成员
//     管理权（权限怪圈防线——项目内被覆写为 admin 者仍按团队角色判定）。
//
// MoveApp / MoveDatabase 不在本票（改派 = 换名重部署，依赖 W2-S3 命名
// 三段化——设计 §3.4/§5）。
//
// 审计与事件：项目原语的审计（project.created/deleted/member_role_changed/
// member_removed）与事件（project.created/deleted/member_changed）由 state
// 层随业务写同事务落（Outbox），本服务不重复落 api.* 审计。
type ProjectsService struct {
	serverv1.UnimplementedProjectsServiceServer
	st *state.Store
}

// NewProjectsService 构造 ProjectsService。
func NewProjectsService(st *state.Store) *ProjectsService {
	return &ProjectsService{st: st}
}

// requireProjectOverrideManager 是队内覆写成员面门（§3.3 明文：团队
// admin/owner 经 ProjectsService 成员面增删改；平台管理员不代写）。
func requireProjectOverrideManager(ctx context.Context, st *state.Store, teamID string) (Principal, error) {
	p, err := requireTeamUser(ctx)
	if err != nil {
		return Principal{}, err
	}
	m, err := st.GetMembership(ctx, teamID, p.UserID)
	if err != nil {
		if errors.Is(err, state.ErrTeamMemberNotFound) {
			return Principal{}, statusEnvelope(codes.PermissionDenied, "team admin or owner role required")
		}
		return Principal{}, err
	}
	if m.Role != state.TeamRoleOwner && m.Role != state.TeamRoleAdmin {
		return Principal{}, statusEnvelope(codes.PermissionDenied, "team admin or owner role required")
	}
	return p, nil
}

// mapProjectErr 是 state 项目哨兵 → api 语义的唯一映射点。
func mapProjectErr(err error) error {
	switch {
	case errors.Is(err, state.ErrProjectNotFound):
		return notFound("project not found")
	case errors.Is(err, state.ErrProjectSlugTaken):
		return conflict("project slug already taken in team")
	case errors.Is(err, state.ErrProjectNotEmpty):
		return conflict("project still has live resources (apps/databases); move or delete them first")
	case errors.Is(err, state.ErrProjectMemberNotFound):
		return notFound("project member override not found")
	case errors.Is(err, state.ErrProjectOwnerOverride):
		return conflict("team owners cannot have a project role override (owners hold owner rights in every project)")
	case errors.Is(err, state.ErrNotTeamMember):
		return conflict("target user is not a member of the owning team (project overrides are limited to team members)")
	default:
		return err
	}
}

// getProjectForRead 取项目行并过读面门（归属团队成员或平台管理员）。
func (s *ProjectsService) getProjectForRead(ctx context.Context, id string) (state.Project, error) {
	if _, err := requireTeamUser(ctx); err != nil {
		return state.Project{}, err
	}
	p, err := s.st.GetProject(ctx, id)
	if err != nil {
		return state.Project{}, mapProjectErr(err)
	}
	if err := requireTeamReadAccess(ctx, s.st, p.TeamID); err != nil {
		return state.Project{}, err
	}
	return p, nil
}

// ── ProjectsService RPC ──────────────────────────────────────────────────────

// CreateProject 在团队下建项目（owner 专属，§3.2 矩阵「项目创建/删除」行）。
func (s *ProjectsService) CreateProject(ctx context.Context, req *serverv1.CreateProjectRequest) (*serverv1.CreateProjectResponse, error) {
	p, err := requireTeamOwner(ctx, s.st, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	if err := validateProjectSlug(req.GetSlug()); err != nil {
		return nil, err
	}
	proj, err := s.st.CreateProject(ctx, state.ProjectWrite{
		TeamID:       req.GetTeamId(),
		Slug:         req.GetSlug(),
		Name:         req.GetName(),
		Description:  req.GetDescription(),
		ActorUserID:  p.UserID,
		ActorTokenID: callerTokenID(ctx),
	})
	if err != nil {
		return nil, mapProjectErr(err)
	}
	view, err := s.projectView(ctx, proj)
	if err != nil {
		return nil, err
	}
	return &serverv1.CreateProjectResponse{Project: view}, nil
}

// ListProjects 我可见项目（团队归属集；可见性零级联）；带 team_id 收窄到
// 单队（须为该队成员或平台管理员）；平台管理员不带过滤 = 全部。
func (s *ProjectsService) ListProjects(ctx context.Context, req *serverv1.ListProjectsRequest) (*serverv1.ListProjectsResponse, error) {
	p, err := requireTeamUser(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	if req.GetTeamId() != "" {
		if err := requireTeamReadAccess(ctx, s.st, req.GetTeamId()); err != nil {
			return nil, err
		}
		allowed[req.GetTeamId()] = true
	} else if !isPlatformAdminUser(ctx, s.st) {
		memberships, err := s.st.ListUserMemberships(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		for _, m := range memberships {
			allowed[m.TeamID] = true
		}
	}
	projects, err := s.st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.ProjectView, 0, len(projects))
	for _, proj := range projects {
		if req.GetTeamId() != "" || isPlatformAdminUser(ctx, s.st) || allowed[proj.TeamID] {
			view, err := s.projectView(ctx, proj)
			if err != nil {
				return nil, err
			}
			out = append(out, view)
		}
	}
	return &serverv1.ListProjectsResponse{Projects: out}, nil
}

// GetProject 项目投影（读面门）。
func (s *ProjectsService) GetProject(ctx context.Context, req *serverv1.GetProjectRequest) (*serverv1.GetProjectResponse, error) {
	proj, err := s.getProjectForRead(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	view, err := s.projectView(ctx, proj)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetProjectResponse{Project: view}, nil
}

// UpdateProject 仅改 name/description（slug 不可变——请求无 slug 字段）。
func (s *ProjectsService) UpdateProject(ctx context.Context, req *serverv1.UpdateProjectRequest) (*serverv1.UpdateProjectResponse, error) {
	proj, err := s.getProjectForRead(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if _, err := requireTeamOwner(ctx, s.st, proj.TeamID); err != nil {
		return nil, err
	}
	updated, err := s.st.UpdateProject(ctx, state.ProjectUpdate{
		ID:          req.GetId(),
		Name:        req.GetName(),
		Description: req.GetDescription(),
		ActorUserID: principalOf(ctx).UserID,
	})
	if err != nil {
		return nil, mapProjectErr(err)
	}
	view, err := s.projectView(ctx, updated)
	if err != nil {
		return nil, err
	}
	return &serverv1.UpdateProjectResponse{Project: view}, nil
}

// DeleteProject 删除空项目（state 非空守卫：apps 非 tombstone /
// db_instances 非 deleted 终态阻塞——资源先迁走或删光，不做隐式级联）。
func (s *ProjectsService) DeleteProject(ctx context.Context, req *serverv1.DeleteProjectRequest) (*serverv1.DeleteProjectResponse, error) {
	proj, err := s.getProjectForRead(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	p, err := requireTeamOwner(ctx, s.st, proj.TeamID)
	if err != nil {
		return nil, err
	}
	if err := s.st.DeleteProject(ctx, req.GetId(), p.UserID, callerTokenID(ctx)); err != nil {
		return nil, mapProjectErr(err)
	}
	return &serverv1.DeleteProjectResponse{}, nil
}

// ListProjectMembers 覆写行列表（读面门；无覆写行的成员不在列表——其权限
// 走团队角色）。
func (s *ProjectsService) ListProjectMembers(ctx context.Context, req *serverv1.ListProjectMembersRequest) (*serverv1.ListProjectMembersResponse, error) {
	proj, err := s.getProjectForRead(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	members, err := s.st.ListProjectMembers(ctx, proj.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.ProjectMemberView, 0, len(members))
	for _, m := range members {
		u, _ := s.st.GetUser(ctx, m.UserID) // 属主行缺失按空投影（不阻塞列表）
		out = append(out, projectMemberView(m, u))
	}
	return &serverv1.ListProjectMembersResponse{Members: out}, nil
}

// SetProjectMemberRole 队内覆写 upsert（团队 admin/owner；目标须为团队
// 成员；owner 不可覆写——守卫全在 state 原语同事务）。
func (s *ProjectsService) SetProjectMemberRole(ctx context.Context, req *serverv1.SetProjectMemberRoleRequest) (*serverv1.SetProjectMemberRoleResponse, error) {
	if !validProjectRoleValue(req.GetRole()) {
		return nil, statusInvalidArgument("project role must be one of: admin, developer, viewer (owners cannot be overridden)")
	}
	proj, err := s.getProjectForRead(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	p, err := requireProjectOverrideManager(ctx, s.st, proj.TeamID)
	if err != nil {
		return nil, err
	}
	m, err := s.st.SetProjectMemberRole(ctx, proj.ID, req.GetUserId(), req.GetRole(), p.UserID, callerTokenID(ctx))
	if err != nil {
		return nil, mapProjectErr(err)
	}
	u, _ := s.st.GetUser(ctx, m.UserID) // 属主行缺失按空投影（不阻塞响应）
	return &serverv1.SetProjectMemberRoleResponse{Member: projectMemberView(m, u)}, nil
}

// RemoveProjectMember 删除覆写行（该成员回退用团队角色）。
func (s *ProjectsService) RemoveProjectMember(ctx context.Context, req *serverv1.RemoveProjectMemberRequest) (*serverv1.RemoveProjectMemberResponse, error) {
	proj, err := s.getProjectForRead(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	p, err := requireProjectOverrideManager(ctx, s.st, proj.TeamID)
	if err != nil {
		return nil, err
	}
	if err := s.st.RemoveProjectMember(ctx, proj.ID, req.GetUserId(), p.UserID, callerTokenID(ctx)); err != nil {
		return nil, mapProjectErr(err)
	}
	return &serverv1.RemoveProjectMemberResponse{}, nil
}

// ── 投影 helpers ─────────────────────────────────────────────────────────────

// projectView 是 state.Project → proto 投影（team_slug 补全——限定形
// team/project 展示面，D-W0-9；团队行缺失按空串投影，不阻塞列表）。
func (s *ProjectsService) projectView(ctx context.Context, p state.Project) (*serverv1.ProjectView, error) {
	teamSlug := ""
	if t, err := s.st.GetTeam(ctx, p.TeamID); err == nil {
		teamSlug = t.Slug
	} else if !errors.Is(err, state.ErrTeamNotFound) {
		return nil, err
	}
	return &serverv1.ProjectView{
		Id:          p.ID,
		TeamId:      p.TeamID,
		TeamSlug:    teamSlug,
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		CreatedAt:   tstamp(p.CreatedAt),
	}, nil
}

// projectMemberView 是 state.ProjectMember → proto 投影（属主投影随行补全；
// 属主行缺失按空投影——不阻塞列表）。
func projectMemberView(m state.ProjectMember, u state.User) *serverv1.ProjectMemberView {
	return &serverv1.ProjectMemberView{
		ProjectId:   m.ProjectID,
		UserId:      m.UserID,
		Role:        m.Role,
		CreatedAt:   tstamp(m.CreatedAt),
		Email:       u.Email,
		DisplayName: u.DisplayName,
	}
}
