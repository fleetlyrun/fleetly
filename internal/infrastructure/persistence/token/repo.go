// Package token 是 Token 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/identity/token（ADR-0075 Repo 接口层；材质生成在 identity 种子
// 包），本包只持不导出实现与构造。
package token

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain token.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain token.Repo）。
func New(clock state.Clock, run state.Runner) token.Repo { return &repo{clock: clock, run: run} }

// Create 落一行（UserID 空串落 NULL——无属主用户的 Token：Agent/CI/
// bootstrap）。
func (r *repo) Create(ctx context.Context, t *token.Token) error {
	seq, err := persistence.NextSeq(ctx, r.run, "tokens")
	if err != nil {
		return err
	}
	t.Seq = seq
	if t.ID == "" {
		t.ID = entityid.Join("tok", seq)
	}
	t.CreatedAt = state.FormatTime(r.clock.Now())
	revoked := 0
	if t.Revoked {
		revoked = 1
	}
	var userID any
	if t.UserID != "" {
		userID = t.UserID
	}
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO tokens (id, seq, name, team_id, user_id, role_id, sha256, prefix, revoked, last_used_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		t.ID, t.Seq, t.Name, t.TeamID, userID, t.RoleID, t.SHA256, t.Prefix, revoked, t.CreatedAt)
	return err
}

// GetBySHA256 按明文摘要查行（authn 拦截器的查询键）。
func (r *repo) GetBySHA256(ctx context.Context, sha string) (*token.Token, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens WHERE sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*token.Token, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens WHERE id = ?`, id)
	return scanOne(row.Scan)
}

// HasAliveBootstrap 报告是否存在未吊销的 bootstrap Token（首启去重判定；
// journal 之外的库内权威锚）。
func (r *repo) HasAliveBootstrap(ctx context.Context) (bool, error) {
	var n int
	err := r.run.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tokens WHERE name = 'bootstrap' AND revoked = 0`).Scan(&n)
	return n > 0, err
}

// List 返回全部 Token（ID 序稳定；含已吊销——吊销状态是可见事实）。
func (r *repo) List(ctx context.Context) ([]token.Token, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, name, team_id, COALESCE(user_id, ''), role_id, sha256, prefix, revoked, last_used_at, created_at
		FROM tokens ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []token.Token
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
func (r *repo) Revoke(ctx context.Context, id string) (*token.Token, error) {
	_, err := r.run.ExecContext(ctx, `UPDATE tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, id)
}

// TouchLastUsed 记录最近使用时刻（authn 拦截器经内存节流调用——默认
// 60s 内同名 Token 不重复写库；空串首次必写）。
func (r *repo) TouchLastUsed(ctx context.Context, id string) error {
	_, err := r.run.ExecContext(ctx, `
		UPDATE tokens SET last_used_at = ? WHERE id = ?`,
		state.FormatTime(r.clock.Now()), id)
	return err
}

func scanOne(scan func(dest ...any) error) (*token.Token, error) {
	var t token.Token
	var revoked int
	if err := scan(&t.ID, &t.Seq, &t.Name, &t.TeamID, &t.UserID, &t.RoleID, &t.SHA256, &t.Prefix,
		&revoked, &t.LastUsedAt, &t.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	t.Revoked = revoked != 0
	return &t, nil
}
