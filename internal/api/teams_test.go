package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 团队/项目面单测（v0.3 W2-S1，rbac-teams 设计 §3.1/§3.2/§3.3/§5 验收）：
//   - 创建/邀请/接受全链（第二用户经 AcceptInvite 入队拿角色）+ 邀请一次性/
//     吊销（E_INVITE_INVALID 稳定码）；
//   - owner 守卫（最后一名 owner 不可移除/降级 → E_TEAM_LAST_OWNER）；
//   - 覆写四断言（developer 降 viewer / viewer 升 developer / owner 不可
//     覆写 / 非成员拒）+ 覆写角色不授予成员管理权；
//   - 机具令牌团队面 403；平台管理员只读放行 + 写面 403；
//   - slug 纪律（保留字/词表/冲突；不可变为请求结构保证——UpdateTeam 无
//     slug 字段，改名后 slug 不动）；
//   - DeleteTeam 两段式 confirm + 项目须空；DeleteProject 非空守卫；
//   - ListTeams/ListProjects 可见性（成员=我所在；平台管理员=全部）；
//   - scope 登记完整性（fail-closed 兜底面）。

// teamEnv 是团队/项目面测试环境（独立 store；用户经 state.RegisterUser
// 播种——首用户=平台管理员，其后须先开注册窗口；调用凭据 = 用户 PAT）。
type teamEnv struct {
	st   *state.Store
	conn *grpc.ClientConn
}

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env := &teamEnv{st: st}
	srv := newAuthServer(NewAuthenticator(st))
	serverv1.RegisterAuthServiceServer(srv, NewAuthService(st))
	serverv1.RegisterTeamsServiceServer(srv, NewTeamsService(st))
	serverv1.RegisterProjectsServiceServer(srv, NewProjectsService(st))
	env.conn = serveBufconn(t, srv)
	return env
}

// seedUser 播种用户（首用户=平台管理员；其后自动开窗注册），返回注册落位。
func (e *teamEnv) seedUser(t *testing.T, email string) state.RegisterResult {
	t.Helper()
	rr, err := e.st.RegisterUser(context.Background(), state.RegisterWrite{Email: email, Password: "pw-teams-123"})
	if err == nil {
		return rr
	}
	if !errors.Is(err, state.ErrRegistrationClosed) {
		t.Fatalf("RegisterUser(%s): %v", email, err)
	}
	// 窗口关闭：开窗重试（首个用户之后的常态路径）。
	if err := e.st.SaveRegistration(context.Background(), state.AuthRegistrationOpen, state.AuthSaveOptions{Actor: "test"}); err != nil {
		t.Fatalf("SaveRegistration open: %v", err)
	}
	rr, err = e.st.RegisterUser(context.Background(), state.RegisterWrite{Email: email, Password: "pw-teams-123"})
	if err != nil {
		t.Fatalf("RegisterUser(%s) after opening: %v", email, err)
	}
	return rr
}

// userToken 为用户签发一枚 read scope 用户 PAT（团队面 scope 登记的最小形）。
func (e *teamEnv) userToken(t *testing.T, userID string) string {
	t.Helper()
	plaintext, err := generateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if _, err := e.st.CreateToken(context.Background(), state.TokenWrite{
		Hash:   state.HashToken(plaintext),
		Name:   "teams test pat",
		Scopes: ScopeRead,
		UserID: userID,
		Actor:  "human",
	}); err != nil {
		t.Fatalf("create user token: %v", err)
	}
	return plaintext
}

// machineToken 落一枚机具令牌（user NULL——团队面 403 断言面）。
func (e *teamEnv) machineToken(t *testing.T, scopes string) string {
	t.Helper()
	return seedTokenPlain(t, e.st, scopes)
}

// codeOf 从 gRPC error 解出稳定码（apperr 信封 detail；无信封返回 ""）。
func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	e, ok := apperr.FromError(err)
	if !ok {
		return ""
	}
	return e.Code()
}

