package state

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTeamCRUDAndSlugConflict（RBAC 设计 §3.1）：建队/取行/列表；slug 全局
// 冲突 → ErrTeamSlugTaken；写入通道非空校验。
func TestTeamCRUDAndSlugConflict(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	creator, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	team, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme Inc", CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if team.ID == "" || team.Slug != "acme" || team.Name != "Acme Inc" || team.CreatedBy != creator.ID {
		t.Fatalf("unexpected team row: %+v", team)
	}
	if team.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be set")
	}

	if _, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "dup", CreatedBy: creator.ID}); !errors.Is(err, ErrTeamSlugTaken) {
		t.Fatalf("duplicate slug err = %v, want ErrTeamSlugTaken", err)
	}

	got, err := st.GetTeam(ctx, team.ID)
	if err != nil || got.Slug != "acme" {
		t.Fatalf("GetTeam: %v (%+v)", err, got)
	}
	bySlug, err := st.GetTeamBySlug(ctx, "acme")
	if err != nil || bySlug.ID != team.ID {
		t.Fatalf("GetTeamBySlug: %v (%+v)", err, bySlug)
	}
	if _, err := st.GetTeamBySlug(ctx, "missing"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("GetTeamBySlug missing err = %v, want ErrTeamNotFound", err)
	}
	if _, err := st.GetTeam(ctx, "01MISSING000000000000000000"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("GetTeam missing err = %v, want ErrTeamNotFound", err)
	}

	team2, err := st.CreateTeam(ctx, TeamWrite{Slug: "beta", Name: "Beta", CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("CreateTeam second: %v", err)
	}
	teams, err := st.ListTeams(ctx)
	if err != nil || len(teams) != 2 || teams[0].ID != team.ID || teams[1].ID != team2.ID {
		t.Fatalf("ListTeams: %v (%+v)", err, teams)
	}

	// 写入通道防御：空 slug/name/created_by 拒绝（fail-fast，不做宽松兜底）。
	for name, w := range map[string]TeamWrite{
		"empty slug":       {Slug: "", Name: "n", CreatedBy: creator.ID},
		"empty name":       {Slug: "s", Name: " ", CreatedBy: creator.ID},
		"empty created_by": {Slug: "s", Name: "n", CreatedBy: ""},
	} {
		if _, err := st.CreateTeam(ctx, w); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

// TestTeamMembership 成员原语（设计 §3.1/§3.2）：AddMember 角色词表校验 +
// 重复加入冲突；SetMemberRole 改档；GetMembership/ListMembers 读面。
func TestTeamMembership(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser second: %v", err)
	}
	team, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember owner: %v", err)
	}
	m, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleDeveloper, "", "")
	if err != nil {
		t.Fatalf("AddMember developer: %v", err)
	}
	if m.Role != TeamRoleDeveloper || m.CreatedAt.IsZero() {
		t.Fatalf("unexpected membership: %+v", m)
	}

	// 重复加入 → ErrTeamMemberExists；非法角色写入通道拒绝。
	if _, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleViewer, "", ""); !errors.Is(err, ErrTeamMemberExists) {
		t.Fatalf("duplicate member err = %v, want ErrTeamMemberExists", err)
	}
	if _, err := st.AddMember(ctx, team.ID, "01THIRD0000000000000000000", "boss", "", ""); err == nil {
		t.Fatal("invalid team role must be rejected at write channel")
	}

	// 改档 + 读面。
	if _, err := st.SetMemberRole(ctx, team.ID, mate.ID, TeamRoleViewer, "", ""); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	got, err := st.GetMembership(ctx, team.ID, mate.ID)
	if err != nil || got.Role != TeamRoleViewer {
		t.Fatalf("GetMembership: %v (%+v)", err, got)
	}
	if _, err := st.GetMembership(ctx, team.ID, "01THIRD0000000000000000000"); !errors.Is(err, ErrTeamMemberNotFound) {
		t.Fatalf("GetMembership missing err = %v, want ErrTeamMemberNotFound", err)
	}
	if _, err := st.SetMemberRole(ctx, team.ID, "01THIRD0000000000000000000", TeamRoleViewer, "", ""); !errors.Is(err, ErrTeamMemberNotFound) {
		t.Fatalf("SetMemberRole missing err = %v, want ErrTeamMemberNotFound", err)
	}
	members, err := st.ListMembers(ctx, team.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("ListMembers: %v (n=%d)", err, len(members))
	}

	// 审计动作落档（设计 §6）。
	audits, err := st.RecentAudits(ctx, 20)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var added, changed bool
	for _, a := range audits {
		if a.Target == "team:"+team.ID && a.Action == "team.member_added" {
			added = true
		}
		if a.Target == "team:"+team.ID && a.Action == "team.member_role_changed" {
			changed = true
		}
	}
	if !added || !changed {
		t.Fatalf("audit missing: member_added=%v member_role_changed=%v", added, changed)
	}
}

