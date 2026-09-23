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

// teams / team_members / team_invites 表读写（v0.3 W2 结构先行落库，RBAC
// 设计 §3.1）：团队与成员/邀请原语。
//
// 纪律（设计 §3.1）：
//   - slug/role 词表校验在本层写入通道（CHECK 词典是数据库侧双保险）；
//     slug 字符集（[a-z0-9]{2,32}）与保留字校验在上层（E_TEAM_SLUG_RESERVED）。
//   - owner 恒在位：最后一名 owner 不可被移除/降级（E_TEAM_LAST_OWNER），
//     守卫判定与写操作同事务（消除「检查与写分离」竞态窗，RevokeToken-
//     GuardLastAdmin 同型）。
//   - RemoveMember 联动清理该用户在该团队全部 projects 下的 project_members
//     覆写行（D-W0-2 队内覆写形的级联语义），同事务生效。
//   - 邀请一次性：token sha256 入库、7 天过期、可吊销；accept 原子落成员行
//     + 置 accepted_at；过期/已用/已吊销/查无此 token 统一 ErrInviteInvalid。
//
// 审计动作名取设计 §6 注册表：team.created / member_added /
// member_role_changed / member_removed / invite_created / invite_accepted /
// invite_revoked（与业务写同事务 fail-closed）。

// 团队角色词表（设计 §3.2 四档）。
const (
	TeamRoleOwner     = "owner"
	TeamRoleAdmin     = "admin"
	TeamRoleDeveloper = "developer"
	TeamRoleViewer    = "viewer"
)

// DefaultInviteTTL 是邀请链接有效期（设计 §3.1：7 天）。
const DefaultInviteTTL = 7 * 24 * time.Hour

// isValidTeamRole 报告 role 是否在四档词表内（本层写入通道校验，与
// team_members.role 的 CHECK 词典双保险）。
func isValidTeamRole(role string) bool {
	switch role {
	case TeamRoleOwner, TeamRoleAdmin, TeamRoleDeveloper, TeamRoleViewer:
		return true
	}
	return false
}

// Team 是一条团队行（只读投影）。
type Team struct {
	ID string
	// Slug 是单词制 [a-z0-9]{2,32} 标识（全局唯一、不可变；底座命名公式段）。
	Slug string
	// Name 是人读显示名（可改——改名不在本票面）。
	Name string
	// CreatedBy 是建队用户 ID。
	CreatedBy string
	CreatedAt time.Time
}

// TeamMember 是一条团队成员行（只读投影）。
type TeamMember struct {
	TeamID string
	UserID string
	// Role ∈ owner/admin/developer/viewer（四档，设计 §3.2）。
	Role      string
	CreatedAt time.Time
}

// TeamInvite 是一条邀请行（只读投影；token_hash 不在投影内——明文 token
// 只在 CreateInvite 响应一次性返回）。
type TeamInvite struct {
	ID     string
	TeamID string
	// Email 是被邀邮箱（小写归一）。
	Email string
	// Role 是受邀团队角色（accept 时落 team_members.role）。
	Role string
	// CreatedBy 是邀方用户 ID。
	CreatedBy string
	// ExpiresAt 是过期时刻（创建 + 7 天）。
	ExpiresAt time.Time
	CreatedAt time.Time
	// AcceptedAt / RevokedAt 零值 = 未消费/未吊销。
	AcceptedAt time.Time
	RevokedAt  time.Time
}

// team 相关哨兵错误。
var (
	// ErrTeamNotFound 表示目标团队不存在。
	ErrTeamNotFound = errors.New("team not found")
	// ErrTeamSlugTaken 表示 slug 已被占用（全局唯一）。
	ErrTeamSlugTaken = errors.New("team slug already taken")
	// ErrTeamMemberExists 表示该用户已是团队成员。
	ErrTeamMemberExists = errors.New("user is already a team member")
	// ErrTeamMemberNotFound 表示目标成员关系不存在。
	ErrTeamMemberNotFound = errors.New("team member not found")
	// ErrTeamLastOwner 是最后一名 owner 守卫（设计 §3.1：E_TEAM_LAST_OWNER
	// ——移除/降级最后一名 owner 会使团队失去责任人，事务内拒绝）。
	ErrTeamLastOwner = errors.New("last team owner")
	// ErrNotTeamMember 表示目标用户不是团队成员（project_members 写入前置
	// 校验，D-W0-2：覆写行仅限团队成员）。
	ErrNotTeamMember = errors.New("user is not a team member")
	// ErrInviteNotFound 表示目标邀请不存在。
	ErrInviteNotFound = errors.New("invite not found")
	// ErrInviteInvalid 表示邀请不可消费——查无此 token/已接受/已吊销/已
	// 过期统一同码（一次性凭据，不泄漏具体状态）。
	ErrInviteInvalid = errors.New("invite invalid")
)

