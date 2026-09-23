package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// projects / project_members 表读写（v0.3 W2 结构先行落库，RBAC 设计
// §3.1/§3.3）：项目与队内覆写成员原语。
//
// 纪律（设计 §3.1/§3.3/§3.4）：
//   - slug 单词制、不可变（UpdateProject 仅改 name/description；唯一性 =
//     UNIQUE(team_id, slug)，同名项目跨团队允许，D-W0-9）。
//   - 删除守卫：项目内有存活资源（apps 非 deleted tombstone / db_instances
//     非 deleted 终态）即拒绝（ErrProjectNotEmpty）——资源先迁走或删光，
//     不做隐式级联（设计 §3.1 破坏性纪律；tombstone 行不阻塞——平台无
//     apps/db_instances 物理删除通道之外的清理，已删光的 tombstone 项目
//     必须可删）。
//   - project_members 队内覆写（D-W0-2 B 形）：role ∈ admin/developer/viewer；
//     写入校验目标用户已是该团队成员（ErrNotTeamMember）；owner 不可覆写
//     （ErrProjectOwnerOverride——owner 恒在全部项目保有 owner 权，覆写行
//     会破坏「先查覆写、无行走团队角色」的解析序）；upsert 语义（有行则改
//     角色）。移出团队的联动清理在 teams.go RemoveMember（同事务）。
//
// 审计动作名取设计 §6 注册表：project.created / deleted /
// member_role_changed / member_removed（与业务写同事务 fail-closed）。
// UpdateProject 不入审计（设计 §6 注册表无 project.updated 动作——动作
// 注册表只增不改，扩注册表属 W2 API 票裁决）。

// 项目角色词表（设计 §3.3：队内覆写三档，owner 不可覆写）。
const (
	ProjectRoleAdmin     = "admin"
	ProjectRoleDeveloper = "developer"
	ProjectRoleViewer    = "viewer"
)

// isValidProjectRole 报告 role 是否在覆写三档词表内。
func isValidProjectRole(role string) bool {
	switch role {
	case ProjectRoleAdmin, ProjectRoleDeveloper, ProjectRoleViewer:
		return true
	}
	return false
}

// Project 是一条项目行（只读投影）。
type Project struct {
	ID string
	// TeamID 是归属团队。
	TeamID string
	// Slug 是单词制标识（team 内唯一、不可变；底座命名公式三段第二位）。
	Slug string
	// Name 是人读显示名（可改）。
	Name string
	// Description 是人读描述（可改；'' = 无）。
	Description string
	CreatedAt   time.Time
}

// ProjectMember 是一条队内覆写成员行（只读投影；无行 = 用团队角色）。
type ProjectMember struct {
	ProjectID string
	UserID    string
	// Role ∈ admin/developer/viewer（覆写三档，设计 §3.3）。
	Role      string
	CreatedAt time.Time
}

// project 相关哨兵错误。
var (
	// ErrProjectNotFound 表示目标项目不存在。
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectSlugTaken 表示 slug 在该团队内已被占用（UNIQUE(team_id, slug)）。
	ErrProjectSlugTaken = errors.New("project slug already taken in team")
	// ErrProjectNotEmpty 表示项目内仍有存活资源（删除守卫，设计 §3.1：
	// 资源先迁走或删光，不做隐式级联）。
	ErrProjectNotEmpty = errors.New("project not empty")
	// ErrProjectMemberNotFound 表示目标覆写行不存在。
	ErrProjectMemberNotFound = errors.New("project member not found")
	// ErrProjectOwnerOverride 表示试图给团队 owner 建覆写行（设计 §3.3：
	// owner 不可覆写——owner 是团队级概念，恒在全部项目保有 owner 权）。
	ErrProjectOwnerOverride = errors.New("team owner cannot have a project role override")
)

// ProjectWrite 是一次项目写入。
type ProjectWrite struct {
	// ID 留空自动生成 ULID。
	ID string
	// TeamID 是归属团队（非空必填）。
	TeamID string
	// Slug 单词制标识（字符集校验在上层；team 内冲突 ErrProjectSlugTaken）。
	Slug string
	// Name 是人读显示名（非空必填）。
	Name string
	// Description 是人读描述（可空）。
	Description string
	// ActorUserID 是发起写入的用户（审计 actor；空 = human）。
	ActorUserID string
	// ActorTokenID 是发起写入的调用方 PAT（可空）。
	ActorTokenID string
}