// TestLastOwnerGuard（设计 §3.1：owner 恒在位）：降级/移除最后一名 owner
// 被拒（ErrTeamLastOwner，事务回滚不生效）；存在第二名 owner 后放行。
func TestLastOwnerGuard(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser second: %v", err)
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

	// 最后一名 owner：降级与移除都被拒，行不生效。
	if _, err := st.SetMemberRole(ctx, team.ID, owner.ID, TeamRoleAdmin, "", ""); !errors.Is(err, ErrTeamLastOwner) {
		t.Fatalf("demote last owner err = %v, want ErrTeamLastOwner", err)
	}
	if err := st.RemoveMember(ctx, team.ID, owner.ID, "", ""); !errors.Is(err, ErrTeamLastOwner) {
		t.Fatalf("remove last owner err = %v, want ErrTeamLastOwner", err)
	}
	got, err := st.GetMembership(ctx, team.ID, owner.ID)
	if err != nil || got.Role != TeamRoleOwner {
		t.Fatalf("owner membership must be intact after guard: %v (%+v)", err, got)
	}

	// 第二名 owner 就位后（原 developer 升 owner，此刻两名 owner）：
	// 移除其一放行；随后原 owner 降级被拒（又成最后一名）。
	if _, err := st.SetMemberRole(ctx, team.ID, mate.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("promote second owner: %v", err)
	}
	if err := st.RemoveMember(ctx, team.ID, mate.ID, "", ""); err != nil {
		t.Fatalf("remove one of two owners: %v", err)
	}
	if _, err := st.SetMemberRole(ctx, team.ID, owner.ID, TeamRoleAdmin, "", ""); !errors.Is(err, ErrTeamLastOwner) {
		t.Fatalf("demote final owner err = %v, want ErrTeamLastOwner", err)
	}
}

