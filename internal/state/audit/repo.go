// Package audit 是审计 repo（架构 §6：Token/人/Agent 的一切写操作留痕；
// 来源枚举学 zane-ops，外加 platform 自治动作的 system 来源）。完整执法
// （操作者解析/Scope 绑定）随账号批接入；写路径四件一拍自本批起落行。
package audit

import (
	"context"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// Source 是写操作触发来源（只增枚举；F0.7 验收的来源过滤消费本列）。
type Source string

const (
	SourceManual   Source = "manual"   // 人工直接操作
	SourceAPI      Source = "api"      // API 直调（非 CLI）
	SourceCLI      Source = "cli"      // CLI（Agent 与人类共用面）
	SourceWebhook  Source = "webhook"  // Git webhook 触发
	SourceSchedule Source = "schedule" // Schedule 触发
	SourceSystem   Source = "system"   // 平台自治动作（看门狗/自动回滚/收口）
)

// Entry 是一条审计记录（前后值指纹：敏感值不进审计，只留指纹）。
type Entry struct {
	ID        string
	Actor     string // 操作者（Token 名/用户名；system 动作为空）
	Source    Source
	Action    string // 如 "project.create"
	Resource  string // 如 "project/<id>"
	BeforeFP  string // 变更前指纹（创建为空）
	AfterFP   string // 变更后指纹（删除为空）
	CreatedAt string
}

// Repo 是审计存取（只增）。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Append 落一条审计（四件一拍的"审计"件）。
func (r *Repo) Append(ctx context.Context, run state.Runner, e *Entry) error {
	e.CreatedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO audit (id, actor, source, action, resource, before_fp, after_fp, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Actor, string(e.Source), e.Action, e.Resource, e.BeforeFP, e.AfterFP, e.CreatedAt)
	return err
}

// List 返回审计（新→旧；limit 上界钳制）。
func (r *Repo) List(ctx context.Context, run state.Runner, limit int) ([]Entry, error) {
	return r.ListFiltered(ctx, run, Filter{Limit: limit})
}

// Filter 是审计查询面（F0.7）：actor/resource 精确、action 前缀、source
// 枚举过滤；全部可空。
type Filter struct {
	Actor        string
	Source       string
	ActionPrefix string
	Resource     string
	Limit        int
}

// ListFiltered 按过滤返回审计（新→旧；limit 上界钳制；条件动态拼接但
// 占位符参数化——无字符串拼接 SQL）。
func (r *Repo) ListFiltered(ctx context.Context, run state.Runner, f Filter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 1000
	}
	query := `SELECT id, actor, source, action, resource, before_fp, after_fp, created_at FROM audit`
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
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := run.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Entry
	for rows.Next() {
		var e Entry
		var src string
		if err := rows.Scan(&e.ID, &e.Actor, &src, &e.Action, &e.Resource, &e.BeforeFP, &e.AfterFP, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Source = Source(src)
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
