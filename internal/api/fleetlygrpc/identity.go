package fleetlygrpc

// Identity & Access 上下文服务实现（Users/Teams/Roles/Tokens/AuditQuery，
// F0.5~F0.7；Invitations 在 invitations.go）。明文凭证只在创建响应出现
// 一次；审计行全部带 actor（authn ctx）；identity 写路径同样四件一拍
//（含 outbox 事件）。

import (
	"context"
	"database/sql"
	"encoding/json"

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
func identityAudit(ctx context.Context, action, resource, beforeFP, afterFP string) *audit.Entry {
	return &audit.Entry{
		ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		Action: action, Resource: resource, BeforeFP: beforeFP, AfterFP: afterFP,
	}
}

// emitIdentityEvent 落 outbox 事件（payload 是最小 JSON 面）。
func emitIdentityEvent(ctx context.Context, s *Services, tx *sql.Tx, name, aggregate, id string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.OutboxEvents.Append(ctx, tx, name, aggregate, id, data)
	return err
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
	u := &user.User{ID: newID(), Name: req.GetName()}
	m := &membership.Membership{ID: newID(), UserID: u.ID, TeamID: teamID, RoleID: req.GetRoleId()}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := svc.s.Teams.Get(ctx, tx, teamID); err != nil {
			return err
		}
		if _, err := svc.s.Roles.Get(ctx, tx, req.GetRoleId()); err != nil {
			return err
		}
		if err := svc.s.Users.Create(ctx, tx, u); err != nil {
			return err
		}
		if err := svc.s.Memberships.Create(ctx, tx, m); err != nil {
			return err
		}
		if err := emitIdentityEvent(ctx, svc.s, tx, "user.created", "user", u.ID, map[string]string{"name": u.Name}); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "user.create", "user/"+u.ID, "", u.Name))
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		u, err := svc.s.Users.Get(ctx, tx, req.GetId())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memberships WHERE user_id = ?`, req.GetId()); err != nil {
			return err
		}
		if err := svc.s.Users.Delete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "user.delete", "user/"+req.GetId(), u.Name, ""))
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Teams.Create(ctx, tx, t); err != nil {
			return err
		}
		if err := emitIdentityEvent(ctx, svc.s, tx, "team.created", "team", t.ID, map[string]string{"name": t.Name}); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "team.create", "team/"+t.ID, "", t.Name))
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := svc.s.Teams.Get(ctx, tx, req.GetId())
		if err != nil {
			return err
		}
		if err := svc.s.Teams.Delete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "team.delete", "team/"+req.GetId(), t.Name, ""))
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
	ro := &role.Role{ID: newID(), TeamID: teamID, Name: req.GetName(), Scopes: identity.ScopeStrings(scopes)}
	err = svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := svc.s.Teams.Get(ctx, tx, teamID); err != nil {
			return err
		}
		if err := svc.s.Roles.Create(ctx, tx, ro); err != nil {
			return err
		}
		if err := emitIdentityEvent(ctx, svc.s, tx, "role.created", "role", ro.ID, map[string][]string{"scopes": ro.Scopes}); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "role.create", "role/"+ro.ID, "", joinStrings(ro.Scopes)))
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
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		ro, err := svc.s.Roles.Get(ctx, tx, req.GetId())
		if err != nil {
			return err
		}
		if err := svc.s.Roles.Delete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "role.delete", "role/"+req.GetId(), joinStrings(ro.Scopes), ""))
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
	teamID := req.GetTeamId()
	if teamID == "" {
		teamID = identity.DefaultTeamID
	}
	material, err := identity.NewToken()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "token material generation failed")
	}
	t := &tokenrepo.Token{
		ID: newID(), Name: req.GetName(), TeamID: teamID, UserID: req.GetUserId(),
		RoleID: req.GetRoleId(), SHA256: material.SHA256, Prefix: material.Prefix,
	}
	err = svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := svc.s.Teams.Get(ctx, tx, teamID); err != nil {
			return err
		}
		if _, err := svc.s.Roles.Get(ctx, tx, req.GetRoleId()); err != nil {
			return err
		}
		if req.GetUserId() != "" {
			if _, err := svc.s.Users.Get(ctx, tx, req.GetUserId()); err != nil {
				return err
			}
		}
		if err := svc.s.Tokens.Create(ctx, tx, t); err != nil {
			return err
		}
		if err := emitIdentityEvent(ctx, svc.s, tx, "token.created", "token", t.ID, map[string]string{"name": t.Name}); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "token.create", "token/"+t.ID, "", t.Name))
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
	return &identityv1.GetTokenResponse{Token: tokenMsg(t)}, nil
}

func (svc *TokensService) ListTokens(ctx context.Context, _ *identityv1.ListTokensRequest) (*identityv1.ListTokensResponse, error) {
	list, err := svc.s.Tokens.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "token")
	}
	out := &identityv1.ListTokensResponse{}
	for i := range list {
		out.Tokens = append(out.Tokens, tokenMsg(&list[i]))
	}
	return out, nil
}

// RevokeToken 吊销（幂等；验收锚：进行中请求的下一个调用即 401——authn
// 拦截器逐请求查表）。
func (svc *TokensService) RevokeToken(ctx context.Context, req *identityv1.RevokeTokenRequest) (*identityv1.RevokeTokenResponse, error) {
	var revoked *tokenrepo.Token
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		t, err := svc.s.Tokens.Revoke(ctx, tx, req.GetId())
		if err != nil {
			return err
		}
		revoked = t
		if err := emitIdentityEvent(ctx, svc.s, tx, "token.revoked", "token", t.ID, map[string]string{"name": t.Name}); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, identityAudit(ctx, "token.revoke", "token/"+t.ID, t.Name, ""))
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
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	list, err := svc.s.Audits.ListFiltered(ctx, svc.s.DB.Runner(), audit.Filter{
		Actor: req.GetActor(), Source: req.GetSource(),
		ActionPrefix: req.GetAction(), Resource: req.GetResource(), Limit: limit,
	})
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