// TestRemoveMemberCascadesProjectOverrides（D-W0-2 级联语义）：移出团队 =
// 同事务清理该用户在该团队全部项目下的覆写行；其他团队的成员关系与覆写
// 行不受波及。
func TestRemoveMemberCascadesProjectOverrides(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser second: %v", err)
	}
	acme, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam acme: %v", err)
	}
	beta, err := st.CreateTeam(ctx, TeamWrite{Slug: "beta", Name: "Beta", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam beta: %v", err)
	}
	p1, err := st.CreateProject(ctx, ProjectWrite{TeamID: acme.ID, Slug: "web", Name: "Acme Web"})
	if err != nil {
		t.Fatalf("CreateProject p1: %v", err)
	}
	p2, err := st.CreateProject(ctx, ProjectWrite{TeamID: acme.ID, Slug: "ops", Name: "Acme Ops"})
	if err != nil {
		t.Fatalf("CreateProject p2: %v", err)
	}
	bp, err := st.CreateProject(ctx, ProjectWrite{TeamID: beta.ID, Slug: "web", Name: "Beta Web"})
	if err != nil {
		t.Fatalf("CreateProject bp: %v", err)
	}

	if _, err := st.AddMember(ctx, acme.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember owner acme: %v", err)
	}
	if _, err := st.AddMember(ctx, acme.ID, mate.ID, TeamRoleDeveloper, "", ""); err != nil {
		t.Fatalf("AddMember mate acme: %v", err)
	}
	if _, err := st.AddMember(ctx, beta.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember owner beta: %v", err)
	}
	if _, err := st.AddMember(ctx, beta.ID, mate.ID, TeamRoleViewer, "", ""); err != nil {
		t.Fatalf("AddMember mate beta: %v", err)
	}
	// mate 在 acme 两个项目下都有覆写行；beta 项目下的覆写行应幸存。
	if _, err := st.SetProjectMemberRole(ctx, p1.ID, mate.ID, ProjectRoleViewer, "", ""); err != nil {
		t.Fatalf("override p1: %v", err)
	}
	if _, err := st.SetProjectMemberRole(ctx, p2.ID, mate.ID, ProjectRoleAdmin, "", ""); err != nil {
		t.Fatalf("override p2: %v", err)
	}
	if _, err := st.SetProjectMemberRole(ctx, bp.ID, mate.ID, ProjectRoleDeveloper, "", ""); err != nil {
		t.Fatalf("override bp: %v", err)
	}

	if err := st.RemoveMember(ctx, acme.ID, mate.ID, "", ""); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if _, err := st.GetMembership(ctx, acme.ID, mate.ID); !errors.Is(err, ErrTeamMemberNotFound) {
		t.Fatalf("membership after removal err = %v, want ErrTeamMemberNotFound", err)
	}
	for _, p := range []Project{p1, p2} {
		rows, err := st.ListProjectMembers(ctx, p.ID)
		if err != nil || len(rows) != 0 {
			t.Fatalf("overrides in %s must be purged: %v (n=%d)", p.Slug, err, len(rows))
		}
	}
	// beta 的成员关系与覆写行不受波及。
	if got, err := st.GetMembership(ctx, beta.ID, mate.ID); err != nil || got.Role != TeamRoleViewer {
		t.Fatalf("beta membership must survive: %v (%+v)", err, got)
	}
	rows, err := st.ListProjectMembers(ctx, bp.ID)
	if err != nil || len(rows) != 1 || rows[0].Role != ProjectRoleDeveloper {
		t.Fatalf("beta override must survive: %v (%+v)", err, rows)
	}
	// 审计 team.member_removed 落档（含清理计数）。
	audits, err := st.RecentAudits(ctx, 20)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var removed bool
	for _, a := range audits {
		if a.Target == "team:"+acme.ID && a.Action == "team.member_removed" {
			removed = true
		}
	}
	if !removed {
		t.Fatal("audit team.member_removed missing")
	}
}

