// Package token 是 Token 聚合 repo（CONTEXT.md Token 词条：携带 Scope 的
// 凭证——Scope 经 role 间接持有；sha256 存储，明文只在创建响应出现一次）。
package token

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Token 是聚合行（SHA256 是明文的 hex 摘要查询键；LastUsedAt 空串=从未
// 使用）。
type Token struct {
	ID         string
	Name       string
	TeamID     string
	UserID     string
	RoleID     string
	SHA256     string
	Prefix     string
	Revoked    bool
	LastUsedAt string
	CreatedAt  string
}

// Repo 是 Token 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（UserID 空串落 NULL——无属主用户的 Token：Agent/CI/
// bootstrap）。
func (r *Repo) Create(ctx context.Context, run state.Runner, t *Token) error {
	t.CreatedAt = state.FormatTime(r.clock.Now())
	revoked := 0
	if t.Revoked {
		revoked = 1
	}
	var userID any
	if t.UserID != "" {
		userID = t.UserID
	}
	_, err := run.ExecContext(ctx, `
		INSERT INTO tokens (id, name, team_id, user_id, role_id, sha256, prefix, revoked, last_used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		t.ID, t.Name, t.TeamID, userID, t.RoleID, t.SHA256, t.Prefix, revoked, t.CreatedAt)
	return err
}

// GetBySHA256 按明文摘要查行（authn 拦截器的查询键）。
func (r *Repo) GetBySHA256(ctx context.Context, run state.Runner, sha string) (*Token, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens WHERE sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Token, error) {
	row := run.QueryRowContext(ctx, `
		SELECT id, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens WHERE id = ?`, id)
	return scanOne(row.Scan)
}

// HasAliveBootstrap 报告是否存在未吊销的 bootstrap Token（首启去重判定；
// journal 之外的库内权威锚）。
func (r *Repo) HasAliveBootstrap(ctx context.Context, run state.Runner) (bool, error) {
	var n int
	err := run.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens WHERE name = 'bootstrap' AND revoked = 0`).Scan(&n)
	return n > 0, err
}

// List 返回全部 Token（ID 序稳定；含已吊销——吊销状态是可见事实）。
func (r *Repo) List(ctx context.Context, run state.Runner) ([]Token, error) {
	rows, err := run.QueryContext(ctx, `
		SELECT id, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Token
	for rows.Next() {
		t, err := scanOne(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// Revoke 落吊销位（幂等；重复吊销返回当前行）。验收锚：吊销后进行中
// 请求的下一个调用即 401（authn 拦截器逐请求查表）。
func (r *Repo) Revoke(ctx context.Context, run state.Runner, id string) (*Token, error) {
	_, err := run.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, run, id)
}

// TouchLastUsed 记录最近使用时刻（authn 拦截器经内存节流调用——默认
// 60s 内同名 Token 不重复写库；空串首次必写）。
func (r *Repo) TouchLastUsed(ctx context.Context, run state.Runner, id string) error {
	_, err := run.ExecContext(ctx, `
		UPDATE tokens SET last_used_at = ? WHERE id = ?`,
		state.FormatTime(r.clock.Now()), id)
	return err
}

func scanOne(scan func(dest ...any) error) (*Token, error) {
	var t Token
	var revoked int
	if err := scan(&t.ID, &t.Name, &t.TeamID, &t.UserID, &t.RoleID, &t.SHA256, &t.Prefix,
		&revoked, &t.LastUsedAt, &t.CreatedAt); err != nil {
		return nil, state.MapScanErr(err)
	}
	t.Revoked = revoked != 0
	return &t, nil
}
