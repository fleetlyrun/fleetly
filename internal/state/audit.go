package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// DiffSummary 构造审计 diff 摘要 JSON 字符串（B4：审计摘要的唯一构造器
// ——内部 json.Marshal，键值含 `"`、`\`、控制字符时按 JSON 规则转义，
// 消灭手拼 JSON 的注入与破包面）。kvs 为 key,value 成对变参（key 取
// string；value 支持 string/数值/布尔等可 marshal 类型）；奇数个尾参
// 丢弃末项（防御式——调用方均为固定键值对）。marshal 失败（理论不可达）
// 回落 "{}"。map 序列化键序确定（字典序）。
func DiffSummary(kvs ...any) string {
	if len(kvs)%2 != 0 {
		kvs = kvs[:len(kvs)-1]
	}
	m := make(map[string]any, len(kvs)/2)
	for i := 0; i < len(kvs); i += 2 {
		key, _ := kvs[i].(string)
		m[key] = kvs[i+1]
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

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

// AuditRecord 是审计只读投影（验证/后台展示用）。T2.15 起随读通道披露
// ErrorCode/DiffSummary（路由发布失败审计的断言面；加法扩展）。v0.3 W3-S1
// （rbac-teams §6 D-W0-6 读面）加法补 RequestID（审计行原列——写侧一直落
// 库，此前无读面消费；既有消费方零感知）。
type AuditRecord struct {
	ID     string
	At     time.Time
	Actor  string
	Action string
	Target string
	Result string
	// ErrorCode 是失败路径的注册表错误码（'' = 成功行）。
	ErrorCode string
	// RequestID 是关联请求 ID（'' = 无）。
	RequestID string
	// DiffSummary 是审计差异摘要（'' = 无）。
	DiffSummary string
}

// RecentAudits 按 at 倒序返回最近 n 条审计记录。
func (s *Store) RecentAudits(ctx context.Context, n int) ([]AuditRecord, error) {
	const q = `SELECT id, at, actor, action, target, result, error_code, request_id, diff_summary
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
		if err := rows.Scan(&r.ID, &atNano, &r.Actor, &r.Action, &r.Target, &r.Result,
			&r.ErrorCode, &r.RequestID, &r.DiffSummary); err != nil {
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

// ListAudits 的分页边界（W3-S1 D-W0-6 读面）：零/负 Limit 回落缺省页大小；
// 超上限按上限截断（导出侧分页循环取大页即可，单页物化行数有常数上界）。
const (
	DefaultAuditListLimit = 100
	MaxAuditListLimit     = 1000
)

// AuditQuery 是审计查询的过滤集与分页参数（零值字段 = 该维不过滤）。
// 过滤语义（W3-S1 票面裁决，api/CLI 读面同源）：
//
//   - Actor   子串包含（actor 形如 human / system / user:<id>——按 email
//     检索操作者之外的整体串检索同样命中，包含语义对两种用法兼容）；
//   - Action  前缀匹配（audit.* / api.* / auth.* 动作族的族级过滤）；
//   - Result  精确匹配（ok | error）；
//   - Target  子串包含（target 形如 app:<id> / user:<id>）；
//   - Since/Until 时间窗（闭区间：at >= since AND at <= until，两边界均含；
//     零值 = 不限）；
//   - Limit/Offset 分页（Limit 非正 → DefaultAuditListLimit；上限
//     MaxAuditListLimit；Offset 非负）。
type AuditQuery struct {
	Actor  string
	Action string
	Result string
	Target string
	Since  time.Time
	Until  time.Time
	Limit  int
	Offset int
}

// ListAudits 按过滤集查询审计（at 倒序、同刻按 id 倒序稳定排序），返回
// 当页记录与**全量命中计数**（过滤生效、分页生效前的 total——读面分页器
// 的总数投影）。v0.3 W3-S1 D-W0-6：CLI `audit list/export` 与 Console 审计
// 页（W3-S3）的共同查询原语。
func (s *Store) ListAudits(ctx context.Context, q AuditQuery) ([]AuditRecord, int, error) {
	where, args := auditQueryWhere(q)
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultAuditListLimit
	}
	if limit > MaxAuditListLimit {
		limit = MaxAuditListLimit
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	// total 先行（同 WHERE）——分页读之前拿到全量命中数。
	var total int
	countQ := "SELECT count(*) FROM audit_log" + where //nolint:gosec // G202：WHERE 为包内常量片段拼接（auditQueryWhere），全部值经 ? 参数绑定
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("state: count audits: %w", err)
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	pageQ := "SELECT id, at, actor, action, target, result, error_code, request_id, diff_summary FROM audit_log" + where + " ORDER BY at DESC, id DESC LIMIT ? OFFSET ?" //nolint:gosec // G202：同上——常量片段 + 参数绑定
	rows, err := s.db.QueryContext(ctx, pageQ, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("state: query audits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []AuditRecord
	for rows.Next() {
		var r AuditRecord
		var atNano int64
		if err := rows.Scan(&r.ID, &atNano, &r.Actor, &r.Action, &r.Target, &r.Result,
			&r.ErrorCode, &r.RequestID, &r.DiffSummary); err != nil {
			return nil, 0, fmt.Errorf("state: scan audit: %w", err)
		}
		r.At = time.Unix(0, atNano).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("state: iterate audits: %w", err)
	}
	return out, total, nil
}

// escapeLikePattern 把 LIKE 模式元字符（% _ \）转义为字面量（配 ESCAPE
// '\\' 使用）——过滤词来自 API/CLI 外部输入，元字符按字面匹配而非通配。
func escapeLikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// auditQueryWhere 由过滤集拼 WHERE 子句与绑定参数（ListAudits 的单点——
// count 与 page 两查询共用同一 WHERE，语义不可能漂移）。恒返回以 " WHERE"
// 或空串开头、参数与占位符同序的形态。
func auditQueryWhere(q AuditQuery) (string, []any) {
	clauses := []string{}
	var args []any
	if q.Actor != "" {
		clauses = append(clauses, "instr(actor, ?) > 0")
		args = append(args, q.Actor)
	}
	if q.Action != "" {
		clauses = append(clauses, "action LIKE ? ESCAPE '\\'")
		args = append(args, escapeLikePattern(q.Action)+"%")
	}
	if q.Result != "" {
		clauses = append(clauses, "result = ?")
		args = append(args, q.Result)
	}
	if q.Target != "" {
		clauses = append(clauses, "instr(target, ?) > 0")
		args = append(args, q.Target)
	}
	if !q.Since.IsZero() {
		clauses = append(clauses, "at >= ?")
		args = append(args, q.Since.UnixNano())
	}
	if !q.Until.IsZero() {
		clauses = append(clauses, "at <= ?")
		args = append(args, q.Until.UnixNano())
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