// TeamWrite 是一次团队写入。
type TeamWrite struct {
	// ID 留空自动生成 ULID。
	ID string
	// Slug 单词制标识（字符集/保留字校验在上层；冲突 ErrTeamSlugTaken）。
	Slug string
	// Name 是人读显示名（非空必填）。
	Name string
	// CreatedBy 是建队用户 ID（非空必填）。
	CreatedBy string
	// ActorUserID 是发起写入的用户（审计 actor；空 = human）。
	ActorUserID string
	// ActorTokenID 是发起写入的调用方 PAT（可空）。
	ActorTokenID string
}

// CreateTeam 落一条团队行并与审计（team.created）同事务 fail-closed；
// slug 冲突返回 ErrTeamSlugTaken。
func (s *Store) CreateTeam(ctx context.Context, w TeamWrite) (Team, error) {
	if strings.TrimSpace(w.Slug) == "" {
		return Team{}, fmt.Errorf("state: create team: slug is empty")
	}
	if strings.TrimSpace(w.Name) == "" {
		return Team{}, fmt.Errorf("state: create team: name is empty")
	}
	if strings.TrimSpace(w.CreatedBy) == "" {
		return Team{}, fmt.Errorf("state: create team: created_by is empty")
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	var out Team
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		const q = `INSERT INTO teams (id, slug, name, created_by, created_at) VALUES (?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, w.Slug, w.Name, w.CreatedBy, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrTeamSlugTaken, w.Slug)
			}
			return fmt.Errorf("state: insert team: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(w.ActorUserID),
			ActorTokenID: w.ActorTokenID,
			Action:       "team.created",
			Target:       "team:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary("slug", w.Slug, "name", w.Name),
		}); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT id, slug, name, created_by, created_at FROM teams WHERE id = ?`, id)
		return scanTeam(row, &out)
	})
	if err != nil {
		return Team{}, err
	}
	return out, nil
}

// GetTeam 按 ID 取团队行；不存在返回 ErrTeamNotFound。
func (s *Store) GetTeam(ctx context.Context, id string) (Team, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, name, created_by, created_at FROM teams WHERE id = ?`, id)
	var t Team
	if err := scanTeam(row, &t); err != nil {
		return Team{}, err
	}
	return t, nil
}

// GetTeamBySlug 按 slug 取团队行（注册默认队 slug 归一冲突探测等）；不存在
// 返回 ErrTeamNotFound。
func (s *Store) GetTeamBySlug(ctx context.Context, slug string) (Team, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, slug, name, created_by, created_at FROM teams WHERE slug = ?`, slug)
	var t Team
	if err := scanTeam(row, &t); err != nil {
		return Team{}, err
	}
	return t, nil
}

// ListTeams 返回全部团队（created_at 升序；「我所在 + 平台全量」的过滤在
// 调用面按可见性做）。
func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, name, created_by, created_at FROM teams ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list teams: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Team
	for rows.Next() {
		var t Team
		if err := scanTeam(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate teams: %w", err)
	}
	return out, nil
}

