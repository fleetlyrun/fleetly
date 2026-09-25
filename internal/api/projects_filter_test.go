package api

// 2026-09-25 staging 真机走查实爆三断面的回归钉（用户判定「未达可用」的
// 修复面）：
//  1. ListProjects 的 team_id 收窄曾是恒真条件（`req.GetTeamId() != ""`），
//     团队设置 Projects tab 串出全部团队的项目——严格归属匹配回归；
//  2. resolveApp/resolveDatabaseRef 的平台 ID 短路（26 字符 ULID）——
//     Console 详情导航以 id 寻址（同名 app 裸名必歧义、REST 单段路由承载
//     不了三段限定形）；
//  3. AppView/GetAppResponse 的归属 slug 投影（team_slug/project_slug）——
//     Console 限定形展示与团队级客户端收窄的数据源。

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestListProjectsTeamIdFilter：team_id 收窄 = 严格归属匹配（平台管理员带
// team_id 也不再返回全集）；非成员带 team_id 仍 403；同名 project 跨队
// （D-W0-9）在收窄面各自唯一。
func TestListProjectsTeamIdFilter(t *testing.T) {
	env := newTeamEnv(t)
	projects := serverv1.NewProjectsServiceClient(env.conn)
	ctx := context.Background()

	root := env.seedUser(t, "filter-root@example.com")
	rootTok := env.userToken(t, root.User.ID)
	_, ownerATok, teamA := env.setupTeam(t, "alpha")
	_, ownerBTok, teamB := env.setupTeam(t, "beta")

	// 两队各建同名 project "web"（队内唯一语义的可分性锚点）。
	for _, tc := range []struct { tok string; teamID *serverv1.TeamView } {
		{ownerATok, teamA}, {ownerBTok, teamB},
	} {
		if _, err := projects.CreateProject(authCtx(ctx, tc.tok), &serverv1.CreateProjectRequest{
			TeamId: tc.teamID.GetId(), Slug: "web", Name: "Web",
		}); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}

	// 平台管理员 + team_id → 只回该队项目（修复点：此前恒真条件回全集）。
	for _, tc := range []struct { teamID string; wantSlug string } {
		{teamA.GetId(), "alpha"}, {teamB.GetId(), "beta"},
	} {
		got, err := projects.ListProjects(authCtx(ctx, rootTok), &serverv1.ListProjectsRequest{TeamId: tc.teamID})
		if err != nil {
			t.Fatalf("admin ListProjects(team_id): %v", err)
		}
		if len(got.GetProjects()) != 1 {
			t.Fatalf("admin ListProjects(team_id) = %d rows, want 1 (strict filter)", len(got.GetProjects()))
		}
		if p := got.GetProjects()[0]; p.GetTeamSlug() != tc.wantSlug {
			t.Fatalf("filtered row team_slug = %q, want %q", p.GetTeamSlug(), tc.wantSlug)
		}
	}

	// 非成员带 team_id → 403（requireTeamReadAccess 不放行）。
	if _, err := projects.ListProjects(authCtx(ctx, ownerATok), &serverv1.ListProjectsRequest{TeamId: teamB.GetId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member team_id filter err = %v, want PermissionDenied", err)
	}
}

// TestResolveAppByIDAndOwnershipProjection：平台 ID 短路（同名 app 的 id 引
// 用各自精确命中、裸名仍歧义）+ AppView/GetAppResponse 归属 slug 投影。
func TestResolveAppByIDAndOwnershipProjection(t *testing.T) {
	env := newTestEnv(t)
	apps := serverv1.NewAppsServiceClient(env.conn)
	ctx := authCtx(context.Background(), env.admTok)

	// 第二个团队+项目（夹具项目之外）：同名 app 两行跨项目。
	team, err := env.st.CreateTeam(context.Background(), state.TeamWrite{Slug: "beta", Name: "Beta", CreatedBy: "test"})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	projBeta, err := env.st.CreateProject(context.Background(), state.ProjectWrite{TeamID: team.ID, Slug: "web", Name: "Web"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	var ids []string
	for _, proj := range []state.Project{env.fixtureProject, projBeta} {
		app, err := env.st.CreateApp(context.Background(), "", "demo", proj.ID, proj.TeamID)
		if err != nil {
			t.Fatalf("CreateApp: %v", err)
		}
		ids = append(ids, app.ID)
	}

	// ListApps 投影：team_slug/project_slug 随行（同名行各自归属可分）。
	list, err := apps.ListApps(ctx, &serverv1.ListAppsRequest{})
	if err != nil || len(list.GetApps()) != 2 {
		t.Fatalf("ListApps: %v (n=%d)", err, len(list.GetApps()))
	}
	seen := map[string][2]string{}
	for _, a := range list.GetApps() {
		if a.GetTeamSlug() == "" || a.GetProjectSlug() == "" {
			t.Fatalf("AppView ownership projection empty for %s", a.GetId())
		}
		seen[a.GetId()] = [2]string{a.GetTeamSlug(), a.GetProjectSlug()}
	}
	if seen[ids[0]] != [2]string{"tfixture", "fixture"} || seen[ids[1]] != [2]string{"beta", "web"} {
		t.Fatalf("ownership projection = %v, want tfixture/fixture + beta/web", seen)
	}

	// id 引用：同名两行各自精确命中（修复点：此前 "app not found"）。
	for i, id := range ids {
		got, err := apps.GetApp(ctx, &serverv1.GetAppRequest{Name: id})
		if err != nil {
			t.Fatalf("GetApp by id[%d]: %v", i, err)
		}
		if got.GetId() != id {
			t.Fatalf("GetApp by id = %s, want %s", got.GetId(), id)
		}
		if got.GetTeamSlug() == "" {
			t.Fatalf("GetAppResponse team_slug empty")
		}
	}

	// 裸名仍歧义（id 短路不放松按名纪律）。
	_, err = apps.GetApp(ctx, &serverv1.GetAppRequest{Name: "demo"})
	if got := status.Code(err); got != codes.FailedPrecondition || !errors.Is(err, err) {
		t.Logf("bare-name ambiguity err = %v (code=%s) — envelope code asserted below", err, got)
	}
	if codeOf(t, err) != "E_APP_AMBIGUOUS" {
		t.Fatalf("bare name err = %v, want E_APP_AMBIGUOUS", err)
	}

	// 库族同款：id 短路直调 resolveDatabaseRef（machine principal = 全库）。
	inst, err := env.st.CreateDatabaseInstance(context.Background(), state.DatabaseInstance{
		Name: "pgdemo", Template: "postgres", ImageDigest: "sha256:test",
		CredentialCipher: "cipher-blob", ProjectID: env.fixtureProject.ID, TeamID: env.fixtureProject.TeamID,
	})
	if err != nil {
		t.Fatalf("CreateDatabaseInstance: %v", err)
	}
	got, err := resolveDatabaseRef(directCtx(context.Background()), env.st, inst.ID)
	if err != nil || got.ID != inst.ID {
		t.Fatalf("resolveDatabaseRef by id = %v (%v), want %s", got.ID, err, inst.ID)
	}
}
