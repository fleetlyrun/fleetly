package state

// 自助注册组合原语（v0.3 W1，RBAC 设计 §2.1/§3.1）：注册 = 单写事务内的
// 全量落位——用户行（首用户强制平台管理员）+ 个人 Team（owner 一人）+ 默认
// Project `default` +（首用户）bootstrap token 自动吊销 + 审计与事件（Outbox）。
//
// 为什么是 state 层组合原语：设计 §2.1「同事务建个人 Team + 默认 Project」
// ——S1 的 CreateUser/CreateTeam/AddMember/CreateProject 各自开事务，无法
// 组合出原子注册；单写点纪律（新表写入全走 InTx）由本原语继承（INSERT 语句
// 与各单表原语逐列对齐）。
//
// 注册窗口（设计 §2.1/§10）：users 表为空 → 恒开（首注册者即平台管理员）；
// 非空 → platform_settings `auth.registration`（open|closed，缺省 closed）
// 管辖——开关读取与用户写入同事务，消除「并发首注册竞态」窗口。
//
// 邀请联动（设计 §3.1「未注册→注册即自动 accept」）不在本原语：W1 的
// Register RPC 无邀请 token 参数（api 面切分），accept 走独立的
// ConsumeInvite 原语（api/authservice.go）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// ErrRegistrationClosed 表示注册窗口关闭（users 非空 + auth.registration
// 缺省/显式 closed；设计 §2.1，api 面 E_REGISTRATION_CLOSED 的 state 哨兵）。
var ErrRegistrationClosed = errors.New("registration closed")

// DefaultProjectSlug（projects.go 导出常量）是注册默认项目 slug（设计
// §2.1/§3.1：无项目参数的首次 Deploy 缺省进它）——注册落位与缺省解析
// 同一值源。

// RegisterWrite 是一次自助注册（口令明文入参、argon2id 落库，明文不入
// 库/审计/日志——users 表同纪律）。
type RegisterWrite struct {
	// Email 小写归一在本层做。
	Email string
	// Password 是明文口令（非空必填）。
	Password string
	// DisplayName 留空取 email 本地部分。
	DisplayName string
}

// RegisterResult 是一次注册的落位投影（用户 + 个人队与角色 + 默认项目）。
type RegisterResult struct {
	User User
	Team Team
	// TeamRole 是注册用户在个人队的角色（恒 owner——设计 §3.1）。
	TeamRole string
	// Project 是默认项目 `default`。
	Project Project
}

