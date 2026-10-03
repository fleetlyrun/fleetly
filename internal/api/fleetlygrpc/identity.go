package fleetlygrpc

// Identity & Access 上下文服务实现（Users/Teams/Roles/Tokens/AuditQuery，
// F0.5~F0.7；Invitations 在 invitations.go）。明文凭证只在创建响应出现
// 一次；审计行全部带 actor（authn ctx）；identity 写路径同样四件一拍
//（含 outbox 事件）。

import (
	"context"
	"database/sql"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/invitation"
	"github.com/fleetlyrun/fleetly/internal/state/membership"
	"github.com/fleetlyrun/fleetly/internal/state/role"
	"github.com/fleetlyrun/fleetly/internal/state/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/user"
)

// identityAudit 是 identity 写路径的审计行构造（actor + 来源从 authn ctx）。
// outbox 事件经 acceptance.go 的 identityEvent 构造（写原语事实段）。
func identityAudit(ctx context.Context, action, resource, beforeFP, afterFP string) *audit.Entry {
	return &audit.Entry{
		ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		Action: action, Resource: resource, BeforeFP: beforeFP, AfterFP: afterFP,
	}
}

// ---- Users ----

type UsersService struct {
	identityv1.UnimplementedUsersServiceServer
	s *Services
}

// WhoAmI 是 login 验证与调试入口：凭证是任意有效 Token 本身（PUBLIC 档，
// 无身份即 E_UNAUTHENTICATED——窄 scope Token 也能 login）。
func (svc *UsersService) WhoAmI(ctx context.Context, _ *identityv1.WhoAmIRequest) (*identityv1.WhoAmIResponse, error) {
	id, ok := authn.FromContext(ctx)
	if !ok {
		return nil, apperr.New("E_UNAUTHENTICATED",
			"present a valid token to ask who you are (this method itself needs no scope)")
	}
	return &identityv1.WhoAmIResponse{
		TokenId: id.TokenID, TokenName: id.TokenName,
		TeamId: id.TeamID, RoleId: id.RoleID, RoleName: id.RoleName,
		UserId: id.UserID, UserName: id.UserName,
		Scopes: id.ScopeStrings(),
	}, nil
}

func (svc *UsersService) CreateUser(ctx context.Context, req *identityv1.CreateUserRequest) (*identityv1.CreateUserResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	if req.GetRoleId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "role_id: must not be empty (grant a role within the team)")
	}
	teamID := req.GetTeamId()
	if teamID == "" {
		teamID = identity.DefaultTeamID
	}
	// Team 轴（ADR-0035 决策 5）：membership 目标 Team 缺省 = 调用方 Team；
	// 显式他队目标仅平台 owner 可。
	resolvedTeam, err := resolveTargetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	teamID = resolvedTeam
	u := &user.User{ID: newID(), Name: req.GetName()}
	m := &membership.Membership{ID: newID(), UserID: u.ID, TeamID: teamID, RoleID: req.GetRoleId()}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.teamExists(teamID), svc.s.roleInTeam(req.GetRoleId(), teamID)},
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Users.Create(ctx, tx, u); err != nil {
				return err
			}
			return svc.s.Memberships.Create(ctx, tx, m)
		},
		events: []eventFact{identityEvent("user.created", "user", u.ID, nameEventPayload{Name: u.Name})},
		audits: []*audit.Entry{identityAudit(ctx, "user.create", "user/"+u.ID, "", u.Name)},
	})
	if err != nil {
		return nil, mapStateError(err, "user")
	}
	return &identityv1.CreateUserResponse{User: userMsg(u)}, nil
}

func (svc *UsersService) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	u, err := svc.s.Users.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "user")
	}
	return &identityv1.GetUserResponse{User: userMsg(u)}, nil
}

func (svc *UsersService) ListUsers(ctx context.Context, _ *identityv1.ListUsersRequest) (*identityv1.ListUsersResponse, error) {
	list, err := svc.s.Users.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "user")
	}
	out := &identityv1.ListUsersResponse{}
	for i := range list {
		out.Users = append(out.Users, userMsg(&list[i]))
	}
	return out, nil
}