// wantCode 断言 err 携带指定稳定码。
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %s, got nil", code)
	}
	if got := codeOf(t, err); got != code {
		t.Fatalf("err code = %q, want %s (err=%v)", got, code, err)
	}
}

// setupTeam 播种 owner 用户 + 团队（owner 落位），返回（owner 投影, token, 团队）。
// 注意：owner email 由 slug 派生——同 env 内 slug 必须互异。
func (e *teamEnv) setupTeam(t *testing.T, slug string) (state.RegisterResult, string, *serverv1.TeamView) {
	t.Helper()
	owner := e.seedUser(t, slug+"-owner@example.com")
	tok := e.userToken(t, owner.User.ID)
	res, err := serverv1.NewTeamsServiceClient(e.conn).CreateTeam(authCtx(context.Background(), tok),
		&serverv1.CreateTeamRequest{Slug: slug, Name: "Team " + slug})
	if err != nil {
		t.Fatalf("CreateTeam(%s): %v", slug, err)
	}
	return owner, tok, res.GetTeam()
}

// inviteAndAccept 全链：owner 邀 email → 新用户播种 → AcceptInvite 拿角色。
// 返回（新用户注册落位, 新用户 token）。
func (e *teamEnv) inviteAndAccept(t *testing.T, ownerTok, teamID, email, role string) (state.RegisterResult, string) {
	t.Helper()
	inv, err := serverv1.NewTeamsServiceClient(e.conn).CreateInvite(authCtx(context.Background(), ownerTok),
		&serverv1.CreateInviteRequest{TeamId: teamID, Email: email, Role: role})
	if err != nil {
		t.Fatalf("CreateInvite(%s): %v", email, err)
	}
	if inv.GetToken() == "" {
		t.Fatal("CreateInvite must return the one-time plaintext token")
	}
	invitee := e.seedUser(t, email)
	inviteeTok := e.userToken(t, invitee.User.ID)
	acc, err := serverv1.NewAuthServiceClient(e.conn).AcceptInvite(authCtx(context.Background(), inviteeTok),
		&serverv1.AcceptInviteRequest{Token: inv.GetToken()})
	if err != nil {
		t.Fatalf("AcceptInvite(%s): %v", email, err)
	}
	if acc.GetRole() != role || acc.GetTeamId() != teamID {
		t.Fatalf("AcceptInvite = %+v, want team %s role %s", acc, teamID, role)
	}
	return invitee, inviteeTok
}