// TestInviteLifecycle（设计 §3.1 邀请语义）：创建（明文 token 一次性返回、
// 库存哈希、7 天过期）→ 消费（原子落成员行 + accepted_at）→ 复用拒绝；
// 已吊销/已过期/查无此 token 统一 ErrInviteInvalid；已是成员时消费不改
// 现有角色。
func TestInviteLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	owner, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	invitee, err := st.CreateUser(ctx, UserWrite{Email: "INVITEE@Example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser invitee: %v", err)
	}
	team, err := st.CreateTeam(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, owner.ID, TeamRoleOwner, "", ""); err != nil {
		t.Fatalf("AddMember owner: %v", err)
	}

	inv, token, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "Invitee@Example.COM", Role: TeamRoleDeveloper, ActorUserID: owner.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Email != "invitee@example.com" {
		t.Fatalf("invite email = %q, want lowercase normalized", inv.Email)
	}
	if inv.Role != TeamRoleDeveloper {
		t.Fatalf("invite role = %q", inv.Role)
	}
	if !inv.ExpiresAt.After(time.Now().UTC().Add(DefaultInviteTTL-time.Hour)) {
		t.Fatalf("expires_at = %v, want ~7d out", inv.ExpiresAt)
	}
	// token 哈希入库：库存 sha256，不落明文。
	var stored string
	if err := st.db.QueryRowContext(ctx, `SELECT token_hash FROM team_invites WHERE id = ?`, inv.ID).Scan(&stored); err != nil {
		t.Fatalf("scan invite hash: %v", err)
	}
	if stored != HashToken(token) || strings.Contains(stored, token) {
		t.Fatalf("invite storage must hold sha256(token), got %q", stored)
	}
	if _, _, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "x@y.com", Role: "boss"}); err == nil {
		t.Fatal("invalid invite role must be rejected at write channel")
	}

	invites, err := st.ListInvites(ctx, team.ID)
	if err != nil || len(invites) != 1 || invites[0].ID != inv.ID {
		t.Fatalf("ListInvites: %v (%+v)", err, invites)
	}

	// 消费：成员行落位（受邀角色）+ accepted_at 置位。
	if got, err := st.ConsumeInvite(ctx, token, invitee.ID, invitee.ID, ""); err != nil || got.Role != TeamRoleDeveloper {
		t.Fatalf("ConsumeInvite: %v (%+v)", err, got)
	}
	got, err := st.GetMembership(ctx, team.ID, invitee.ID)
	if err != nil || got.Role != TeamRoleDeveloper {
		t.Fatalf("membership after accept: %v (%+v)", err, got)
	}
	after, err := st.ListInvites(ctx, team.ID)
	if err != nil || after[0].AcceptedAt.IsZero() {
		t.Fatalf("accepted_at must be set: %v (%+v)", err, after)
	}

	// 一次性：复用同一 token 拒绝。
	if _, err := st.ConsumeInvite(ctx, token, invitee.ID, "", ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("reuse consumed invite err = %v, want ErrInviteInvalid", err)
	}
	// 查无此 token：同码拒绝（不泄漏存在性）。
	if _, err := st.ConsumeInvite(ctx, "bogus-token", invitee.ID, "", ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("unknown invite err = %v, want ErrInviteInvalid", err)
	}
	if _, err := st.ConsumeInvite(ctx, "", invitee.ID, "", ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("empty invite err = %v, want ErrInviteInvalid", err)
	}

	// 吊销路径：新邀请吊销后消费拒绝；RevokeInvite 幂等面（已吊销/已消费
	// = 幂等成功；不存在 = ErrInviteNotFound）。
	inv2, token2, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "other@example.com", Role: TeamRoleViewer})
	if err != nil {
		t.Fatalf("CreateInvite second: %v", err)
	}
	if err := st.RevokeInvite(ctx, inv2.ID, owner.ID, ""); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if _, err := st.ConsumeInvite(ctx, token2, invitee.ID, "", ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("revoked invite err = %v, want ErrInviteInvalid", err)
	}
	if err := st.RevokeInvite(ctx, inv2.ID, "", ""); err != nil {
		t.Fatalf("revoke idempotent: %v", err)
	}
	if err := st.RevokeInvite(ctx, inv.ID, "", ""); err != nil {
		t.Fatalf("revoke consumed invite is idempotent success: %v", err)
	}
	if err := st.RevokeInvite(ctx, "01MISSING000000000000000000", "", ""); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("revoke missing err = %v, want ErrInviteNotFound", err)
	}

	// 过期路径：新邀请把 expires_at 回拨到过去（store 层无时钟注入先例，
	// 直接构造过期存量）→ 消费拒绝。
	inv3, token3, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "third@example.com", Role: TeamRoleAdmin})
	if err != nil {
		t.Fatalf("CreateInvite third: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE team_invites SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute).UnixNano(), inv3.ID); err != nil {
		t.Fatalf("backdate expires_at: %v", err)
	}
	if _, err := st.ConsumeInvite(ctx, token3, invitee.ID, "", ""); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("expired invite err = %v, want ErrInviteInvalid", err)
	}

	// 已是成员：消费另一封未消费的邀请 = 成功（invite 视为已消费）且不改
	// 现有角色（不重复插入成员行）。
	_, token4, err4 := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "invitee@example.com", Role: TeamRoleAdmin})
	if err4 != nil {
		t.Fatalf("CreateInvite fourth: %v", err4)
	}
	if _, err := st.ConsumeInvite(ctx, token4, invitee.ID, "", ""); err != nil {
		t.Fatalf("consume invite for existing member: %v", err)
	}
	if got, err := st.GetMembership(ctx, team.ID, invitee.ID); err != nil || got.Role != TeamRoleDeveloper {
		t.Fatalf("existing member role must not change on re-invite accept: %v (%+v)", err, got)
	}
}