// DeleteUser 删用户（先解绑 memberships；仍有 Token 引用时 FK 拒——先吊销
// 或删名下 Token）。
func (svc *UsersService) DeleteUser(ctx context.Context, req *identityv1.DeleteUserRequest) (*identityv1.DeleteUserResponse, error) {
	// 审计的行读随写路径（事务内读-删同生共死）；裸 memberships 清理的
	// FK 归一（repo 的 DeleteByUser 带 team 过滤、语义不同）随 ADR-0028 批。
	entry := identityAudit(ctx, "user.delete", "user/"+req.GetId(), "", "")
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			u, err := svc.s.Users.Get(ctx, tx, req.GetId())
			if err != nil {
				return err
			}
			entry.BeforeFP = u.Name
			if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE user_id = ?`, req.GetId()); err != nil {
				return err
			}
			return svc.s.Users.Delete(ctx, tx, req.GetId())
		},
		audits: []*audit.Entry{entry},
	})
	if err != nil {
		return nil, mapStateError(err, "user")
	}
	return &identityv1.DeleteUserResponse{}, nil
}

// ---- Teams ----

type TeamsService struct {
	identityv1.UnimplementedTeamsServiceServer
	s *Services
}

func (svc *TeamsService) CreateTeam(ctx context.Context, req *identityv1.CreateTeamRequest) (*identityv1.CreateTeamResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	t := &team.Team{ID: newID(), Name: req.GetName()}
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Teams.Create(ctx, tx, t)
		},
		events: []eventFact{identityEvent("team.created", "team", t.ID, nameEventPayload{Name: t.Name})},
		audits: []*audit.Entry{identityAudit(ctx, "team.create", "team/"+t.ID, "", t.Name)},
	})
	if err != nil {
		return nil, mapStateError(err, "team")
	}
	return &identityv1.CreateTeamResponse{Team: teamMsg(t)}, nil
}

func (svc *TeamsService) GetTeam(ctx context.Context, req *identityv1.GetTeamRequest) (*identityv1.GetTeamResponse, error) {
	t, err := svc.s.Teams.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "team")
	}
	return &identityv1.GetTeamResponse{Team: teamMsg(t)}, nil
}

func (svc *TeamsService) ListTeams(ctx context.Context, _ *identityv1.ListTeamsRequest) (*identityv1.ListTeamsResponse, error) {
	list, err := svc.s.Teams.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "team")
	}
	out := &identityv1.ListTeamsResponse{}
	for i := range list {
		out.Teams = append(out.Teams, teamMsg(&list[i]))
	}
	return out, nil
}

func (svc *TeamsService) DeleteTeam(ctx context.Context, req *identityv1.DeleteTeamRequest) (*identityv1.DeleteTeamResponse, error) {
	entry := identityAudit(ctx, "team.delete", "team/"+req.GetId(), "", "")
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			t, err := svc.s.Teams.Get(ctx, tx, req.GetId())
			if err != nil {
				return err
			}
			entry.BeforeFP = t.Name
			return svc.s.Teams.Delete(ctx, tx, req.GetId())
		},
		audits: []*audit.Entry{entry},
	})
	if err != nil {
		return nil, mapStateError(err, "team")
	}
	return &identityv1.DeleteTeamResponse{}, nil
}

// ---- Roles ----

type RolesService struct {
	identityv1.UnimplementedRolesServiceServer
	s *Services
}

func (svc *RolesService) CreateRole(ctx context.Context, req *identityv1.CreateRoleRequest) (*identityv1.CreateRoleResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	if len(req.GetScopes()) == 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "scopes: must not be empty (a role is a named scope set)")
	}
	scopes, err := identity.ParseScopes(req.GetScopes(), svc.s.ScopeVocabulary)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "scopes: %v", err)
	}
	teamID := req.GetTeamId()
	if teamID == "" {
		teamID = identity.DefaultTeamID
	}
	// Team 轴（ADR-0035 决策 5）：自定义 Role 落在调用方 Team（他队目标仅
	// 平台 owner 可）。
	resolvedTeam, err := resolveTargetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	teamID = resolvedTeam
	ro := &role.Role{ID: newID(), TeamID: teamID, Name: req.GetName(), Scopes: identity.ScopeStrings(scopes)}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.teamExists(teamID)},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Roles.Create(ctx, tx, ro)
		},
		events: []eventFact{identityEvent("role.created", "role", ro.ID, roleEventPayload{Scopes: ro.Scopes})},
		audits: []*audit.Entry{identityAudit(ctx, "role.create", "role/"+ro.ID, "", joinStrings(ro.Scopes))},
	})
	if err != nil {
		return nil, mapStateError(err, "role")
	}
	return &identityv1.CreateRoleResponse{Role: roleMsg(ro)}, nil
}

func (svc *RolesService) GetRole(ctx context.Context, req *identityv1.GetRoleRequest) (*identityv1.GetRoleResponse, error) {
	ro, err := svc.s.Roles.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "role")
	}
	return &identityv1.GetRoleResponse{Role: roleMsg(ro)}, nil
}

func (svc *RolesService) ListRoles(ctx context.Context, _ *identityv1.ListRolesRequest) (*identityv1.ListRolesResponse, error) {
	list, err := svc.s.Roles.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "role")
	}
	out := &identityv1.ListRolesResponse{}
	for i := range list {
		out.Roles = append(out.Roles, roleMsg(&list[i]))
	}
	return out, nil
}

func (svc *RolesService) DeleteRole(ctx context.Context, req *identityv1.DeleteRoleRequest) (*identityv1.DeleteRoleResponse, error) {
	entry := identityAudit(ctx, "role.delete", "role/"+req.GetId(), "", "")
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			ro, err := svc.s.Roles.Get(ctx, tx, req.GetId())
			if err != nil {
				return err
			}
			entry.BeforeFP = joinStrings(ro.Scopes)
			return svc.s.Roles.Delete(ctx, tx, req.GetId())
		},
		audits: []*audit.Entry{entry},
	})
	if err != nil {
		return nil, mapStateError(err, "role")
	}
	return &identityv1.DeleteRoleResponse{}, nil
}

// ---- Tokens ----

type TokensService struct {
	identityv1.UnimplementedTokensServiceServer
	s *Services
}

// CreateToken 铸 Token（明文只在本响应出现一次；存储 sha256）。
func (svc *TokensService) CreateToken(ctx context.Context, req *identityv1.CreateTokenRequest) (*identityv1.CreateTokenResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	if req.GetName() == identity.BootstrapTokenName {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name %q is reserved", identity.BootstrapTokenName)
	}
	if req.GetRoleId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "role_id: must not be empty (tokens get scopes via a team role)")
	}
	// Team 轴（ADR-0035 决策 5）：铸 Token 的目标 Team 缺省 = 调用方 Team
	//（此前缺省 default——跨队铸造零校验是提权面）；显式他队仅平台 owner。
	teamID, err := resolveTargetTeam(ctx, req.GetTeamId())
	if err != nil {
		return nil, err
	}
	material, err := identity.NewToken()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "token material generation failed")
	}
	t := &tokenrepo.Token{
		ID: newID(), Name: req.GetName(), TeamID: teamID, UserID: req.GetUserId(),
		RoleID: req.GetRoleId(), SHA256: material.SHA256, Prefix: material.Prefix,
	}
	checks := []acceptanceCheck{svc.s.teamExists(teamID), svc.s.roleInTeam(req.GetRoleId(), teamID)}
	if req.GetUserId() != "" {
		checks = append(checks, svc.s.userExists(req.GetUserId()))
	}
	err = svc.s.commit(ctx, writeFact{
		checks: checks,
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Tokens.Create(ctx, tx, t)
		},
		events: []eventFact{identityEvent("token.created", "token", t.ID, nameEventPayload{Name: t.Name})},
		audits: []*audit.Entry{identityAudit(ctx, "token.create", "token/"+t.ID, "", t.Name)},
	})
	if err != nil {
		return nil, mapStateError(err, "token")
	}
	return &identityv1.CreateTokenResponse{Token: tokenMsg(t), Secret: material.Secret}, nil
}

func (svc *TokensService) GetToken(ctx context.Context, req *identityv1.GetTokenRequest) (*identityv1.GetTokenResponse, error) {
	t, err := svc.s.Tokens.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "token")
	}
	if err := svc.s.authorizeTokenRow(ctx, t); err != nil {
		return nil, err
	}
	return &identityv1.GetTokenResponse{Token: tokenMsg(t)}, nil
}

// ListTokens 非 owner 按 Team 过滤（ADR-0035：Token 名/前缀是敏感目录面）。
func (svc *TokensService) ListTokens(ctx context.Context, _ *identityv1.ListTokensRequest) (*identityv1.ListTokensResponse, error) {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.s.Tokens.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "token")
	}
	out := &identityv1.ListTokensResponse{}
	for i := range list {
		if !owner && list[i].TeamID != teamID {
			continue
		}
		out.Tokens = append(out.Tokens, tokenMsg(&list[i]))
	}
	return out, nil
}

// RevokeToken 吊销（幂等；验收锚：进行中请求的下一个调用即 401——authn
// 拦截器逐请求查表）。事件与审计的载荷字段来自事务内 Revoke 的返回行，
// 经切片元素就地填充（write 先于 events/audits 段执行）。行级授权
// （ADR-0035）：吊销他队 Token 是提权面。
func (svc *TokensService) RevokeToken(ctx context.Context, req *identityv1.RevokeTokenRequest) (*identityv1.RevokeTokenResponse, error) {
	if row, err := svc.s.Tokens.Get(ctx, svc.s.DB.Runner(), req.GetId()); err == nil {
		if aerr := svc.s.authorizeTokenRow(ctx, row); aerr != nil {
			return nil, aerr
		}
	} else {
		return nil, mapStateError(err, "token")
	}
	var revoked *tokenrepo.Token
	events := []eventFact{identityEvent("token.revoked", "token", req.GetId(), nil)}
	entry := identityAudit(ctx, "token.revoke", "token/"+req.GetId(), "", "")
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			t, err := svc.s.Tokens.Revoke(ctx, tx, req.GetId())
			if err != nil {
				return err
			}
			revoked = t
			events[0] = events[0].withPayload(map[string]string{"name": t.Name})
			entry.BeforeFP = t.Name
			return nil
		},
		events: events,
		audits: []*audit.Entry{entry},
	})
	if err != nil {
		return nil, mapStateError(err, "token")
	}
	return &identityv1.RevokeTokenResponse{Token: tokenMsg(revoked)}, nil
}

// ---- AuditQuery ----

type AuditQueryService struct {
	identityv1.UnimplementedAuditQueryServiceServer
	s *Services
}

func (svc *AuditQueryService) ListAudit(ctx context.Context, req *identityv1.ListAuditRequest) (*identityv1.ListAuditResponse, error) {
	if src := req.GetSource(); src != "" && !validAuditSource(src) {
		return nil, apperr.New("E_INVALID_ARGUMENT", "source: unknown source %q", src)
	}
	// Team 轴（ADR-0035 决策 6）：非 owner 过滤本队行 + ''平台级行（system
	// 动作与迁移前存量；内容无值载荷）。
	filter := audit.Filter{
		Actor: req.GetActor(), Source: req.GetSource(),
		ActionPrefix: req.GetAction(), Resource: req.GetResource(),
	}
	if _, owner, err := callerTeam(ctx); err != nil {
		return nil, err
	} else if !owner {
		id, _ := authn.FromContext(ctx)
		filter.Team = id.TeamID
	}
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	filter.Limit = limit
	list, err := svc.s.Audits.ListFiltered(ctx, svc.s.DB.Runner(), filter)
	if err != nil {
		return nil, mapStateError(err, "audit")
	}
	out := &identityv1.ListAuditResponse{}
	for i := range list {
		e := &list[i]
		out.Entries = append(out.Entries, &identityv1.AuditEntry{
			Id: e.ID, Actor: e.Actor, Source: string(e.Source),
			Action: e.Action, Resource: e.Resource,
			BeforeFp: e.BeforeFP, AfterFp: e.AfterFP, CreatedAt: e.CreatedAt,
		})
	}
	return out, nil
}

func validAuditSource(s string) bool {
	switch s {
	case string(audit.SourceManual), string(audit.SourceAPI), string(audit.SourceCLI),
		string(audit.SourceWebhook), string(audit.SourceSchedule), string(audit.SourceSystem):
		return true
	}
	return false
}

// ---- 消息映射 ----

func userMsg(u *user.User) *identityv1.User {
	return &identityv1.User{Id: u.ID, Name: u.Name, CreatedAt: u.CreatedAt}
}

func teamMsg(t *team.Team) *identityv1.Team {
	return &identityv1.Team{Id: t.ID, Name: t.Name, CreatedAt: t.CreatedAt}
}

func roleMsg(ro *role.Role) *identityv1.Role {
	return &identityv1.Role{
		Id: ro.ID, TeamId: ro.TeamID, Name: ro.Name, Builtin: ro.Builtin,
		Scopes: ro.Scopes, CreatedAt: ro.CreatedAt,
	}
}

func tokenMsg(t *tokenrepo.Token) *identityv1.Token {
	return &identityv1.Token{
		Id: t.ID, Name: t.Name, TeamId: t.TeamID, UserId: t.UserID, RoleId: t.RoleID,
		Prefix: t.Prefix, Revoked: t.Revoked, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt,
	}
}

func invitationMsg(inv *invitation.Invitation) *identityv1.Invitation {
	return &identityv1.Invitation{
		Id: inv.ID, TeamId: inv.TeamID, RoleId: inv.RoleID, CreatedBy: inv.CreatedBy,
		ExpiresAt: inv.ExpiresAt, ConsumedAt: inv.ConsumedAt, CreatedAt: inv.CreatedAt,
	}
}

func joinStrings(parts []string) string {
	out := ""
	for i, s := range parts {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
