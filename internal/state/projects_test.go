package state

import (
	"context"
	"errors"
	"testing"
)

// TestProjectCRUDAndSlugConflict（RBAC 设计 §3.1/§3.4）：项目 CRUD；slug
// team 内唯一（同 slug 跨团队合法，D-W0-9）；UpdateProject 仅改
// name/description（slug 不可变）；删除空项目；守卫面。
func TestProjectCRUDAndSlugConflict(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	acme, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam acme: %v", err)
	}
	beta, err := st.CreateTeam(ctx, TeamWrite{Slug: "beta", Name: "Beta", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam beta: %v", err)
	}

	p1, err := st.CreateProject(ctx, ProjectWrite{TeamID: acme.ID, Slug: "default", Name: "Acme Default", Description: "landing zone"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p1.TeamID != acme.ID || p1.Slug != "default" || p1.Description != "landing zone" {
		t.Fatalf("unexpected project row: %+v", p1)
	}
	// team 内 slug 冲突。
	if _, err := st.CreateProject(ctx, ProjectWrite{TeamID: acme.ID, Slug: "default", Name: "dup"}); !errors.Is(err, ErrProjectSlugTaken) {
		t.Fatalf("dup slug in team err = %v, want ErrProjectSlugTaken", err)
	}
	// 同 slug 跨团队合法（D-W0-9：每个团队各有自己的 default）。
	bp, err := st.CreateProject(ctx, ProjectWrite{TeamID: beta.ID, Slug: "default", Name: "Beta Default"})
	if err != nil {
		t.Fatalf("same slug other team: %v", err)
	}
	// 写入通道防御。
	for name, w := range map[string]ProjectWrite{
		"empty team": {TeamID: "", Slug: "s", Name: "n"},
		"empty slug": {TeamID: acme.ID, Slug: " ", Name: "n"},
		"empty name": {TeamID: acme.ID, Slug: "s", Name: ""},
	} {
		if _, err := st.CreateProject(ctx, w); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}

	// 读面。
	got, err := st.GetProject(ctx, p1.ID)
	if err != nil || got.Name != "Acme Default" {
		t.Fatalf("GetProject: %v (%+v)", err, got)
	}
	if _, err := st.GetProject(ctx, "01MISSING000000000000000000"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetProject missing err = %v, want ErrProjectNotFound", err)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil || len(projects) != 2 || projects[0].ID != p1.ID || projects[1].ID != bp.ID {
		t.Fatalf("ListProjects: %v (%+v)", err, projects)
	}

	// UpdateProject：name/description 可改，slug/team 不动。
	upd, err := st.UpdateProject(ctx, ProjectUpdate{ID: p1.ID, Name: "Acme Prod", Description: "renamed"})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if upd.Name != "Acme Prod" || upd.Description != "renamed" || upd.Slug != "default" || upd.TeamID != acme.ID {
		t.Fatalf("updated project = %+v", upd)
	}
	if _, err := st.UpdateProject(ctx, ProjectUpdate{ID: p1.ID, Name: " "}); err == nil {
		t.Fatal("empty name must be rejected")
	}
	if _, err := st.UpdateProject(ctx, ProjectUpdate{ID: "01MISSING000000000000000000", Name: "n"}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("update missing err = %v, want ErrProjectNotFound", err)
	}

	// 删除空项目；再删不存在。
	if err := st.DeleteProject(ctx, bp.ID, "", ""); err != nil {
		t.Fatalf("DeleteProject empty: %v", err)
	}
	if _, err := st.GetProject(ctx, bp.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("deleted project must be gone: %v", err)
	}
	if err := st.DeleteProject(ctx, bp.ID, "", ""); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("delete missing err = %v, want ErrProjectNotFound", err)
	}

	// 审计动作落档（设计 §6）。
	audits, err := st.RecentAudits(ctx, 20)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var created, deleted bool
	for _, a := range audits {
		if a.Action == "project.created" && a.Target == "project:"+p1.ID {
			created = true
		}
		if a.Action == "project.deleted" && a.Target == "project:"+bp.ID {
			deleted = true
		}
	}
	if !created || !deleted {
		t.Fatalf("audit missing: created=%v deleted=%v", created, deleted)
	}
}

// TestDeleteProjectGuardNonEmpty（设计 §3.1：需项目已空，不做隐式级联）：
// 存活 app / 存活库实例阻塞删除；deleted tombstone / deleted 终态行不阻塞
// （平台无物理删除通道——已删光的 tombstone 项目必须可删）；删除同事务清
// 覆写成员行。
func TestDeleteProjectGuardNonEmpty(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	team, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	proj, err := st.CreateProject(ctx, ProjectWrite{TeamID: team.ID, Slug: "web", Name: "Web"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 存活 app（active）阻塞。
	if _, err := st.db.ExecContext(ctx, `INSERT INTO apps (id, name, lifecycle, created_at, updated_at, project_id, team_id)
		VALUES ('01APP1', 'web', 'active', 1, 1, ?, ?)`, proj.ID, team.ID); err != nil {
		t.Fatalf("insert live app: %v", err)
	}
	if err := st.DeleteProject(ctx, proj.ID, "", ""); !errors.Is(err, ErrProjectNotEmpty) {
		t.Fatalf("delete with live app err = %v, want ErrProjectNotEmpty", err)
	}
	// app 走 tombstone（deleted）后不阻塞。
	if _, err := st.db.ExecContext(ctx, `UPDATE apps SET lifecycle = 'deleted' WHERE id = '01APP1'`); err != nil {
		t.Fatalf("tombstone app: %v", err)
	}
	// 存活库实例（ready）阻塞。
	if _, err := st.db.ExecContext(ctx, `INSERT INTO db_instances (id, name, template, image_digest, settings, credential_cipher, platform_node_id, state, created_at, updated_at, project_id, team_id)
		VALUES ('01DB1', 'pg', 'postgres-16', 'sha256:x', '{}', 'cipher', '', 'ready', 1, 1, ?, ?)`, proj.ID, team.ID); err != nil {
		t.Fatalf("insert live db: %v", err)
	}
	if err := st.DeleteProject(ctx, proj.ID, "", ""); !errors.Is(err, ErrProjectNotEmpty) {
		t.Fatalf("delete with live db err = %v, want ErrProjectNotEmpty", err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE db_instances SET state = 'deleted' WHERE id = '01DB1'`); err != nil {
		t.Fatalf("delete db instance: %v", err)
	}

	// 全部删光：可删；覆写成员行同事务清理。
	if _, err := st.AddMember(ctx, team.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, owner.ID, ProjectRoleAdmin, "", ""); !errors.Is(err, ErrProjectOwnerOverride) {
		t.Fatalf("owner override probe err = %v, want ErrProjectOwnerOverride", err)
	}
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser mate: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleDeveloper, "", ""); err != nil {
		t.Fatalf("AddMember mate: %v", err)
	}
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, mate.ID, ProjectRoleViewer, "", ""); err != nil {
		t.Fatalf("override mate: %v", err)
	}
	if err := st.DeleteProject(ctx, proj.ID, "", ""); err != nil {
		t.Fatalf("DeleteProject after purge: %v", err)
	}
	rows, err := st.ListProjectMembers(ctx, proj.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("project members must be purged with the project: %v (n=%d)", err, len(rows))
	}
}

// TestProjectMemberOverrides（D-W0-2 队内覆写形）：非团队成员拒绝
// （ErrNotTeamMember）；团队成员 upsert（建行/改档双向覆写）；owner 不可
// 覆写（ErrProjectOwnerOverride）；角色词表三档校验；移除/列表读面。
func TestProjectMemberOverrides(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser mate: %v", err)
	}
	outsider, err := st.CreateUser(ctx, UserWrite{Email: "outsider@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser outsider: %v", err)
	}
	team, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember owner: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleDeveloper, "", ""); err != nil {
		t.Fatalf("AddMember mate: %v", err)
	}
	proj, err := st.CreateProject(ctx, ProjectWrite{TeamID: team.ID, Slug: "web", Name: "Web"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// 非团队成员（含项目不存在）拒绝。
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, outsider.ID, ProjectRoleViewer, "", ""); !errors.Is(err, ErrNotTeamMember) {
		t.Fatalf("outsider override err = %v, want ErrNotTeamMember", err)
	}
	if _, err := st.SetProjectMemberRole(ctx, "01MISSING000000000000000000", mate.ID, ProjectRoleViewer, "", ""); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("missing project err = %v, want ErrProjectNotFound", err)
	}
	// 词表校验（三档之外拒绝）与空参数防御。
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, mate.ID, "owner", "", ""); err == nil {
		t.Fatal("project override role 'owner' must be rejected")
	}
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, "", ProjectRoleViewer, "", ""); err == nil {
		t.Fatal("empty user id must be rejected")
	}

	// 团队成员：建行（developer → viewer 降权覆写）。
	m, err := st.SetProjectMemberRole(ctx, proj.ID, mate.ID, ProjectRoleViewer, "", "")
	if err != nil {
		t.Fatalf("override mate: %v", err)
	}
	if m.Role != ProjectRoleViewer || m.CreatedAt.IsZero() {
		t.Fatalf("unexpected override row: %+v", m)
	}
	// upsert：改档（viewer → admin 升权覆写），created_at 保持原值。
	m2, err := st.SetProjectMemberRole(ctx, proj.ID, mate.ID, ProjectRoleAdmin, "", "")
	if err != nil {
		t.Fatalf("override mate upsert: %v", err)
	}
	if m2.Role != ProjectRoleAdmin || !m2.CreatedAt.Equal(m.CreatedAt) {
		t.Fatalf("upsert must keep created_at and change role: %+v vs %+v", m2, m)
	}

	// owner 不可覆写（设计 §3.3：owner 恒在全部项目保有 owner 权）。
	if _, err := st.SetProjectMemberRole(ctx, proj.ID, owner.ID, ProjectRoleViewer, "", ""); !errors.Is(err, ErrProjectOwnerOverride) {
		t.Fatalf("owner override err = %v, want ErrProjectOwnerOverride", err)
	}

	// 读面 + 移除。
	rows, err := st.ListProjectMembers(ctx, proj.ID)
	if err != nil || len(rows) != 1 || rows[0].UserID != mate.ID {
		t.Fatalf("ListProjectMembers: %v (%+v)", err, rows)
	}
	if err := st.RemoveProjectMember(ctx, proj.ID, outsider.ID, "", ""); !errors.Is(err, ErrProjectMemberNotFound) {
		t.Fatalf("remove missing override err = %v, want ErrProjectMemberNotFound", err)
	}
	if err := st.RemoveProjectMember(ctx, proj.ID, mate.ID, "", ""); err != nil {
		t.Fatalf("RemoveProjectMember: %v", err)
	}
	if rows, err := st.ListProjectMembers(ctx, proj.ID); err != nil || len(rows) != 0 {
		t.Fatalf("override row must be gone: %v (n=%d)", err, len(rows))
	}

	// 审计动作落档（设计 §6）。
	audits, err := st.RecentAudits(ctx, 30)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var roleChanged, memberRemoved bool
	for _, a := range audits {
		if a.Target == "project:"+proj.ID && a.Action == "project.member_role_changed" {
			roleChanged = true
		}
		if a.Target == "project:"+proj.ID && a.Action == "project.member_removed" {
			memberRemoved = true
		}
	}
	if !roleChanged || !memberRemoved {
		t.Fatalf("audit missing: role_changed=%v member_removed=%v", roleChanged, memberRemoved)
	}
}

// TestAppsProjectNameUnique（D-W0-4 二修，随 00018 建索引）：UNIQUE
// (project_id, name) 行为抽检——同项目同名拒绝；NULL 归属多行不阻塞
// （两条 NULL project 的 app 共存，SQLite NULL 互异语义）。诚实口径：W1
// 的 apps.name / db_instances.name 全局 UNIQUE（00001/00015）仍在位，00019
// 表重建时随 NOT NULL 收敛一并降级——故此处「同项目同名」断言无法区分是
// 哪个唯一约束触发（复合索引的在册与列序由 TestMigration00018RBACSchema
// 钉死），「同名跨项目合法」是 00019 后的行为，不在本票断言面。
func TestAppsProjectNameUnique(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	insApp := func(id, name string, projectID any) error {
		_, err := st.db.ExecContext(ctx, `INSERT INTO apps (id, name, lifecycle, created_at, updated_at, project_id)
			VALUES (?, ?, 'active', 1, 1, ?)`, id, name, projectID)
		return err
	}
	// NULL 归属两行共存（归属接入前的存量形态，无阻塞）。
	if err := insApp("01APP1", "web", nil); err != nil {
		t.Fatalf("insert NULL-project app 1: %v", err)
	}
	if err := insApp("01APP2", "api", nil); err != nil {
		t.Fatalf("insert NULL-project app 2: %v", err)
	}
	// 同项目同名 → 唯一冲突（project 内唯一语义；W1 期间由全局 name
	// UNIQUE 先触发，复合唯一索引在册由迁移测试保证）。
	if err := insApp("01APP3", "web2", "01PROJ1"); err != nil {
		t.Fatalf("insert project app: %v", err)
	}
	if err := insApp("01APP4", "web2", "01PROJ1"); !isUniqueViolation(err) {
		t.Fatalf("duplicate (project_id, name) err = %v, want unique violation", err)
	}

	// db_instances 同口径：同项目同名拒绝。
	insDb := func(id, name string, projectID any) error {
		_, err := st.db.ExecContext(ctx, `INSERT INTO db_instances (id, name, template, image_digest, settings, credential_cipher, platform_node_id, state, created_at, updated_at, project_id)
			VALUES (?, ?, 'postgres-16', 'sha256:x', '{}', 'cipher', '', 'ready', 1, 1, ?)`, id, name, projectID)
		return err
	}
	if err := insDb("01DB1", "pg", "01PROJ1"); err != nil {
		t.Fatalf("insert db 1: %v", err)
	}
	if err := insDb("01DB2", "pg", "01PROJ1"); !isUniqueViolation(err) {
		t.Fatalf("duplicate (project_id, name) db err = %v, want unique violation", err)
	}
	// NULL 归属库实例共存。
	if err := insDb("01DB3", "pgnull", nil); err != nil {
		t.Fatalf("insert NULL-project db: %v", err)
	}
}