// TestCreateTeamWithOwner（v0.3 W2-S1）：建队 + owner 成员行 + 审计 + 事件
// 单事务落位；slug 冲突拒绝（事务整体回滚——无成员行残留）。
func TestCreateTeamWithOwner(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	creator, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	team, err := st.CreateTeamWithOwner(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("CreateTeamWithOwner: %v", err)
	}
	m, err := st.GetMembership(ctx, team.ID, creator.ID)
	if err != nil || m.Role != TeamRoleOwner {
		t.Fatalf("creator membership: %v (%+v)", err, m)
	}
	// slug 冲突整体回滚：无半落位状态。
	if _, err := st.CreateTeamWithOwner(ctx, TeamWrite{Slug: "acme", Name: "dup", CreatedBy: creator.ID}); !errors.Is(err, ErrTeamSlugTaken) {
		t.Fatalf("duplicate slug err = %v, want ErrTeamSlugTaken", err)
	}
	members, err := st.ListMembers(ctx, team.ID)
	if err != nil || len(members) != 1 {
		t.Fatalf("rollback must leave no stray member rows: %v (n=%d)", err, len(members))
	}
}

// TestTeamUpdateAndDelete（v0.3 W2-S1，设计 §3.1）：UpdateTeam 仅显示名
// （slug 不可变——原语签名无 slug 入参）；DeleteTeam 项目非空守卫
//（ErrTeamNotEmpty）+ 成功路径同事务清成员/邀请 + 审计 team.deleted。
func TestTeamUpdateAndDelete(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	creator, err := st.CreateUser(ctx, UserWrite{Email: "owner@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	team, err := st.CreateTeamWithOwner(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("CreateTeamWithOwner: %v", err)
	}

	// 改名成功；slug 不动。
	updated, err := st.UpdateTeam(ctx, TeamUpdate{ID: team.ID, Name: "Acme Renamed", ActorUserID: creator.ID})
	if err != nil || updated.Name != "Acme Renamed" || updated.Slug != "acme" {
		t.Fatalf("UpdateTeam: %v (%+v)", err, updated)
	}
	if _, err := st.UpdateTeam(ctx, TeamUpdate{ID: "01MISSING000000000000000000", Name: "x"}); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("UpdateTeam missing err = %v, want ErrTeamNotFound", err)
	}
	if _, err := st.UpdateTeam(ctx, TeamUpdate{ID: team.ID, Name: " "}); err == nil {
		t.Fatal("empty name must be rejected at write channel")
	}

	// 项目非空守卫：项目行存在即拒（ErrTeamNotEmpty）；项目删光后放行。
	if _, err := st.CreateProject(ctx, ProjectWrite{TeamID: team.ID, Slug: "default", Name: "Default", ActorUserID: creator.ID}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.DeleteTeam(ctx, team.ID, creator.ID, ""); !errors.Is(err, ErrTeamNotEmpty) {
		t.Fatalf("DeleteTeam with project err = %v, want ErrTeamNotEmpty", err)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects: %v (n=%d)", err, len(projects))
	}
	if err := st.DeleteProject(ctx, projects[0].ID, creator.ID, ""); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// 成员/邀请随团队删除清场（同事务）。
	mate, err := st.CreateUser(ctx, UserWrite{Email: "mate@example.com", Password: "pw"})
	if err != nil {
		t.Fatalf("CreateUser mate: %v", err)
	}
	if _, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleDeveloper, "", ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, _, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "x@y.com", Role: TeamRoleViewer}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := st.DeleteTeam(ctx, team.ID, creator.ID, ""); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	if _, err := st.GetTeam(ctx, team.ID); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("GetTeam after delete err = %v, want ErrTeamNotFound", err)
	}
	if members, err := st.ListMembers(ctx, team.ID); err != nil || len(members) != 0 {
		t.Fatalf("members must be cleared with the team: %v (n=%d)", err, len(members))
	}
	if invites, err := st.ListInvites(ctx, team.ID); err != nil || len(invites) != 0 {
		t.Fatalf("invites must be cleared with the team: %v (n=%d)", err, len(invites))
	}

	// 审计 team.deleted 落档（事件面无 team.deleted——设计 §6 清单无此事件）。
	audits, err := st.RecentAudits(ctx, 20)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	var deleted bool
	for _, a := range audits {
		if a.Target == "team:"+team.ID && a.Action == "team.deleted" {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("audit team.deleted missing")
	}
}