// CreateProject 落一条项目行并与审计（project.created）同事务 fail-closed；
// team 内 slug 冲突返回 ErrProjectSlugTaken。
func (s *Store) CreateProject(ctx context.Context, w ProjectWrite) (Project, error) {
	if w.TeamID == "" {
		return Project{}, fmt.Errorf("state: create project: team id is empty")
	}
	if strings.TrimSpace(w.Slug) == "" {
		return Project{}, fmt.Errorf("state: create project: slug is empty")
	}
	if strings.TrimSpace(w.Name) == "" {
		return Project{}, fmt.Errorf("state: create project: name is empty")
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	var out Project
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		const q = `INSERT INTO projects (id, team_id, slug, name, description, created_at) VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, w.TeamID, w.Slug, w.Name, w.Description, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s in team %s", ErrProjectSlugTaken, w.Slug, w.TeamID)
			}
			return fmt.Errorf("state: insert project: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(w.ActorUserID),
			ActorTokenID: w.ActorTokenID,
			Action:       "project.created",
			Target:       "project:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary("team_id", w.TeamID, "slug", w.Slug, "name", w.Name),
		}); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx,
			`SELECT id, team_id, slug, name, description, created_at FROM projects WHERE id = ?`, id)
		return scanProject(row, &out)
	})
	if err != nil {
		return Project{}, err
	}
	return out, nil
}

// GetProject 按 ID 取项目行；不存在返回 ErrProjectNotFound。
func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, team_id, slug, name, description, created_at FROM projects WHERE id = ?`, id)
	var p Project
	if err := scanProject(row, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// ListProjects 返回全部项目（created_at 升序；可见性过滤在调用面——用户
// 可见项目集 / 平台全量，设计 §4.2）。
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, team_id, slug, name, description, created_at FROM projects ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Project
	for rows.Next() {
		var p Project
		if err := scanProject(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate projects: %w", err)
	}
	return out, nil
}

// ProjectUpdate 是一次项目更新（仅 name/description——slug 不可变，设计
// §3.1/§3.4）。
type ProjectUpdate struct {
	ID string
	// Name 是人读显示名（非空必填）。
	Name string
	// Description 是人读描述（可空）。
	Description string
	// ActorUserID 是发起写入的用户（保留字段：设计 §6 注册表暂无
	// project.updated 动作，本原语暂不入审计）。
	ActorUserID string
}

// UpdateProject 更新显示名与描述（slug 不可变）；不存在返回
// ErrProjectNotFound。
func (s *Store) UpdateProject(ctx context.Context, u ProjectUpdate) (Project, error) {
	if strings.TrimSpace(u.Name) == "" {
		return Project{}, fmt.Errorf("state: update project: name is empty")
	}
	var out Project
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE projects SET name = ?, description = ? WHERE id = ?`, u.Name, u.Description, u.ID)
		if err != nil {
			return fmt.Errorf("state: update project: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read project update count: %w", err)
		}
		if n == 0 {
			return ErrProjectNotFound
		}
		row := tx.QueryRowContext(ctx,
			`SELECT id, team_id, slug, name, description, created_at FROM projects WHERE id = ?`, u.ID)
		return scanProject(row, &out)
	})
	if err != nil {
		return Project{}, err
	}
	return out, nil
}

// DeleteProject 删除空项目：项目内有存活资源（apps 非 deleted tombstone /
// db_instances 非 deleted 终态）即拒绝 ErrProjectNotEmpty（不做隐式级联，
// 设计 §3.1）；删除时同事务清掉该项目的覆写成员行。tombstone/终态行不
// 阻塞（平台无 apps/db_instances 物理删除通道——按 tombstone-first 纪律
// 已删光的资源不应永久锁死项目删除）。与审计（project.deleted）同事务
// fail-closed。
func (s *Store) DeleteProject(ctx context.Context, id, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrProjectNotFound
			}
			return fmt.Errorf("state: probe project: %w", err)
		}
		var apps int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM apps WHERE project_id = ? AND lifecycle != 'deleted'`, id).Scan(&apps); err != nil {
			return fmt.Errorf("state: count project apps: %w", err)
		}
		var dbs int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM db_instances WHERE project_id = ? AND state != 'deleted'`, id).Scan(&dbs); err != nil {
			return fmt.Errorf("state: count project databases: %w", err)
		}
		if apps > 0 || dbs > 0 {
			return fmt.Errorf("%w: %d live app(s), %d live database(s)", ErrProjectNotEmpty, apps, dbs)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM project_members WHERE project_id = ?`, id); err != nil {
			return fmt.Errorf("state: purge project members: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id); err != nil {
			return fmt.Errorf("state: delete project: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "project.deleted",
			Target:       "project:" + id,
			Result:       "ok",
		})
	})
}

