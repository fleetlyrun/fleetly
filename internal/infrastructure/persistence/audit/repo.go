// Package audit 是审计聚合 repo 的 SQLite 实现：行类型、来源词表与 Repo
// 接口住 domain/identity/audit（ADR-0075 Repo 接口层——归属裁决见该包
// 注记），本包只持不导出实现与构造。
package audit

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// auditAppendAttempts 是非事务 Append 的有界尝试次数（首次 + 2 次唯一冲突
// 重试）。唯一冲突是"另一写者刚铸走同一 seq"的信号，重取 MAX+1 即可；其
// 他错误不重试（连接/约束/语法错误重试只会延迟失败）。
const auditAppendAttempts = 3

// nonTxAppendMu 串行化"非事务执行面"的 seq 铸造 + 插入（R05-M1）。SQLite
// 连接池收口为 1，但 NextSeq 与 INSERT 是两条自动提交语句，并发调用可在
// 两语句之间交错而双铸同一 seq（32 goroutine × 8 次复现 205/256 失败）。
// 非事务写者（authn 收窄审计 / engine converge 事实）在进程内共享本锁，
// 把读-改-写收成一个临界区；有界重试则兜住跨进程/未来连接池放宽的残余
// 竞争。
//
// 事务执行面（*sql.Tx）**不取锁**：单连接事务内的 NextSeq+INSERT 已天然
// 串行；若事务路径也抢本锁，会出现"事务持连接等锁 / 非事务持锁等连接"的
// 死锁。故仅在 run 不是 *sql.Tx 时加锁。
var nonTxAppendMu sync.Mutex

// repo 是 domain audit.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain audit.Repo）。
func New(clock state.Clock, run state.Runner) audit.Repo { return &repo{clock: clock, run: run} }

// Append 落一条审计（四件一拍的"审计"件）。Team 轴统一铸入点：Entry 未
// 显式携带时从 ctx 取（ADR-0035——全部调用方（api/engine/webhook）零改动
// 获得 Team 标注；system/无身份动作显式留空 = 平台级行）。ID 由行装配处
// 装配 aud-<seq>（ADR-0070——调用方不再自带 id）。
//
// 并发面（R05-M1）：非事务执行面先取 nonTxAppendMu 串行化 NextSeq+INSERT，
// 唯一冲突（跨进程残余竞争）再按 auditAppendAttempts 有界重试——仅唯一
// 冲突重取 seq 重试，其他错误立即返回；每次尝试重铸 e.Seq/e.ID（CreatedAt
// 在受理时刻取一次，重试不推进事实落账时间）。
func (r *repo) Append(ctx context.Context, e *audit.Entry) error {
	if e.TeamID == "" {
		e.TeamID = audit.TeamFromContext(ctx)
	}
	e.CreatedAt = state.FormatTime(r.clock.Now())

	if _, inTx := r.run.(*sql.Tx); !inTx {
		nonTxAppendMu.Lock()
		defer nonTxAppendMu.Unlock()
	}

	var lastErr error
	for attempt := 0; attempt < auditAppendAttempts; attempt++ {
		seq, err := persistence.NextSeq(ctx, r.run, "audit")
		if err != nil {
			return err
		}
		e.Seq = seq
		e.ID = entityid.Audit(seq)
		_, err = r.run.ExecContext(ctx, `
		INSERT INTO audit (id, seq, actor, source, action, resource, before_fp, after_fp, team_id, created_at, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.Seq, e.Actor, string(e.Source), e.Action, e.Resource, e.BeforeFP, e.AfterFP, e.TeamID, e.CreatedAt, e.Detail)
		if err == nil {
			return nil
		}
		if !persistence.IsUniqueViolation(err) {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// List 返回审计（新→旧；limit 上界钳制）。
func (r *repo) List(ctx context.Context, limit int) ([]audit.Entry, error) {
	return r.ListFiltered(ctx, audit.Filter{Limit: limit})
}

// ListFiltered 按过滤返回审计（新→旧；limit 上界钳制；条件动态拼接但
// 占位符参数化——无字符串拼接 SQL）。
func (r *repo) ListFiltered(ctx context.Context, f audit.Filter) ([]audit.Entry, error) {
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

	rows, err := r.run.QueryContext(ctx, query, args...)
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