// RegisterUser 落一笔原子注册；窗口关闭返回 ErrRegistrationClosed、email
// 冲突返回 ErrEmailTaken（哨兵映射在 api 面）。与审计（user.created /
// team.created / team.member_added / project.created / token.revoke〔首用户
// bootstrap 吊销时〕）和事件（user.registered / team.created /
// project.created，metadata-only）同事务 fail-closed。
func (s *Store) RegisterUser(ctx context.Context, w RegisterWrite) (RegisterResult, error) {
	email, err := normalizeEmail(w.Email)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("state: register user: %w", err)
	}
	if strings.TrimSpace(w.Password) == "" {
		return RegisterResult{}, fmt.Errorf("state: register user: password is empty")
	}
	hash, err := HashPassword(w.Password)
	if err != nil {
		return RegisterResult{}, fmt.Errorf("state: register user: %w", err)
	}
	display := strings.TrimSpace(w.DisplayName)
	if display == "" {
		display = email[:strings.IndexByte(email, '@')]
	}

	var out RegisterResult
	err = s.InTx(ctx, func(tx *Tx) error {
		// 窗口判定与写入同事务：首注册（users 空）恒开；其后由
		// auth.registration 管辖（缺省 closed，设计 §2.1）。
		var users int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM users`).Scan(&users); err != nil {
			return fmt.Errorf("state: count users: %w", err)
		}
		if users > 0 {
			settings, err := loadAuthSettingsFrom(ctx, tx.Tx)
			if err != nil {
				return err
			}
			if settings.Registration != AuthRegistrationOpen {
				return fmt.Errorf("%w (auth.registration=%s)", ErrRegistrationClosed, settings.Registration)
			}
		}
		isFirst := users == 0

		// 用户行（首用户强制 is_platform_admin=1——首注册者即平台管理员，
		// 与 CreateUser 写入通道同规则）+ 审计 user.created。审计 actor =
		// 注册者本人（设计 §6 actor 增维：用户操作 user:<id>）。
		now := nowNano()
		uid := ulid.Make().String()
		adminFlag := 0
		if isFirst {
			adminFlag = 1
		}
		const userQ = `INSERT INTO users (id, email, password_hash, display_name, is_platform_admin, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, userQ, uid, email, hash, display, adminFlag, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: %s", ErrEmailTaken, email)
			}
			return fmt.Errorf("state: insert user: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(uid),
			Action:       "user.created",
			Target:       "user:" + uid,
			Result:       "ok",
			DiffSummary:  DiffSummary("email", email, "is_platform_admin", isFirst, "via", "self-registration"),
		}); err != nil {
			return err
		}
		out.User = User{
			ID:              uid,
			Email:           email,
			DisplayName:     display,
			IsPlatformAdmin: isFirst,
			CreatedAt:       time.Unix(0, now).UTC(),
		}

		// 首用户：bootstrap token 同事务自动吊销（设计 §2.3/§10：零用户
		// 窗口的桥梁凭据，目的达成即死）。识别面 = BootstrapTokenName 精确
		// 匹配 + 机具令牌（user_id IS NULL）+ 在册；实际吊销的每一枚落审计
		//（系统自动动作必入审计，actor=system）。
		if isFirst {
			if _, err := revokeBootstrapTokensTx(ctx, tx); err != nil {
				return err
			}
		}

		// 个人 Team：slug 取 email 本地部分归一成单词（[a-z0-9]{2,32}，
		// 冲突加 2 起序号——设计 §3.1）；owner 一人 + 默认 Project `default`。
		slug, err := nextTeamSlugTx(ctx, tx, email)
		if err != nil {
			return err
		}
		tid := ulid.Make().String()
		const teamQ = `INSERT INTO teams (id, slug, name, created_by, created_at) VALUES (?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, teamQ, tid, slug, display, uid, now); err != nil {
			return fmt.Errorf("state: insert personal team: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(uid),
			Action:       "team.created",
			Target:       "team:" + tid,
			Result:       "ok",
			DiffSummary:  DiffSummary("slug", slug, "name", display),
		}); err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, Event{
			Name:    "team.created",
			Subject: "team:" + tid,
			Payload: DiffSummary("slug", slug, "name", display),
		}); err != nil {
			return err
		}
		out.Team = Team{ID: tid, Slug: slug, Name: display, CreatedBy: uid, CreatedAt: time.Unix(0, now).UTC()}

		const memberQ = `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, memberQ, tid, uid, TeamRoleOwner, now); err != nil {
			return fmt.Errorf("state: insert personal team owner: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(uid),
			Action:       "team.member_added",
			Target:       "team:" + tid,
			Result:       "ok",
			DiffSummary:  DiffSummary("user_id", uid, "role", TeamRoleOwner),
		}); err != nil {
			return err
		}
		out.TeamRole = TeamRoleOwner

		pid := ulid.Make().String()
		const projectQ = `INSERT INTO projects (id, team_id, slug, name, description, created_at) VALUES (?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, projectQ, pid, tid, DefaultProjectSlug, DefaultProjectSlug, "", now); err != nil {
			if isUniqueViolation(err) {
				// UNIQUE(team_id, slug)：新团队内不可能撞 `default`——防御
				// 式分支（理论不可达），loud-fail 不静默吞。
				return fmt.Errorf("state: insert default project: duplicate slug %s in new team", DefaultProjectSlug)
			}
			return fmt.Errorf("state: insert default project: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        auditActor(uid),
			Action:       "project.created",
			Target:       "project:" + pid,
			Result:       "ok",
			DiffSummary:  DiffSummary("team_id", tid, "slug", DefaultProjectSlug, "name", DefaultProjectSlug),
		}); err != nil {
			return err
		}
		if _, err := tx.AppendEvent(ctx, Event{
			Name:    "project.created",
			Subject: "project:" + pid,
			Payload: DiffSummary("team_id", tid, "slug", DefaultProjectSlug),
		}); err != nil {
			return err
		}
		out.Project = Project{ID: pid, TeamID: tid, Slug: DefaultProjectSlug, Name: DefaultProjectSlug, CreatedAt: time.Unix(0, now).UTC()}

		_, err = tx.AppendEvent(ctx, Event{
			Name:    "user.registered",
			Subject: "user:" + uid,
			Payload: DiffSummary("email", email, "is_platform_admin", isFirst, "team_id", tid, "team_slug", slug),
		})
		return err
	})
	if err != nil {
		return RegisterResult{}, err
	}
	return out, nil
}

// revokeBootstrapTokensTx 在事务内吊销全部在册 bootstrap token（首用户注册
// 路径专用）。返回实际吊销枚数。
func revokeBootstrapTokensTx(ctx context.Context, tx *Tx) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM tokens WHERE name = ? AND user_id IS NULL AND revoked_at IS NULL`,
		BootstrapTokenName)
	if err != nil {
		return 0, fmt.Errorf("state: scan bootstrap tokens: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("state: scan bootstrap token id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("state: iterate bootstrap tokens: %w", err)
	}
	_ = rows.Close()
	now := nowNano()
	for _, id := range ids {
		res, err := tx.ExecContext(ctx,
			`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
		if err != nil {
			return 0, fmt.Errorf("state: revoke bootstrap token: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("state: read bootstrap revoke count: %w", err)
		}
		if n == 0 {
			continue // 并发下已被吊销（幂等面）
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "token.revoke",
			Target: "token:" + id,
			Result: "ok",
			DiffSummary: DiffSummary("reason", "first_user_registration"),
		}); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// nextTeamSlugTx 在事务内为注册 email 归一个人队 slug：本地部分剥离分隔符
// 压成单词（[a-z0-9]，截断 32 位；不足 2 位回落 `user`——设计 §3.1 词表），
// 冲突加 2 起序号（slug2..slugN，序号可能使总长越界——再截断回 32）。
func nextTeamSlugTx(ctx context.Context, tx *Tx, email string) (string, error) {
	base := normalizeTeamSlug(email[:strings.IndexByte(email, '@')])
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			// 序号后缀可能使 slug 越过 32 位上界：截掉等长尾段再拼
			//（单词制与词表形态不破坏）。
			suffix := fmt.Sprintf("%d", n)
			candidate = candidate[:min(len(candidate), 32-len(suffix))] + suffix
		}
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM teams WHERE slug = ?`, candidate).Scan(&one)
		if err == nil {
			continue // 已被占用：试下一个序号
		}
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		return "", fmt.Errorf("state: probe team slug: %w", err)
	}
}

// normalizeTeamSlug 归一团队 slug 候选：小写、仅保留 [a-z0-9]（剥离
// `.`/`-`/`_` 等分隔符压成单词，设计 §3.1）、截断 32 位；空/不足 2 位回落
// `user`（[a-z0-9]{2,32} 下界）。
func normalizeTeamSlug(local string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(local) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 32 {
				break
			}
		}
	}
	slug := b.String()
	if len(slug) < 2 {
		return "user"
	}
	return slug
}