// AddMember 落一条团队成员行并与审计（team.member_added）同事务
// fail-closed；重复加入返回 ErrTeamMemberExists；角色词表非法在本层拒绝
// （与 CHECK 词典双保险）。
func (s *Store) AddMember(ctx context.Context, teamID, userID, role, actorUserID, actorTokenID string) (TeamMember, error) {
	if !isValidTeamRole(role) {
		return TeamMember{}, fmt.Errorf("state: add member: invalid team role %q", role)
	}
	if teamID == "" || userID == "" {
		return TeamMember{}, fmt.Errorf("state: add member: team id and user id are required")
	}
	var out TeamMember
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		const q = `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, teamID, userID, role, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: user %s in team %s", ErrTeamMemberExists, userID, teamID)
			}
			return fmt.Errorf("state: insert team member: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "team.member_added",
			Target:       "team:" + teamID,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", userID, "role", role),
		}); err != nil {
			return err
		}
		return scanTeamMember(tx.QueryRowContext(ctx,
			`SELECT team_id, user_id, role, created_at FROM team_members WHERE team_id = ? AND user_id = ?`,
			teamID, userID), &out)
	})
	if err != nil {
		return TeamMember{}, err
	}
	return out, nil
}

// SetMemberRole 调整成员角色（幂等无守卫面：目标角色与现值相同 = 直接
// 成功）。降级最后一名 owner 被守卫拒绝（ErrTeamLastOwner，事务回滚）；
// 非成员返回 ErrTeamMemberNotFound。与审计（team.member_role_changed）
// 同事务 fail-closed。
func (s *Store) SetMemberRole(ctx context.Context, teamID, userID, role, actorUserID, actorTokenID string) (TeamMember, error) {
	if !isValidTeamRole(role) {
		return TeamMember{}, fmt.Errorf("state: set member role: invalid team role %q", role)
	}
	var out TeamMember
	err := s.InTx(ctx, func(tx *Tx) error {
		var current string
		err := tx.QueryRowContext(ctx,
			`SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&current)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTeamMemberNotFound
			}
			return fmt.Errorf("state: probe team member: %w", err)
		}
		if current == TeamRoleOwner && role != TeamRoleOwner {
			if err := guardLastOwner(ctx, tx, teamID, userID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE team_members SET role = ? WHERE team_id = ? AND user_id = ?`, role, teamID, userID); err != nil {
			return fmt.Errorf("state: update team member role: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "team.member_role_changed",
			Target:       "team:" + teamID,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", userID, "from", current, "to", role),
		}); err != nil {
			return err
		}
		return scanTeamMember(tx.QueryRowContext(ctx,
			`SELECT team_id, user_id, role, created_at FROM team_members WHERE team_id = ? AND user_id = ?`,
			teamID, userID), &out)
	})
	if err != nil {
		return TeamMember{}, err
	}
	return out, nil
}

// RemoveMember 移出成员（设计 §3.1 级联语义）：同事务删除成员行 + 该用户
// 在该团队全部 projects 下的 project_members 覆写行（D-W0-2：移出团队
// 联动清理）；移除最后一名 owner 被守卫拒绝；非成员返回
// ErrTeamMemberNotFound。与审计（team.member_removed）同事务 fail-closed。
func (s *Store) RemoveMember(ctx context.Context, teamID, userID, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		var role string
		err := tx.QueryRowContext(ctx,
			`SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID).Scan(&role)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTeamMemberNotFound
			}
			return fmt.Errorf("state: probe team member: %w", err)
		}
		if role == TeamRoleOwner {
			if err := guardLastOwner(ctx, tx, teamID, userID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID); err != nil {
			return fmt.Errorf("state: delete team member: %w", err)
		}
		// 队内覆写行联动清理（D-W0-2）：只清本团队项目下的覆写行，其他
		// 团队的成员关系与覆写行不动。
		res, err := tx.ExecContext(ctx, `DELETE FROM project_members
			WHERE user_id = ? AND project_id IN (SELECT id FROM projects WHERE team_id = ?)`, userID, teamID)
		if err != nil {
			return fmt.Errorf("state: purge project overrides on member removal: %w", err)
		}
		purged, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read override purge count: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "team.member_removed",
			Target:       "team:" + teamID,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", userID, "role", role, "project_overrides_removed", purged),
		})
	})
}

// ListMembers 返回团队全部成员（created_at 升序）。
func (s *Store) ListMembers(ctx context.Context, teamID string) ([]TeamMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id, user_id, role, created_at FROM team_members WHERE team_id = ? ORDER BY created_at ASC, user_id ASC`, teamID)
	if err != nil {
		return nil, fmt.Errorf("state: list team members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TeamMember
	for rows.Next() {
		var m TeamMember
		if err := scanTeamMember(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate team members: %w", err)
	}
	return out, nil
}

// GetMembership 取单条成员关系；不存在返回 ErrTeamMemberNotFound。
func (s *Store) GetMembership(ctx context.Context, teamID, userID string) (TeamMember, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT team_id, user_id, role, created_at FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)
	var m TeamMember
	if err := scanTeamMember(row, &m); err != nil {
		return TeamMember{}, err
	}
	return m, nil
}

// guardLastOwner 是最后一名 owner 守卫（设计 §3.1）：teamID 内除 userID 外
// 已无其他 owner → ErrTeamLastOwner。必须在持有写锁的事务内调用（与写操作
// 同事务，消除「检查与写分离」竞态窗）。
func guardLastOwner(ctx context.Context, tx *Tx, teamID, userID string) error {
	var owners int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM team_members WHERE team_id = ? AND role = ?`,
		teamID, TeamRoleOwner).Scan(&owners); err != nil {
		return fmt.Errorf("state: count team owners: %w", err)
	}
	if owners <= 1 {
		return ErrTeamLastOwner
	}
	return nil
}