// eventPayloads 收集指定事件名的 (subject, payload) 对（Outbox 事件断言面；
// 同 subject 多事件全保留——member_changed 系列同主体多次发出）。
func eventPayloads(t *testing.T, st *Store, name string) [][2]string {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 500)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	var out [][2]string
	for _, e := range evs {
		if e.Name == name {
			out = append(out, [2]string{e.Subject, e.Payload})
		}
	}
	return out
}

// payloadField 解出事件 payload JSON 的指定字段（解析失败 = 测试失败）。
func payloadField(t *testing.T, payload, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("payload %q is not JSON: %v", payload, err)
	}
	v, _ := m[key].(string)
	return v
}

// TestTeamEventOutbox（v0.3 W2-S1，设计 §6 事件清单 + Outbox 同事务）：
// team.created / team.member_changed（added/role_changed/removed 三形态）/
// invite.accepted 随业务写同事务落 events 表。
func TestTeamEventOutbox(t *testing.T) {
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

	team, err := st.CreateTeamWithOwner(ctx, TeamWrite{Slug: "acme", Name: "Acme", CreatedBy: owner.ID})
	if err != nil {
		t.Fatalf("CreateTeamWithOwner: %v", err)
	}
	created := eventPayloads(t, st, "team.created")
	if len(created) == 0 {
		t.Fatal("team.created event missing")
	}

	if _, err := st.AddMember(ctx, team.ID, mate.ID, TeamRoleDeveloper, owner.ID, ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := st.SetMemberRole(ctx, team.ID, mate.ID, TeamRoleViewer, owner.ID, ""); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	if err := st.RemoveMember(ctx, team.ID, mate.ID, owner.ID, ""); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	inv, token, err := st.CreateInvite(ctx, InviteWrite{TeamID: team.ID, Email: "x@y.com", Role: TeamRoleDeveloper, ActorUserID: owner.ID})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if _, err := st.ConsumeInvite(ctx, token, mate.ID, mate.ID, ""); err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}

	// team.member_changed 三形态（payload change 字段区分）。
	changed := eventPayloads(t, st, "team.member_changed")
	discriminants := map[string]bool{}
	for _, pair := range changed {
		if pair[0] != "team:"+team.ID {
			continue
		}
		switch payloadField(t, pair[1], "change") {
		case "added", "role_changed", "removed":
			discriminants[payloadField(t, pair[1], "change")] = true
		default:
			t.Fatalf("unexpected change discriminant in payload %q", pair[1])
		}
	}
	if !discriminants["added"] || !discriminants["role_changed"] || !discriminants["removed"] {
		t.Fatalf("team.member_changed must carry added/role_changed/removed: %v", discriminants)
	}

	accepted := eventPayloads(t, st, "invite.accepted")
	for _, pair := range accepted {
		if pair[0] == "invite:"+inv.ID {
			if role := payloadField(t, pair[1], "role"); role != TeamRoleDeveloper {
				t.Fatalf("invite.accepted role = %q, want developer", role)
			}
			return
		}
	}
	t.Fatalf("invite.accepted event missing (seen: %v)", accepted)
}