// SetProjectMemberRole 队内覆写 upsert（设计 §3.3）：目标用户已是团队成员
// 才可写（否则 ErrNotTeamMember）；团队 owner 不可被覆写（ErrProjectOwner-
// Override）；有行则改角色、无行则插入。与审计
// （project.member_role_changed）同事务 fail-closed。
func (s *Store) SetProjectMemberRole(ctx context.Context, projectID, userID, role, actorUserID, actorTokenID string) (ProjectMember, error) {
	if !isValidProjectRole(role) {
		return ProjectMember{}, fmt.Errorf("state: set project member role: invalid project role %q", role)
	}
	if projectID == "" || userID == "" {
		return ProjectMember{}, fmt.Errorf("state: set project member role: project id and user id are required")
	}
	var out ProjectMember
	err := s.InTx(ctx, func(tx *Tx) error {
		var teamID string
		err := tx.QueryRowContext(ctx, `SELECT team_id FROM projects WHERE id = ?`, projectID).Scan(&teamID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrProjectNotFound
			}
			return fmt.Errorf("state: probe project: %w", err)
		}
		// owner 不可覆写（设计 §3.3）：覆写行会破坏「先查覆写、无行走
		// 团队角色」的解析序，恒拒绝。
		var isOwner int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ? AND role = ?`,
			teamID, userID, TeamRoleOwner).Scan(&isOwner); err == nil {
			return ErrProjectOwnerOverride
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("state: probe team owner: %w", err)
		}
		// 仅限团队成员（D-W0-2）：无成员行即拒（ErrNotTeamMember）。
		var memberOne int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&memberOne); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotTeamMember
			}
			return fmt.Errorf("state: probe team member: %w", err)
		}
		now := nowNano()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_members (project_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = excluded.role`,
			projectID, userID, role, now); err != nil {
			return fmt.Errorf("state: upsert project member: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "project.member_role_changed",
			Target:       "project:" + projectID,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", userID, "role", role),
		}); err != nil {
			return err
		}
		return scanProjectMember(tx.QueryRowContext(ctx,
			`SELECT project_id, user_id, role, created_at FROM project_members WHERE project_id = ? AND user_id = ?`,
			projectID, userID), &out)
	})
	if err != nil {
		return ProjectMember{}, err
	}
	return out, nil
}

// RemoveProjectMember 删除一条覆写行（无行 = 用团队角色，删除即回退）；
// 不存在返回 ErrProjectMemberNotFound。与审计（project.member_removed）
// 同事务 fail-closed。
func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
		if err != nil {
			return fmt.Errorf("state: remove project member: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read remove count: %w", err)
		}
		if n == 0 {
			return ErrProjectMemberNotFound
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "project.member_removed",
			Target:       "project:" + projectID,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", userID),
		})
	})
}

// ListProjectMembers 返回项目全部覆写行（created_at 升序）。
func (s *Store) ListProjectMembers(ctx context.Context, projectID string) ([]ProjectMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, user_id, role, created_at FROM project_members WHERE project_id = ? ORDER BY created_at ASC, user_id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("state: list project members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ProjectMember
	for rows.Next() {
		var m ProjectMember
		if err := scanProjectMember(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate project members: %w", err)
	}
	return out, nil
}

// scanProject 从单行构造 Project。
func scanProject(row interface{ Scan(dest ...any) error }, p *Project) error {
	var created int64
	if err := row.Scan(&p.ID, &p.TeamID, &p.Slug, &p.Name, &p.Description, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProjectNotFound
		}
		return fmt.Errorf("state: scan project: %w", err)
	}
	p.CreatedAt = time.Unix(0, created).UTC()
	return nil
}

// scanProjectMember 从单行构造 ProjectMember。
func scanProjectMember(row interface{ Scan(dest ...any) error }, m *ProjectMember) error {
	var created int64
	if err := row.Scan(&m.ProjectID, &m.UserID, &m.Role, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProjectMemberNotFound
		}
		return fmt.Errorf("state: scan project member: %w", err)
	}
	m.CreatedAt = time.Unix(0, created).UTC()
	return nil
}