// InviteWrite 是一次邀请写入。
type InviteWrite struct {
	// TeamID 是目标团队。
	TeamID string
	// Email 是被邀邮箱（小写归一在本层做）。
	Email string
	// Role 是受邀团队角色（四档词表；team_invites 无 CHECK——本层是唯一
	// 校验点，非法词表拒绝）。
	Role string
	// ActorUserID 是邀方用户（审计 actor；空 = human）。
	ActorUserID string
	// ActorTokenID 是发起写入的调用方 PAT（可空）。
	ActorTokenID string
}

// CreateInvite 落一条邀请行（token sha256 入库，7 天过期）并返回
// （行投影, 明文 token）——明文只出现这一次（无 SMTP：邀请链接页面直出，
// 设计 §3.1）。与审计（team.invite_created）同事务 fail-closed。
func (s *Store) CreateInvite(ctx context.Context, w InviteWrite) (TeamInvite, string, error) {
	if w.TeamID == "" {
		return TeamInvite{}, "", fmt.Errorf("state: create invite: team id is empty")
	}
	email, err := normalizeEmail(w.Email)
	if err != nil {
		return TeamInvite{}, "", fmt.Errorf("state: create invite: %w", err)
	}
	if !isValidTeamRole(w.Role) {
		return TeamInvite{}, "", fmt.Errorf("state: create invite: invalid team role %q", w.Role)
	}
	token, err := randomTokenHex()
	if err != nil {
		return TeamInvite{}, "", err
	}
	id := ulid.Make().String()
	var out TeamInvite
	err = s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		expires := time.Unix(0, now).UTC().Add(DefaultInviteTTL)
		const q = `INSERT INTO team_invites (id, team_id, email, role, token_hash, expires_at, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, w.TeamID, email, w.Role, HashToken(token), expires.UnixNano(),
			w.ActorUserID, now); err != nil {
			return fmt.Errorf("state: insert invite: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(w.ActorUserID),
			ActorTokenID: w.ActorTokenID,
			Action:       "team.invite_created",
			Target:       "invite:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary("team_id", w.TeamID, "email", email, "role", w.Role),
		}); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT id, team_id, email, role, expires_at, created_by, created_at, accepted_at, revoked_at
			FROM team_invites WHERE id = ?`, id)
		return scanInvite(row, &out)
	})
	if err != nil {
		return TeamInvite{}, "", err
	}
	return out, token, nil
}

