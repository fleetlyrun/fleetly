package state

// MoveApp 资源改派原语的单测（v0.3 W2-S3 续作补验）：归属切换、slug 随行
// 重载（QualifiedName 换段）、审计 app.moved 同事务、同项目/同名占用守卫
// ——rpc 编排与换名重部署面由 api/engine 层消费（internal/engine/move_test.go
// 钉编排），此处钉权威态语义。夹具直用 state 原语播种（testsupport 反向
// import state，测试文件内引会成环）。

import (
	"context"
	"errors"
	"testing"
)

// moveSeedProject 播种一个独立团队+项目（slug 带序号保证 UNIQUE 不串扰）。
func moveSeedProject(t *testing.T, st *Store, ownerID, tag string) Project {
	t.Helper()
	team, err := st.CreateTeam(context.Background(), TeamWrite{
		Slug: "mv" + tag, Name: "move team " + tag, CreatedBy: ownerID,
	})
	if err != nil {
		t.Fatalf("seed team %s: %v", tag, err)
	}
	proj, err := st.CreateProject(context.Background(), ProjectWrite{
		TeamID: team.ID, Slug: "p" + tag, Name: "move project " + tag,
	})
	if err != nil {
		t.Fatalf("seed project %s: %v", tag, err)
	}
	return proj
}

// toTeamSlug 取项目归属团队的 slug（QualifiedName 首段的期望值源）。
func toTeamSlug(t *testing.T, st *Store, p Project) string {
	t.Helper()
	team, err := st.GetTeam(context.Background(), p.TeamID)
	if err != nil {
		t.Fatalf("get team %s: %v", p.TeamID, err)
	}
	return team.Slug
}

func TestMoveAppOwnershipSwitch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "mover@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	from := moveSeedProject(t, st, owner.ID, "a")
	to := moveSeedProject(t, st, owner.ID, "b")
	app, err := st.CreateApp(ctx, "", "web", from.ID, from.TeamID)
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}

	moved, err := st.MoveApp(ctx, app.ID, to.ID, owner.ID, "")
	if err != nil {
		t.Fatalf("MoveApp: %v", err)
	}
	if moved.ProjectID != to.ID || moved.TeamID != to.TeamID {
		t.Fatalf("ownership not switched: project=%s team=%s, want project=%s team=%s",
			moved.ProjectID, moved.TeamID, to.ID, to.TeamID)
	}
	if moved.ProjectSlug != to.Slug {
		t.Fatalf("project slug = %q, want %q (row must reload the immutable slug join)", moved.ProjectSlug, to.Slug)
	}
	if got, want := moved.QualifiedName(), toTeamSlug(t, st, to)+"/"+to.Slug+"/web"; got != want {
		t.Fatalf("QualifiedName = %q, want %q", got, want)
	}
	// 回读同构（读面与写后返回行一致）。
	reread, err := st.GetAppByID(ctx, app.ID)
	if err != nil {
		t.Fatalf("reread app: %v", err)
	}
	if reread.ProjectID != to.ID || reread.QualifiedName() != moved.QualifiedName() {
		t.Fatalf("reread diverges from move result: project=%s qn=%s", reread.ProjectID, reread.QualifiedName())
	}
	// 审计同事务：app.moved + actor 投影。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("recent audits: %v", err)
	}
	found := false
	for _, a := range audits {
		if a.Action == "app.moved" && a.Target == "app:"+app.ID && a.Actor == "user:"+owner.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("app.moved audit row missing in %d recent rows", len(audits))
	}
}

func TestMoveAppSameProjectRejected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "mover@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	proj := moveSeedProject(t, st, owner.ID, "same")
	app, err := st.CreateApp(ctx, "", "web", proj.ID, proj.TeamID)
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}

	if _, err := st.MoveApp(ctx, app.ID, proj.ID, owner.ID, ""); !errors.Is(err, ErrMoveSameTarget) {
		t.Fatalf("same-project move err = %v, want ErrMoveSameTarget", err)
	}
}

func TestMoveAppNameTakenInTarget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "mover@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	from := moveSeedProject(t, st, owner.ID, "src")
	to := moveSeedProject(t, st, owner.ID, "dst")
	app, err := st.CreateApp(ctx, "", "web", from.ID, from.TeamID)
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}
	if _, err := st.CreateApp(ctx, "", "web", to.ID, to.TeamID); err != nil {
		t.Fatalf("seed target twin: %v", err)
	}

	if _, err := st.MoveApp(ctx, app.ID, to.ID, owner.ID, ""); !errors.Is(err, ErrAppExists) {
		t.Fatalf("name-taken move err = %v, want ErrAppExists", err)
	}
	// 源行归属未被半程改写（事务回滚面）。
	after, err := st.GetAppByID(ctx, app.ID)
	if err != nil {
		t.Fatalf("reread app: %v", err)
	}
	if after.ProjectID != from.ID {
		t.Fatalf("source row moved on rejected move: project=%s", after.ProjectID)
	}
}

func TestMoveAppDeletedRowAlsoOccupiesName(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "mover@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	from := moveSeedProject(t, st, owner.ID, "src")
	to := moveSeedProject(t, st, owner.ID, "dst")
	app, err := st.CreateApp(ctx, "", "web", from.ID, from.TeamID)
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}
	// 目标项目里的同名 tombstone 行：UNIQUE(project_id,name) 无生命周期
	// 豁免——占用语义与 CreateApp「任意生命周期态名字均占用」一致，探针把
	// 物理违反提前映射成 ErrAppExists（而非裸约束错误）。
	tomb, err := st.CreateApp(ctx, "", "web", to.ID, to.TeamID)
	if err != nil {
		t.Fatalf("seed tomb: %v", err)
	}
	if err := st.MarkAppDeleting(ctx, tomb.ID); err != nil {
		t.Fatalf("MarkAppDeleting: %v", err)
	}
	if err := st.MarkAppDeleted(ctx, tomb.ID); err != nil {
		t.Fatalf("MarkAppDeleted: %v", err)
	}

	if _, err := st.MoveApp(ctx, app.ID, to.ID, owner.ID, ""); !errors.Is(err, ErrAppExists) {
		t.Fatalf("deleted-tombstone move err = %v, want ErrAppExists", err)
	}
}