// TestTeamsInviteChainAndGuards：创建/邀请/接受全链 + 一次性/吊销 + owner
// 守卫 + 成员管理 owner 专属 + 邀请角色上限。
func TestTeamsInviteChainAndGuards(t *testing.T) {
	env := newTeamEnv(t)
	teams := serverv1.NewTeamsServiceClient(env.conn)
	auth := serverv1.NewAuthServiceClient(env.conn)
	ctx := context.Background()

	owner, ownerTok, team := env.setupTeam(t, "acmecorp")

	// 全链：第二用户经 AcceptInvite 入队拿受邀角色。
	bob, bobTok := env.inviteAndAccept(t, ownerTok, team.GetId(), "bob@example.com", "developer")
	members, err := teams.ListTeamMembers(authCtx(ctx, ownerTok), &serverv1.ListTeamMembersRequest{TeamId: team.GetId()})
	if err != nil || len(members.GetMembers()) != 2 {
		t.Fatalf("ListTeamMembers: %v (n=%d)", err, len(members.GetMembers()))
	}
	if members.GetMembers()[1].GetRole() != "developer" || members.GetMembers()[1].GetEmail() != "bob@example.com" {
		t.Fatalf("bob membership = %+v, want developer/bob@example.com", members.GetMembers()[1])
	}

	// developer 不能邀（邀请面 = owner/admin）、不能改角色/移除成员
	//（成员管理 = owner，§3.2 矩阵直读）。
	bobCtx := authCtx(ctx, bobTok)
	if _, err := teams.CreateInvite(bobCtx, &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "x@y.com", Role: "viewer"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("developer CreateInvite err = %v, want PermissionDenied", err)
	}
	if _, err := teams.SetTeamMemberRole(bobCtx, &serverv1.SetTeamMemberRoleRequest{TeamId: team.GetId(), UserId: bob.User.ID, Role: "admin"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("developer SetTeamMemberRole err = %v, want PermissionDenied", err)
	}
	if _, err := teams.RemoveTeamMember(bobCtx, &serverv1.RemoveTeamMemberRequest{TeamId: team.GetId(), UserId: bob.User.ID}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("developer RemoveTeamMember err = %v, want PermissionDenied", err)
	}

	// owner 提升 bob → admin 后：admin 可邀 developer/admin（角色≤自身），
	// 邀 owner 角色 → 403（owner 角色仅 owner 可邀）。
	if _, err := teams.SetTeamMemberRole(authCtx(ctx, ownerTok), &serverv1.SetTeamMemberRoleRequest{
		TeamId: team.GetId(), UserId: bob.User.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("promote bob to admin: %v", err)
	}
	if _, err := teams.CreateInvite(bobCtx, &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "dev2@example.com", Role: "developer"}); err != nil {
		t.Fatalf("admin invite developer: %v", err)
	}
	if _, err := teams.CreateInvite(bobCtx, &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "adm2@example.com", Role: "admin"}); err != nil {
		t.Fatalf("admin invite admin (role ≤ self): %v", err)
	}
	_, err = teams.CreateInvite(bobCtx, &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "boss@example.com", Role: "owner"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("admin invite owner err = %v, want PermissionDenied", err)
	}

	// 邀请一次性 + 吊销：charlie 的邀请吊销后消费 → E_INVITE_INVALID；
	// 已消费 token 复用 / 未知 token 同码。
	inv2, err := teams.CreateInvite(authCtx(ctx, ownerTok), &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "charlie@example.com", Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite charlie: %v", err)
	}
	charlie := env.seedUser(t, "charlie@example.com")
	charlieTok := env.userToken(t, charlie.User.ID)
	if _, err := teams.RevokeInvite(authCtx(ctx, ownerTok), &serverv1.RevokeInviteRequest{TeamId: team.GetId(), InviteId: inv2.GetInvite().GetId()}); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	_, err = auth.AcceptInvite(authCtx(ctx, charlieTok), &serverv1.AcceptInviteRequest{Token: inv2.GetToken()})
	wantCode(t, err, "E_INVITE_INVALID")

	inv3, err := teams.CreateInvite(authCtx(ctx, ownerTok), &serverv1.CreateInviteRequest{TeamId: team.GetId(), Email: "dave@example.com", Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite dave: %v", err)
	}
	dave := env.seedUser(t, "dave@example.com")
	daveTok := env.userToken(t, dave.User.ID)
	if _, err := auth.AcceptInvite(authCtx(ctx, daveTok), &serverv1.AcceptInviteRequest{Token: inv3.GetToken()}); err != nil {
		t.Fatalf("AcceptInvite dave: %v", err)
	}
	_, err = auth.AcceptInvite(authCtx(ctx, daveTok), &serverv1.AcceptInviteRequest{Token: inv3.GetToken()})
	wantCode(t, err, "E_INVITE_INVALID")

	_, err = auth.AcceptInvite(authCtx(ctx, daveTok), &serverv1.AcceptInviteRequest{Token: "bogus-token"})
	wantCode(t, err, "E_INVITE_INVALID")

	// 吊销面归属校验：他队邀请 ID 吊销 → 404。
	_, otherTok, otherTeam := env.setupTeam(t, "other")
	otherInv, err := teams.CreateInvite(authCtx(ctx, otherTok), &serverv1.CreateInviteRequest{TeamId: otherTeam.GetId(), Email: "z@z.com", Role: "viewer"})
	if err != nil {
		t.Fatalf("CreateInvite other team: %v", err)
	}
	_, err = teams.RevokeInvite(authCtx(ctx, ownerTok), &serverv1.RevokeInviteRequest{TeamId: team.GetId(), InviteId: otherInv.GetInvite().GetId()})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("cross-team revoke err = %v, want NotFound", err)
	}

	// 最后一名 owner 守卫：owner 自移/自降 → E_TEAM_LAST_OWNER；存在第二名
	// owner（eve 受邀 owner）后自移放行，新末位 owner 的自降再被守卫。
	_, err = teams.RemoveTeamMember(authCtx(ctx, ownerTok), &serverv1.RemoveTeamMemberRequest{TeamId: team.GetId(), UserId: owner.User.ID})
	wantCode(t, err, "E_TEAM_LAST_OWNER")
	_, err = teams.SetTeamMemberRole(authCtx(ctx, ownerTok), &serverv1.SetTeamMemberRoleRequest{TeamId: team.GetId(), UserId: owner.User.ID, Role: "admin"})
	wantCode(t, err, "E_TEAM_LAST_OWNER")

	eve, eveTok := env.inviteAndAccept(t, ownerTok, team.GetId(), "eve@example.com", "owner")
	if _, err := teams.RemoveTeamMember(authCtx(ctx, ownerTok), &serverv1.RemoveTeamMemberRequest{TeamId: team.GetId(), UserId: owner.User.ID}); err != nil {
		t.Fatalf("owner removal with a second owner must pass: %v", err)
	}
	_, err = teams.SetTeamMemberRole(authCtx(ctx, eveTok), &serverv1.SetTeamMemberRoleRequest{TeamId: team.GetId(), UserId: eve.User.ID, Role: "viewer"})
	wantCode(t, err, "E_TEAM_LAST_OWNER")
}