// ListInvites 返回团队全部邀请行（created_at 升序，含已消费/已吊销——
// 管理面历史视图；展示过滤在调用面）。
func (s *Store) ListInvites(ctx context.Context, teamID string) ([]TeamInvite, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, team_id, email, role, expires_at, created_by, created_at, accepted_at, revoked_at
		FROM team_invites WHERE team_id = ? ORDER BY created_at ASC, id ASC`, teamID)
	if err != nil {
		return nil, fmt.Errorf("state: list invites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TeamInvite
	for rows.Next() {
		var inv TeamInvite
		if err := scanInvite(rows, &inv); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate invites: %w", err)
	}
	return out, nil
}

// RevokeInvite 吊销邀请（幂等面与 RevokeToken 同型：已吊销/已消费 =
// 幂等成功，不产生新状态；不存在返回 ErrInviteNotFound）。与审计
// （team.invite_revoked）同事务 fail-closed。
func (s *Store) RevokeInvite(ctx context.Context, inviteID, actorUserID, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE team_invites SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL AND accepted_at IS NULL`,
			nowNano(), inviteID)
		if err != nil {
			return fmt.Errorf("state: revoke invite: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read invite revoke count: %w", err)
		}
		if n == 0 {
			// 区分「已吊销/已消费（幂等成功）」与「不存在」。
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM team_invites WHERE id = ?`, inviteID).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrInviteNotFound
				}
				return fmt.Errorf("state: probe invite: %w", err)
			}
			return nil
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "team.invite_revoked",
			Target:       "invite:" + inviteID,
			Result:       "ok",
		})
	})
}

// ConsumeInvite 一次性 accept（设计 §3.1）：按明文 token 找行 → 校验未
// 消费/未吊销/未过期（任一不满足或查无此 token → ErrInviteInvalid，不泄漏
// 具体状态）→ 同事务落 team_members 行（已是成员则保持现有角色，不重复
// 插入不改角色）+ 置 accepted_at。与审计（team.invite_accepted）同事务
// fail-closed。
func (s *Store) ConsumeInvite(ctx context.Context, plaintextToken, userID, actorUserID, actorTokenID string) (TeamInvite, error) {
	if plaintextToken == "" || userID == "" {
		return TeamInvite{}, ErrInviteInvalid
	}
	hash := HashToken(plaintextToken)
	var out TeamInvite
	err := s.InTx(ctx, func(tx *Tx) error {
		var (
			inv        TeamInvite
			expires    int64
			created    int64
			acceptedAt sql.NullInt64
			revokedAt  sql.NullInt64
		)
		err := tx.QueryRowContext(ctx, `SELECT id, team_id, email, role, expires_at, created_by, created_at, accepted_at, revoked_at
			FROM team_invites WHERE token_hash = ?`, hash).
			Scan(&inv.ID, &inv.TeamID, &inv.Email, &inv.Role, &expires, &inv.CreatedBy, &created, &acceptedAt, &revokedAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInviteInvalid
			}
			return fmt.Errorf("state: scan invite: %w", err)
		}
		inv.CreatedAt = time.Unix(0, created).UTC()
		if acceptedAt.Valid || revokedAt.Valid || time.Unix(0, expires).UTC().Before(time.Now().UTC()) {
			return ErrInviteInvalid
		}
		// 已是成员：不重复插入、不改现有角色（invite 视为已消费）。
		now := nowNano()
		res, err := tx.ExecContext(ctx,
			`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (team_id, user_id) DO NOTHING`, inv.TeamID, userID, inv.Role, now)
		if err != nil {
			return fmt.Errorf("state: insert membership on invite accept: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if err := tx.WriteAudit(ctx, AuditEntry{
				Actor:        auditActor(actorUserID),
				ActorTokenID: actorTokenID,
				Action:       "team.member_added",
				Target:       "team:" + inv.TeamID,
				Result:       "ok",
				DiffSummary:  DiffSummary("user_id", userID, "role", inv.Role, "via", "invite:"+inv.ID),
			}); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE team_invites SET accepted_at = ? WHERE id = ?`, now, inv.ID); err != nil {
			return fmt.Errorf("state: mark invite accepted: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(actorUserID),
			ActorTokenID: actorTokenID,
			Action:       "team.invite_accepted",
			Target:       "invite:" + inv.ID,
			Result:       "ok",
			DiffSummary:  DiffSummary("team_id", inv.TeamID, "user_id", userID, "role", inv.Role),
		}); err != nil {
			return err
		}
		inv.ExpiresAt = time.Unix(0, expires).UTC()
		inv.AcceptedAt = time.Unix(0, now).UTC()
		out = inv
		return nil
	})
	if err != nil {
		return TeamInvite{}, err
	}
	return out, nil
}

// scanTeam 从单行构造 Team。
func scanTeam(row interface{ Scan(dest ...any) error }, t *Team) error {
	var created int64
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedBy, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamNotFound
		}
		return fmt.Errorf("state: scan team: %w", err)
	}
	t.CreatedAt = time.Unix(0, created).UTC()
	return nil
}

// scanTeamMember 从单行构造 TeamMember。
func scanTeamMember(row interface{ Scan(dest ...any) error }, m *TeamMember) error {
	var created int64
	if err := row.Scan(&m.TeamID, &m.UserID, &m.Role, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamMemberNotFound
		}
		return fmt.Errorf("state: scan team member: %w", err)
	}
	m.CreatedAt = time.Unix(0, created).UTC()
	return nil
}

// scanInvite 从单行构造 TeamInvite。
func scanInvite(row interface{ Scan(dest ...any) error }, inv *TeamInvite) error {
	var (
		expires    int64
		created    int64
		acceptedAt sql.NullInt64
		revokedAt  sql.NullInt64
	)
	if err := row.Scan(&inv.ID, &inv.TeamID, &inv.Email, &inv.Role, &expires, &inv.CreatedBy, &created, &acceptedAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInviteNotFound
		}
		return fmt.Errorf("state: scan invite: %w", err)
	}
	inv.ExpiresAt = time.Unix(0, expires).UTC()
	inv.CreatedAt = time.Unix(0, created).UTC()
	if acceptedAt.Valid {
		inv.AcceptedAt = time.Unix(0, acceptedAt.Int64).UTC()
	}
	if revokedAt.Valid {
		inv.RevokedAt = time.Unix(0, revokedAt.Int64).UTC()
	}
	return nil
}
