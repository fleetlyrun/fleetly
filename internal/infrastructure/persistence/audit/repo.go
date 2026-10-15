// Package audit 是审计聚合 repo 的 SQLite 实现：行类型、来源词表与 Repo
// 接口住 domain/identity/audit（ADR-0075 Repo 接口层——归属裁决见该包
// 注记），本包只持不导出实现与构造。
package audit

import (
	"context"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain audit.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain audit.Repo）。
func New(clock state.Clock) audit.Repo { return &repo{clock: clock} }

// Append 落一条审计（四件一拍的"审计"件）。Team 轴统一铸入点：Entry 未
// 显式携带时从 ctx 取（ADR-0035——全部调用方（api/engine/webhook）零改动
// 获得 Team 标注；system/无身份动作显式留空 = 平台级行）。ID 由行装配处
// 装配 aud-<seq>（ADR-0070——调用方不再自带 id）。
func (r *repo) Append(ctx context.Context, run state.Runner, e *audit.Entry) error {
	if e.TeamID == "" {
		e.TeamID = audit.TeamFromContext(ctx)
	}
	e.CreatedAt = state.FormatTime(r.clock.Now())
	seq, err := persistence.NextSeq(ctx, run, "audit")
	if err != nil {
		return err
	}
	e.Seq = seq
	e.ID = entityid.Audit(seq)
	_, err = run.ExecContext(ctx, `
		INSERT INTO audit (id, seq, actor, source, action, resource, before_fp, after_fp, team_id, created_at, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Seq, e.Actor, string(e.Source), e.Action, e.Resource, e.BeforeFP, e.AfterFP, e.TeamID, e.CreatedAt, e.Detail)
	return err
}

// List 返回审计（新→旧；limit 上界钳制）。
func (r *repo) List(ctx context.Context, run state.Runner, limit int) ([]audit.Entry, error) {
	return r.ListFiltered(ctx, run, audit.Filter{Limit: limit})
}

// ListFiltered 按过滤返回审计（新→旧；limit 上界钳制；条件动态拼接但
// 占位符参数化——无字符串拼接 SQL）。
func (r *repo) ListFiltered(ctx context.Context, run state.Runner, f audit.Filter) ([]audit.Entry, error) {
	f.Limit = persistence.ClampLimit(f.Limit, 1000, 1000)
	query := `SELECT id, seq, actor, source, action, resource, before_fp, after_fp, team_id, created_at, detail FROM audit`
	var conds []string
	var args []any
	if f.Actor != "" {
		conds = append(conds, "actor = ?")
		args = append(args, f.Actor)
	}
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.ActionPrefix != "" {
		conds = append(conds, "action LIKE ? ESCAPE '\\'")
		args = append(args, likePrefix(f.ActionPrefix))
	}
	if f.Resource != "" {
		conds = append(conds, "resource = ?")
		args = append(args, f.Resource)
	}
	if f.Team != "" {
		conds = append(conds, "(team_id = ? OR team_id = '')")
		args = append(args, f.Team)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY seq DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []audit.Entry
	for rows.Next() {
		var e audit.Entry
		var src string
		if err := rows.Scan(&e.ID, &e.Seq, &e.Actor, &src, &e.Action, &e.Resource, &e.BeforeFP, &e.AfterFP, &e.TeamID, &e.CreatedAt, &e.Detail); err != nil {
			return nil, err
		}
		e.Source = audit.Source(src)
		out = append(out, e)
	}
	return out, rows.Err()
}

// likePrefix 把动作前缀转为 LIKE 模式（% _ 转义——动作名是受控词表，
// 转义是纵深防御）。
func likePrefix(p string) string {
	escaped := strings.ReplaceAll(p, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "%", "\\%")
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	return escaped + "%"
}
