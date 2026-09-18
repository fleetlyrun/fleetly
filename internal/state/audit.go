package state

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// 审计日志（state-model §2.9）：actor（human/ai_agent/system + token）、
// action、target、result、error_code、request_id、diff 摘要。破坏性/管理
// 操作与业务写同事务、审计失败即操作失败（fail-closed）——调用方在
// Store.InTx 内组合 Tx.WriteAudit 与业务写即可；系统自动动作必入审计。
// secret 值禁止进入审计（state-model §2.9 末条）。

// AuditEntry 是一条审计记录。ID 可留空（自动生成 ULID）；Actor/Action/
// Result 为数据库级非空约束（audit_log 表 CHECK），写不出合法行即整事务
// 回滚——这是 fail-closed 的硬保证，包内不做宽松兜底。
type AuditEntry struct {
	// ID 记录 ID（ULID；留空自动生成）。
	ID string
	// At 发生时间（零值取写入时刻）。
	At time.Time
	// Actor 操作主体：human / ai_agent / system（架构 §1.1 术语约定）。
	Actor string
	// ActorTokenID 关联 token（可空）。
	ActorTokenID string
	// Action 稳定动作串（如 app.delete、node.identity_created）。
	Action string
	// Target 操作对象（如 app:<id>、node:<platform_id>）。
	Target string
	// Result 取 ok / error。
	Result string
	// ErrorCode 失败时的注册表错误码（可空）。
	ErrorCode string
	// RequestID 关联请求（可空）。
	RequestID string
	// DiffSummary diff 摘要（脱敏后的 JSON/文本，可空）。
	DiffSummary string
}

// TxWriteAudit 是（事务内）审计写入的错误前缀。
const auditWritePrefix = "state: write audit"

// WriteAudit 在事务内追加审计记录：与同事务内的业务写原子生效或原子
// 回滚。表级 CHECK（actor/action 非空、result 枚举）使非法审计行直接
// 产生数据库错误，调用方不得吞掉——这是 fail-closed 的验证路径
// （audit_test.go 用真实约束违例证明业务写随之回滚）。
func (t *Tx) WriteAudit(ctx context.Context, e AuditEntry) error {
	id := e.ID
	if id == "" {
		id = ulid.Make().String()
	}
	at := e.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	const q = `INSERT INTO audit_log
		(id, at, actor, actor_token_id, action, target, result, error_code, request_id, diff_summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := t.ExecContext(ctx, q,
		id, at.UnixNano(), e.Actor, e.ActorTokenID, e.Action, e.Target, e.Result,
		e.ErrorCode, e.RequestID, e.DiffSummary)
	if err != nil {
		return fmt.Errorf("%s: %w", auditWritePrefix, err)
	}
	return nil
}

// AuditRecord 是审计只读投影（验证/后台展示用，v0.1 最小面）。
type AuditRecord struct {
	ID     string
	At     time.Time
	Actor  string
	Action string
	Target string
	Result string
}

// RecentAudits 按 at 倒序返回最近 n 条审计记录。
func (s *Store) RecentAudits(ctx context.Context, n int) ([]AuditRecord, error) {
	const q = `SELECT id, at, actor, action, target, result
		FROM audit_log ORDER BY at DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, n)
	if err != nil {
		return nil, fmt.Errorf("state: query audits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		var atNano int64
		if err := rows.Scan(&r.ID, &atNano, &r.Actor, &r.Action, &r.Target, &r.Result); err != nil {
			return nil, fmt.Errorf("state: scan audit: %w", err)
		}
		r.At = time.Unix(0, atNano).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate audits: %w", err)
	}
	return out, nil
}