// TestTeamsSlugDeleteAndPlatformFaces：slug 纪律（保留字/词表/冲突/不可变）+
// DeleteTeam 两段式 + 项目须空 + 平台管理员只读 + 机具令牌 403 + 可见性。
// 播种顺序钉死：root 必须是首用户（=平台管理员）。
func TestTeamsSlugDeleteAndPlatformFaces(t *testing.T) {
	env := newTeamEnv(t)
	teams := serverv1.NewTeamsServiceClient(env.conn)
	projects := serverv1.NewProjectsServiceClient(env.conn)
	ctx := context.Background()

	// 首用户 = 平台管理员（有自己的队 rootco）。
	root := env.seedUser(t, "root@example.com")
	rootTok := env.userToken(t, root.User.ID)
	rootTeams, err := teams.CreateTeam(authCtx(ctx, rootTok), &serverv1.CreateTeamRequest{Slug: "rootco", Name: "Root Co"})
	if err != nil {
		t.Fatalf("root CreateTeam: %v", err)
	}

	// 普通用户 owner 建队 acmecorp（slug 纪律断言面）。
	owner := env.seedUser(t, "owner@example.com")
	ownerTok := env.userToken(t, owner.User.ID)
	teamRes, err := teams.CreateTeam(authCtx(ctx, ownerTok), &serverv1.CreateTeamRequest{Slug: "acmecorp", Name: "Acme"})
	if err != nil {
		t.Fatalf("owner CreateTeam: %v", err)
	}
	team := teamRes.GetTeam()

	// 保留字 slug → E_TEAM_SLUG_RESERVED（422→InvalidArgument 传输码）；
	// 词表违约 → 400；slug 冲突 → 409。
	_, err = teams.CreateTeam(authCtx(ctx, ownerTok), &serverv1.CreateTeamRequest{Slug: "db", Name: "reserved"})
	wantCode(t, err, "E_TEAM_SLUG_RESERVED")
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("reserved slug grpc code = %v, want InvalidArgument(422)", status.Code(err))
	}
	if _, err := teams.CreateTeam(authCtx(ctx, ownerTok), &serverv1.CreateTeamRequest{Slug: "Bad-Slug", Name: "shape"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad slug shape err = %v, want InvalidArgument", err)
	}
	if _, err := teams.CreateTeam(authCtx(ctx, ownerTok), &serverv1.CreateTeamRequest{Slug: "acmecorp", Name: "dup"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("dup slug err = %v, want FailedPrecondition(409)", err)
	}

	// 改名成功且 slug 不动（不可变 = UpdateTeam 请求结构无 slug 字段——
	// 客户端无法表达「改 slug」，服务端回读验证 slug 原值）。
	up, err := teams.UpdateTeam(authCtx(ctx, ownerTok), &serverv1.UpdateTeamRequest{Id: team.GetId(), Name: "Acme Renamed"})
	if err != nil || up.GetTeam().GetName() != "Acme Renamed" || up.GetTeam().GetSlug() != "acmecorp" {
		t.Fatalf("UpdateTeam: %v (%+v)", err, up.GetTeam())
	}

	// DeleteTeam 两段式：confirm 不匹配 → 400；confirm = slug 但项目非空
	// → 409；项目删空后放行。
	if _, err := teams.DeleteTeam(authCtx(ctx, ownerTok), &serverv1.DeleteTeamRequest{Id: team.GetId(), Confirm: "wrong"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("DeleteTeam bad confirm err = %v, want InvalidArgument", err)
	}
	proj, err := projects.CreateProject(authCtx(ctx, ownerTok), &serverv1.CreateProjectRequest{
		TeamId: team.GetId(), Slug: "default", Name: "Default",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := teams.DeleteTeam(authCtx(ctx, ownerTok), &serverv1.DeleteTeamRequest{Id: team.GetId(), Confirm: "acmecorp"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("DeleteTeam with project err = %v, want FailedPrecondition(409)", err)
	}
	if _, err := projects.DeleteProject(authCtx(ctx, ownerTok), &serverv1.DeleteProjectRequest{Id: proj.GetProject().GetId()}); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := teams.DeleteTeam(authCtx(ctx, ownerTok), &serverv1.DeleteTeamRequest{Id: team.GetId(), Confirm: "acmecorp"}); err != nil {
		t.Fatalf("DeleteTeam after project purge: %v", err)
	}
	// 删除后回读：403（fail-closed——删除后调用方已非成员，对不存在与
	// 无权限不做存在性区分）。
	if _, err := teams.GetTeam(authCtx(ctx, ownerTok), &serverv1.GetTeamRequest{Id: team.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("GetTeam after delete err = %v, want PermissionDenied (fail-closed)", err)
	}

	// 第二支普通用户团队（可见性断言面）。
	peer, peerTok, peerTeam := env.setupTeam(t, "peer")

	// ListTeams 可见性：成员 = 我所在（注册个人队 + peer 队）；平台管理员
	// = 全部。
	list, err := teams.ListTeams(authCtx(ctx, rootTok), &serverv1.ListTeamsRequest{})
	if err != nil || len(list.GetTeams()) < 4 {
		t.Fatalf("platform admin ListTeams = %d teams (%v), want ≥4 (all: two personal + two created)", len(list.GetTeams()), err)
	}
	peerList, err := teams.ListTeams(authCtx(ctx, peerTok), &serverv1.ListTeamsRequest{})
	if err != nil || len(peerList.GetTeams()) != 2 {
		t.Fatalf("member ListTeams = %d teams (%v), want 2 (personal + peer)", len(peerList.GetTeams()), err)
	}
	peerSeen := map[string]bool{}
	for _, tt := range peerList.GetTeams() {
		peerSeen[tt.GetId()] = true
	}
	if !peerSeen[peerTeam.GetId()] || !peerSeen[peer.Team.ID] || peerSeen[rootTeams.GetTeam().GetId()] {
		t.Fatalf("member visibility broken: %+v", peerList.GetTeams())
	}

	// 平台管理员读面放行（他队 Get/ListMembers）；写面 403（不代写）。
	if _, err := teams.GetTeam(authCtx(ctx, rootTok), &serverv1.GetTeamRequest{Id: peerTeam.GetId()}); err != nil {
		t.Fatalf("platform admin GetTeam: %v", err)
	}
	if _, err := teams.ListTeamMembers(authCtx(ctx, rootTok), &serverv1.ListTeamMembersRequest{TeamId: peerTeam.GetId()}); err != nil {
		t.Fatalf("platform admin ListTeamMembers: %v", err)
	}
	if _, err := teams.UpdateTeam(authCtx(ctx, rootTok), &serverv1.UpdateTeamRequest{Id: peerTeam.GetId(), Name: "hijack"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("platform admin UpdateTeam err = %v, want PermissionDenied", err)
	}
	if _, err := teams.DeleteTeam(authCtx(ctx, rootTok), &serverv1.DeleteTeamRequest{Id: peerTeam.GetId(), Confirm: "peer"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("platform admin DeleteTeam err = %v, want PermissionDenied", err)
	}
	if _, err := teams.CreateInvite(authCtx(ctx, rootTok), &serverv1.CreateInviteRequest{TeamId: peerTeam.GetId(), Email: "x@y.com", Role: "viewer"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("platform admin CreateInvite err = %v, want PermissionDenied", err)
	}
	// 平台管理员以普通用户身份建自己的队 = 放行（写面 403 只针对代他人
	// 团队做写；CreateTeam 是自助面）。
	if _, err := teams.CreateTeam(authCtx(ctx, rootTok), &serverv1.CreateTeamRequest{Slug: "rootteam", Name: "Root Team 2"}); err != nil {
		t.Fatalf("platform admin CreateTeam on own behalf: %v", err)
	}
	_ = rootTeams

	// 机具令牌：团队面恒 403（读面与写面——无用户即无成员身份）。
	for _, scopes := range []string{"admin", "read"} {
		machine := authCtx(ctx, env.machineToken(t, scopes))
		if _, err := teams.ListTeams(machine, &serverv1.ListTeamsRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("machine(%s) ListTeams err = %v, want PermissionDenied", scopes, err)
		}
		if _, err := teams.GetTeam(machine, &serverv1.GetTeamRequest{Id: peerTeam.GetId()}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("machine(%s) GetTeam err = %v, want PermissionDenied", scopes, err)
		}
		if _, err := teams.CreateTeam(machine, &serverv1.CreateTeamRequest{Slug: "mach", Name: "M"}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("machine(%s) CreateTeam err = %v, want PermissionDenied", scopes, err)
		}
	}

	// 非成员读他队 → 403。
	if _, err := teams.GetTeam(authCtx(ctx, peerTok), &serverv1.GetTeamRequest{Id: rootTeams.GetTeam().GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member GetTeam err = %v, want PermissionDenied", err)
	}
}

// TestProjectsOverrideMatrix：覆写四断言（developer 降 viewer / viewer 升
// developer / owner 不可覆写 / 非成员拒）+ 覆写角色不授予成员管理权 +
// DeleteProject 非空守卫 + ListProjects 可见性。播种顺序：root 首用户
//（=平台管理员）。
func TestProjectsOverrideMatrix(t *testing.T) {
	env := newTeamEnv(t)
	teams := serverv1.NewTeamsServiceClient(env.conn)
	projects := serverv1.NewProjectsServiceClient(env.conn)
	ctx := context.Background()

	// 首用户 = 平台管理员（不加入 acmecorp 队——只读全域的断言面）。
	root := env.seedUser(t, "root@example.com")
	rootTok := env.userToken(t, root.User.ID)

	owner, ownerTok, team := env.setupTeam(t, "acmecorp")
	proj, err := projects.CreateProject(authCtx(ctx, ownerTok), &serverv1.CreateProjectRequest{
		TeamId: team.GetId(), Slug: "web", Name: "Web", Description: "frontend",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.GetProject().GetTeamSlug() != "acmecorp" || proj.GetProject().GetSlug() != "web" {
		t.Fatalf("project view = %+v, want acmecorp/web", proj.GetProject())
	}

	// 团队 developer bob / viewer carol 入队。
	bob, bobTok := env.inviteAndAccept(t, ownerTok, team.GetId(), "bob@example.com", "developer")
	carol, _ := env.inviteAndAccept(t, ownerTok, team.GetId(), "carol@example.com", "viewer")
	projID := proj.GetProject().GetId()

	// ① developer 降 viewer（owner 执行）。
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: bob.User.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("override developer->viewer: %v", err)
	}
	// ② viewer 升 developer。
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: carol.User.ID, Role: "developer",
	}); err != nil {
		t.Fatalf("override viewer->developer: %v", err)
	}
	overrides, err := projects.ListProjectMembers(authCtx(ctx, ownerTok), &serverv1.ListProjectMembersRequest{ProjectId: projID})
	if err != nil || len(overrides.GetMembers()) != 2 {
		t.Fatalf("ListProjectMembers: %v (n=%d)", err, len(overrides.GetMembers()))
	}

	// ③ owner 不可覆写（409 冲突——owner 恒在全部项目保有 owner 权）。
	_, err = projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: owner.User.ID, Role: "viewer",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("owner override err = %v, want FailedPrecondition(409)", err)
	}
	// 非团队成员目标 → 409（覆写行仅限团队成员，D-W0-2）。
	stranger := env.seedUser(t, "stranger@example.com")
	_, err = projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: stranger.User.ID, Role: "viewer",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("non-member override err = %v, want FailedPrecondition(409)", err)
	}
	// 词表外角色（owner 不在覆写三档）→ 400。
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: bob.User.ID, Role: "owner",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("owner-role override err = %v, want InvalidArgument", err)
	}

	// ④ 覆写角色不授予成员管理权：团队 developer 带项目 admin 覆写，调用
	// 覆写管理面仍 403（权限怪圈防线，§3.3）。
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, ownerTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: bob.User.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("override bob to project admin: %v", err)
	}
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, bobTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: carol.User.ID, Role: "viewer",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("project-admin override management err = %v, want PermissionDenied", err)
	}

	// 覆写管理面 = 团队 admin/owner：提升 bob 为团队 admin 后放行。
	if _, err := teams.SetTeamMemberRole(authCtx(ctx, ownerTok), &serverv1.SetTeamMemberRoleRequest{
		TeamId: team.GetId(), UserId: bob.User.ID, Role: "admin",
	}); err != nil {
		t.Fatalf("promote bob to team admin: %v", err)
	}
	if _, err := projects.SetProjectMemberRole(authCtx(ctx, bobTok), &serverv1.SetProjectMemberRoleRequest{
		ProjectId: projID, UserId: carol.User.ID, Role: "viewer",
	}); err != nil {
		t.Fatalf("team admin override management: %v", err)
	}

	// 删除覆写行 = 回退团队角色。
	if _, err := projects.RemoveProjectMember(authCtx(ctx, bobTok), &serverv1.RemoveProjectMemberRequest{
		ProjectId: projID, UserId: carol.User.ID,
	}); err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}

	// 平台管理员读面放行；写面 403（不代写）。
	if _, err := projects.GetProject(authCtx(ctx, rootTok), &serverv1.GetProjectRequest{Id: projID}); err != nil {
		t.Fatalf("platform admin GetProject: %v", err)
	}
	if _, err := projects.UpdateProject(authCtx(ctx, rootTok), &serverv1.UpdateProjectRequest{Id: projID, Name: "hijack"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("platform admin UpdateProject err = %v, want PermissionDenied", err)
	}

	// 项目可见性：机具令牌 403；平台管理员 List = 全部；成员 List = 自己
	// 两队（注册个人队的 default + acmecorp 队的 web）。
	if _, err := projects.GetProject(authCtx(ctx, env.machineToken(t, "read")), &serverv1.GetProjectRequest{Id: projID}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("machine GetProject must be 403")
	}
	rootList, err := projects.ListProjects(authCtx(ctx, rootTok), &serverv1.ListProjectsRequest{})
	if err != nil || len(rootList.GetProjects()) < 3 {
		t.Fatalf("platform admin ListProjects = %d (%v), want ≥3 (all)", len(rootList.GetProjects()), err)
	}
	ownerList, err := projects.ListProjects(authCtx(ctx, ownerTok), &serverv1.ListProjectsRequest{})
	if err != nil || len(ownerList.GetProjects()) != 2 {
		t.Fatalf("owner ListProjects = %d (%v), want 2 (personal default + web)", len(ownerList.GetProjects()), err)
	}
	ownerSeen := map[string]bool{}
	for _, p := range ownerList.GetProjects() {
		ownerSeen[p.GetId()] = true
	}
	if !ownerSeen[projID] || !ownerSeen[owner.Project.ID] {
		t.Fatalf("owner visibility broken: %+v", ownerList.GetProjects())
	}

	// UpdateProject 仅 name/description（slug 结构性不可变）。
	up, err := projects.UpdateProject(authCtx(ctx, ownerTok), &serverv1.UpdateProjectRequest{
		Id: projID, Name: "Web Renamed", Description: "renamed",
	})
	if err != nil || up.GetProject().GetName() != "Web Renamed" || up.GetProject().GetSlug() != "web" {
		t.Fatalf("UpdateProject: %v (%+v)", err, up.GetProject())
	}

	// DeleteProject 非空守卫：项目内存活 app 行阻塞；清空后删除成功。
	appID := "01APPPROJ00000000000000000"
	if err := env.st.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO apps (id, name, lifecycle, created_at, updated_at, project_id, team_id)
			VALUES (?, ?, 'active', 1, 1, ?, (SELECT team_id FROM projects WHERE id = ?))`, appID, "webapp", projID, projID)
		return err
	}); err != nil {
		t.Fatalf("seed project app: %v", err)
	}
	if _, err := projects.DeleteProject(authCtx(ctx, ownerTok), &serverv1.DeleteProjectRequest{Id: projID}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("DeleteProject non-empty err = %v, want FailedPrecondition(409)", err)
	}
	if err := env.st.InTx(ctx, func(tx *state.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM apps WHERE id = ?`, appID)
		return err
	}); err != nil {
		t.Fatalf("clear project app: %v", err)
	}
	if _, err := projects.DeleteProject(authCtx(ctx, ownerTok), &serverv1.DeleteProjectRequest{Id: projID}); err != nil {
		t.Fatalf("DeleteProject after purge: %v", err)
	}
	if _, err := projects.GetProject(authCtx(ctx, ownerTok), &serverv1.GetProjectRequest{Id: projID}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetProject after delete err = %v, want NotFound", err)
	}
}

// TestTeamsProjectsScopeRegistrations：新面 scope 登记完整性（整体 read——
// 真授权在 handler 角色门；internal/api/scope.go 头注）。
func TestTeamsProjectsScopeRegistrations(t *testing.T) {
	teamMethods := []string{
		"CreateTeam", "ListTeams", "GetTeam", "UpdateTeam", "DeleteTeam",
		"ListTeamMembers", "SetTeamMemberRole", "RemoveTeamMember",
		"CreateInvite", "ListTeamInvites", "RevokeInvite",
	}
	for _, m := range teamMethods {
		if s, ok := RequiredScope("/fleetly.server.v1.TeamsService/" + m); !ok || s != ScopeRead {
			t.Errorf("TeamsService/%s scope = %q ok=%v, want read", m, s, ok)
		}
	}
	projectMethods := []string{
		"CreateProject", "ListProjects", "GetProject", "UpdateProject", "DeleteProject",
		"ListProjectMembers", "SetProjectMemberRole", "RemoveProjectMember",
	}
	for _, m := range projectMethods {
		if s, ok := RequiredScope("/fleetly.server.v1.ProjectsService/" + m); !ok || s != ScopeRead {
			t.Errorf("ProjectsService/%s scope = %q ok=%v, want read", m, s, ok)
		}
	}
}
