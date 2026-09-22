package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/oklog/ulid/v2"
)

// webhook 订阅端点与投递台账（E6 观测专项设计 §5.1/§5.2，W5-S4）：
// webhook_endpoints（订阅端点权威态）+ webhook_deliveries（投递台账）+
// webhook_state（投递器事件消费游标）。设计红线：**通知自身零事件**（§5.2
// 防自激励环）——本文件的全部写路径只落审计行（webhook.created/updated/
// deleted，tokens/s3 同型），AppendEvent 零调用；events.golden 零变化。
//
// secret 纪律（state-model §2.9）：secret_cipher 存 envelope 密文（加密边
// 界在调用方 internal/api，s3.secret_access_key 同型）；secret_fingerprint
// 存明文 sha256 前 8 hex（读侧指纹，app_secrets.hash8 同口径）——读面出
// 指纹不出明文，明文只在创建/轮换响应一次性返回。密文/指纹/URL 绝不进
// 审计 diff（审计只带名字/模式数/开关等事实字段）。
//
// 词表：投递状态 pending|ok|failed（CHECK 词典 + 本文件写入通道双保险）；
// failed = 终态（重试预算耗尽）。

// Webhook 相关哨兵错误（api 面映射 404/409 信封）。
var (
	// ErrWebhookNotFound 表示目标端点不存在。
	ErrWebhookNotFound = errors.New("webhook endpoint not found")
	// ErrWebhookNameConflict 表示端点名已被占用（UNIQUE 冲突的语义化投影）。
	ErrWebhookNameConflict = errors.New("webhook endpoint name conflict")
)

// 投递状态词表。
const (
	WebhookDeliveryPending = "pending"
	WebhookDeliveryOK      = "ok"
	WebhookDeliveryFailed  = "failed"
)

// webhookStateKeyCursor 是 webhook_state 表的消费游标键（单行状态）。
const webhookStateKeyCursor = "last_seq"

// WebhookEndpoint 是一条订阅端点行（只读投影；secret 只出密文与指纹）。
type WebhookEndpoint struct {
	ID   string
	Name string
	URL  string
	// SecretCipher 是 envelope 密文（存储形态；解密边界在调用方）。
	SecretCipher string
	// SecretFingerprint 是明文 sha256 前 8 hex（识别用，非凭据）。
	SecretFingerprint string
	// EventPatterns 是订阅模式集（保存时已过白名单校验）。
	EventPatterns []string
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// WebhookEndpointWrite 是一次端点创建写入（secret 密文/指纹由调用方加密
// 计算后入参——本层不解释密文）。
type WebhookEndpointWrite struct {
	// ID 留空自动生成 ULID。
	ID   string
	Name string
	URL  string
	// SecretCipher 必须是 envelope 密文形态；空串拒绝（防御性——端点无
	// 签名密钥即不可验签，创建即无效）。
	SecretCipher string
	// SecretFingerprint 必须是明文 sha256 前 8 hex（16 字符）；非法形态拒绝。
	SecretFingerprint string
	EventPatterns     []string
	Enabled           bool
	// Actor/ActorTokenID 进审计（API 面传调用者身份，同事务 fail-closed）。
	Actor        string
	ActorTokenID string
}

// WebhookEndpointUpdate 是端点更新的可选字段集（nil/nil 切片 = 不变）。
type WebhookEndpointUpdate struct {
	Name *string
	URL  *string
	// EventPatterns 非 nil = 整体替换（替换前过 ValidateWebhookPatterns）。
	EventPatterns []string
	Enabled       *bool
	// SecretRotate 密钥轮换（cipher/fingerprint 成对提供——API 面生成新
	// 明文后加密写入；两字段必须同时非 nil，单一字段拒绝）。
	SecretCipher      *string
	SecretFingerprint *string
	Actor             string
	ActorTokenID      string
}

// webhookPatternRe 是订阅模式的白名单：小写字母/数字/点/下划线/连字符 +
// `*` 通配。事件名自身是 [a-z0-9_.] 词表（eventcode 注册纪律），白名单是
// 匹配器转义之外的第二道防注入闸（pattern 会经 regexp.QuoteMeta 逐段转义
// 后再拼匹配器，白名单让「库里存的模式永远可安全渲染」成为存储不变量）。
var webhookPatternRe = regexp.MustCompile(`^[a-z0-9._\-*]{1,128}$`)

// webhookNameRe 是端点名白名单（1..64；字母数字开头，主体字母数字/点/
// 下划线/连字符——CLI 位置参数与 Console 表单共用词表）。
var webhookNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ValidateWebhookName 校验端点名（创建与改名共用）。防御性第二道闸
// （主闸在 API 面的 400 形状校验——形状违约不走注册表码，api 层退化信封）。
func ValidateWebhookName(name string) error {
	if !webhookNameRe.MatchString(name) {
		return fmt.Errorf("state: webhook endpoint name %q is invalid (1..64 chars; letters/digits first, then letters/digits/dot/underscore/hyphen)", name)
	}
	return nil
}

// ValidateWebhookURL 校验接收端 URL（设计 §5.1：scheme http/https；https
// 强烈建议、http 允许——内网 receiver；**单操作员信任模型，无 SSRF 过滤**
// ——收件地址是操作员自己配的）。host 必须非空；拒绝带 userinfo 的形态
// （URL 不是秘密通道，凭据拼 URL 属误用——在入口显式拒绝）。防御性第二道
// 闸（主闸在 API 面 400 形状校验）。
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("state: webhook url %q does not parse: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("state: webhook url %q must use the http or https scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("state: webhook url %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("state: webhook url %q must not carry userinfo (credentials in URLs are a misuse; use the per-endpoint secret)", raw)
	}
	return nil
}

// ValidateWebhookPatterns 校验订阅模式集（保存前统一入口）：非空数组；
// 每项过白名单正则（含 `*` 通配）；去重保序后返回（存储形态 = 归一形态，
// 零重复语义由本函数收敛而非散落在消费侧）。
func ValidateWebhookPatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, apperr.New("E_WEBHOOK_PATTERN_INVALID",
			"webhook event_patterns must be a non-empty list of glob patterns (e.g. [\"deployment.*\"])")
	}
	seen := make(map[string]struct{}, len(patterns))
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if !webhookPatternRe.MatchString(p) {
			return nil, apperr.New("E_WEBHOOK_PATTERN_INVALID",
				"webhook event pattern %q is invalid (1..128 chars of [a-z0-9._-*]; '*' is the wildcard)", p)
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// webhookEndpointCols 是端点行查询列清单。
const webhookEndpointCols = `id, name, url, secret_cipher, secret_fingerprint, event_patterns, enabled, created_at, updated_at`

// scanWebhookEndpoint 从行扫描端点投影。
func scanWebhookEndpoint(row scanner) (WebhookEndpoint, error) {
	var (
		e            WebhookEndpoint
		cipher       string
		patternsJSON string
		enabled      int64
		createdAt    int64
		updatedAt    int64
	)
	if err := row.Scan(&e.ID, &e.Name, &e.URL, &cipher, &e.SecretFingerprint,
		&patternsJSON, &enabled, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WebhookEndpoint{}, ErrWebhookNotFound
		}
		return WebhookEndpoint{}, fmt.Errorf("state: scan webhook endpoint: %w", err)
	}
	e.SecretCipher = cipher
	if err := json.Unmarshal([]byte(patternsJSON), &e.EventPatterns); err != nil {
		return WebhookEndpoint{}, fmt.Errorf("state: scan webhook endpoint %s: patterns json: %w", e.ID, err)
	}
	if e.EventPatterns == nil {
		e.EventPatterns = []string{}
	}
	e.Enabled = enabled != 0
	e.CreatedAt = time.Unix(0, createdAt).UTC()
	e.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return e, nil
}

// CreateWebhookEndpoint 在事务内落一条端点行并与审计同事务 fail-closed。
// 名字冲突（UNIQUE）投影为 ErrWebhookNameConflict。
func (s *Store) CreateWebhookEndpoint(ctx context.Context, w WebhookEndpointWrite) (WebhookEndpoint, error) {
	if err := ValidateWebhookName(w.Name); err != nil {
		return WebhookEndpoint{}, err
	}
	if err := ValidateWebhookURL(w.URL); err != nil {
		return WebhookEndpoint{}, err
	}
	patterns, err := ValidateWebhookPatterns(w.EventPatterns)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	if strings.TrimSpace(w.SecretCipher) == "" {
		return WebhookEndpoint{}, fmt.Errorf("state: create webhook endpoint: secret cipher is empty")
	}
	if len(w.SecretFingerprint) != 16 {
		return WebhookEndpoint{}, fmt.Errorf("state: create webhook endpoint: fingerprint must be 16-char sha256 hex, got %d chars", len(w.SecretFingerprint))
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	patternsJSON, err := json.Marshal(patterns)
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("state: create webhook endpoint: marshal patterns: %w", err)
	}
	actor := w.Actor
	if actor == "" {
		actor = "human"
	}
	err = s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		const q = `INSERT INTO webhook_endpoints
			(id, name, url, secret_cipher, secret_fingerprint, event_patterns, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, w.Name, w.URL, w.SecretCipher,
			w.SecretFingerprint, string(patternsJSON), webhookBoolToInt(w.Enabled), now, now); err != nil {
			if isUniqueViolation(err) {
				return ErrWebhookNameConflict
			}
			return fmt.Errorf("state: insert webhook endpoint: %w", err)
		}
		// 审计（§5.1）：diff 只带名字/模式数/开关——URL 与密文/指纹零出现
		//（URL 虽非凭据，审计面保持最小事实集；读面自可见）。
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        actor,
			ActorTokenID: w.ActorTokenID,
			Action:       "webhook.created",
			Target:       "webhook:" + id,
			Result:       "ok",
			DiffSummary: DiffSummary("name", w.Name, "patterns_count", len(patterns),
				"enabled", w.Enabled),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return WebhookEndpoint{}, err
	}
	return s.GetWebhookEndpoint(ctx, id)
}

// ListWebhookEndpoints 返回全部端点（created_at 升序）。
func (s *Store) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+webhookEndpointCols+` FROM webhook_endpoints ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("state: list webhook endpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []WebhookEndpoint{}
	for rows.Next() {
		e, err := scanWebhookEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate webhook endpoints: %w", err)
	}
	return out, nil
}

// GetWebhookEndpoint 按 ID 取端点（不存在返回 ErrWebhookNotFound）。
func (s *Store) GetWebhookEndpoint(ctx context.Context, id string) (WebhookEndpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+webhookEndpointCols+` FROM webhook_endpoints WHERE id = ?`, id)
	return scanWebhookEndpoint(row)
}

// UpdateWebhookEndpoint 更新端点可选字段 + 审计（同一事务 fail-closed）。
// 至少一个字段必须提供（全空更新 = 无操作，显式拒绝防误调用）。终态失败
// 的端点被恢复（enabled 翻真/URL 修正/密钥轮换）时台账不清——组件红面随
// 下一次成功投递自然转绿（台账是事实面，不做状态性回写）。
func (s *Store) UpdateWebhookEndpoint(ctx context.Context, id string, u WebhookEndpointUpdate) (WebhookEndpoint, error) {
	anyField := u.Name != nil || u.URL != nil || u.EventPatterns != nil ||
		u.Enabled != nil || u.SecretCipher != nil || u.SecretFingerprint != nil
	if !anyField {
		return WebhookEndpoint{}, fmt.Errorf("state: update webhook endpoint: no fields to update")
	}
	if (u.SecretCipher == nil) != (u.SecretFingerprint == nil) {
		return WebhookEndpoint{}, fmt.Errorf("state: update webhook endpoint: secret rotation requires cipher and fingerprint together")
	}
	if u.Name != nil {
		if err := ValidateWebhookName(*u.Name); err != nil {
			return WebhookEndpoint{}, err
		}
	}
	if u.URL != nil {
		if err := ValidateWebhookURL(*u.URL); err != nil {
			return WebhookEndpoint{}, err
		}
	}
	var patternsJSON *string
	if u.EventPatterns != nil {
		patterns, err := ValidateWebhookPatterns(u.EventPatterns)
		if err != nil {
			return WebhookEndpoint{}, err
		}
		raw, err := json.Marshal(patterns)
		if err != nil {
			return WebhookEndpoint{}, fmt.Errorf("state: update webhook endpoint: marshal patterns: %w", err)
		}
		patternsJSON = new(string)
		*patternsJSON = string(raw)
	}
	actor := u.Actor
	if actor == "" {
		actor = "human"
	}
	// 审计 diff 字段集（先收集后写——同事务内看到的是最终形态）。
	changed := []any{}
	err := s.InTx(ctx, func(tx *Tx) error {
		// 行存在性先行（UPDATE 不报错不返回行数语义的防御——审计不落幽灵行）。
		if _, err := scanWebhookEndpoint(tx.QueryRowContext(ctx,
			`SELECT `+webhookEndpointCols+` FROM webhook_endpoints WHERE id = ?`, id)); err != nil {
			return err
		}
		now := nowNano()
		if u.Name != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE webhook_endpoints SET name = ?, updated_at = ? WHERE id = ?`,
				*u.Name, now, id); err != nil {
				if isUniqueViolation(err) {
					return ErrWebhookNameConflict
				}
				return fmt.Errorf("state: update webhook endpoint name: %w", err)
			}
			changed = append(changed, "name", *u.Name)
		}
		if u.URL != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE webhook_endpoints SET url = ?, updated_at = ? WHERE id = ?`,
				*u.URL, now, id); err != nil {
				return fmt.Errorf("state: update webhook endpoint url: %w", err)
			}
			changed = append(changed, "url_changed", true)
		}
		if patternsJSON != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE webhook_endpoints SET event_patterns = ?, updated_at = ? WHERE id = ?`,
				*patternsJSON, now, id); err != nil {
				return fmt.Errorf("state: update webhook endpoint patterns: %w", err)
			}
			changed = append(changed, "patterns_count", len(u.EventPatterns))
		}
		if u.Enabled != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE webhook_endpoints SET enabled = ?, updated_at = ? WHERE id = ?`,
				webhookBoolToInt(*u.Enabled), now, id); err != nil {
				return fmt.Errorf("state: update webhook endpoint enabled: %w", err)
			}
			changed = append(changed, "enabled", *u.Enabled)
		}
		if u.SecretCipher != nil && u.SecretFingerprint != nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE webhook_endpoints SET secret_cipher = ?, secret_fingerprint = ?, updated_at = ? WHERE id = ?`,
				*u.SecretCipher, *u.SecretFingerprint, now, id); err != nil {
				return fmt.Errorf("state: update webhook endpoint secret: %w", err)
			}
			changed = append(changed, "secret_rotated", true)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE webhook_endpoints SET updated_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("state: touch webhook endpoint: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        actor,
			ActorTokenID: u.ActorTokenID,
			Action:       "webhook.updated",
			Target:       "webhook:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary(changed...),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return WebhookEndpoint{}, err
	}
	return s.GetWebhookEndpoint(ctx, id)
}

// DeleteWebhookEndpoint 删除端点：台账行同事务显式清理（FK 无级联——级联
// 是业务动作，db_references 同型）+ 审计。幂等语义与 tokens 不同：不存在
// 即 ErrWebhookNotFound（rm 面需要明确 NotFound 反馈）。
func (s *Store) DeleteWebhookEndpoint(ctx context.Context, id, actor, actorTokenID string) error {
	if actor == "" {
		actor = "human"
	}
	return s.InTx(ctx, func(tx *Tx) error {
		// 先取行（审计要带名字；不存在即 NotFound——不落删除审计）。
		e, err := scanWebhookEndpoint(tx.QueryRowContext(ctx,
			`SELECT `+webhookEndpointCols+` FROM webhook_endpoints WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM webhook_deliveries WHERE endpoint_id = ?`, id); err != nil {
			return fmt.Errorf("state: delete webhook deliveries for endpoint: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM webhook_endpoints WHERE id = ?`, id); err != nil {
			return fmt.Errorf("state: delete webhook endpoint: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        actor,
			ActorTokenID: actorTokenID,
			Action:       "webhook.deleted",
			Target:       "webhook:" + id,
			Result:       "ok",
			DiffSummary:  DiffSummary("name", e.Name),
		}); err != nil {
			return err
		}
		return nil
	})
}

// webhookBoolToInt 把布尔落库为 0/1（webhook_endpoints.enabled 列；nodes.go
// 的 boolToInt 已占用包内该名字，这里用列语义命名避免重声明）。
func webhookBoolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ── 游标（webhook_state 单行状态）────────────────────────────────────────

// GetWebhookCursor 读取投递器事件消费游标（无行 = 0）。
func (s *Store) GetWebhookCursor(ctx context.Context) (int64, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT value FROM webhook_state WHERE key = ?`, webhookStateKeyCursor).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("state: read webhook cursor: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return v.Int64, nil
}

// webhookCursorInitialized 判定游标是否被初始化过（区分「全新安装从 0
// 起步」与「首启对齐到当前最大 seq」——投递器首启不对历史事件补投，
// 订阅从现在开始；GetWebhookCursor 的 0 值语义因此有二义，初始化位由
// webhook_state 行的存在性承载）。
func (s *Store) webhookCursorInitialized(ctx context.Context) (bool, error) {
	var one int64
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM webhook_state WHERE key = ?`, webhookStateKeyCursor).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("state: probe webhook cursor row: %w", err)
	}
	return true, nil
}

// setWebhookCursorTx 在事务内推进游标（只前进——单调语义与事件 seq 一致）。
func setWebhookCursorTx(ctx context.Context, tx *Tx, seq int64) error {
	const q = `INSERT INTO webhook_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
		WHERE CAST(excluded.value AS INTEGER) > CAST(webhook_state.value AS INTEGER)`
	if _, err := tx.ExecContext(ctx, q, webhookStateKeyCursor, strconv.FormatInt(seq, 10)); err != nil {
		return fmt.Errorf("state: advance webhook cursor: %w", err)
	}
	return nil
}

// SetWebhookCursor 独立推进游标（间隙重置路径；只前进）。
func (s *Store) SetWebhookCursor(ctx context.Context, seq int64) error {
	return s.InTx(ctx, func(tx *Tx) error {
		return setWebhookCursorTx(ctx, tx, seq)
	})
}

// ── 投递台账（webhook_deliveries）────────────────────────────────────────

// WebhookDelivery 是一条投递台账行。
type WebhookDelivery struct {
	ID         string
	EventSeq   int64
	EndpointID string
	Status     string
	Attempts   int
	// ResponseCode 是最近一次 HTTP 响应码（nil = 传输失败，无响应）。
	ResponseCode *int
	// LastError 是最近一次失败的单行化摘要（零凭据材料）。
	LastError string
	// NextRetryAt 是下次重试时刻（nil = 无待重试——终态或等待首发）。
	NextRetryAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// webhookDeliveryCols 是台账行查询列清单。
const webhookDeliveryCols = `id, event_seq, endpoint_id, status, attempts, response_code, last_error, next_retry_at, created_at, updated_at`

// scanWebhookDelivery 从行扫描台账投影。
func scanWebhookDelivery(row scanner) (WebhookDelivery, error) {
	var (
		d            WebhookDelivery
		responseCode sql.NullInt64
		nextRetry    sql.NullInt64
		createdAt    int64
		updatedAt    int64
	)
	if err := row.Scan(&d.ID, &d.EventSeq, &d.EndpointID, &d.Status, &d.Attempts,
		&responseCode, &d.LastError, &nextRetry, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WebhookDelivery{}, ErrWebhookNotFound
		}
		return WebhookDelivery{}, fmt.Errorf("state: scan webhook delivery: %w", err)
	}
	if responseCode.Valid {
		code := int(responseCode.Int64)
		d.ResponseCode = &code
	}
	if nextRetry.Valid {
		t := time.Unix(0, nextRetry.Int64).UTC()
		d.NextRetryAt = &t
	}
	d.CreatedAt = time.Unix(0, createdAt).UTC()
	d.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return d, nil
}

// CreateWebhookDeliveriesAndAdvance 是投递器的每事件消费原子：为匹配端点
// 各落一条 pending 行 + 推进消费游标（同一事务——崩溃either全有or全无，
// 投递行的存在与游标前进永不脱节：重放不会双投、推进不会漏投）。
// 返回新建的 pending 行（投递器据此入队）。matchedIDs 为空时只推进游标。
func (s *Store) CreateWebhookDeliveriesAndAdvance(ctx context.Context, eventSeq int64, matchedIDs []string) ([]WebhookDelivery, error) {
	out := make([]WebhookDelivery, 0, len(matchedIDs))
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		for _, epID := range matchedIDs {
			id := ulid.Make().String()
			const q = `INSERT INTO webhook_deliveries
				(id, event_seq, endpoint_id, status, attempts, last_error, created_at, updated_at)
				VALUES (?, ?, ?, ?, 0, '', ?, ?)`
			if _, err := tx.ExecContext(ctx, q, id, eventSeq, epID, WebhookDeliveryPending, now, now); err != nil {
				return fmt.Errorf("state: insert webhook delivery (event %d, endpoint %s): %w", eventSeq, epID, err)
			}
			row, err := scanWebhookDelivery(tx.QueryRowContext(ctx,
				`SELECT `+webhookDeliveryCols+` FROM webhook_deliveries WHERE id = ?`, id))
			if err != nil {
				return err
			}
			out = append(out, row)
		}
		return setWebhookCursorTx(ctx, tx, eventSeq)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InitializeWebhookCursorIfEmpty 首启对齐：游标行不存在时置为 maxSeq（不对
// 历史事件补投——订阅从现在开始）；已初始化（行存在）为 no-op 返回 false。
func (s *Store) InitializeWebhookCursorIfEmpty(ctx context.Context, maxSeq int64) (bool, error) {
	initialized, err := s.webhookCursorInitialized(ctx)
	if err != nil || initialized {
		return false, err
	}
	err = s.InTx(ctx, func(tx *Tx) error {
		// 二次确认（并发首启双进：只有写入者算初始化成功）。
		const q = `INSERT INTO webhook_state (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO NOTHING`
		res, err := tx.ExecContext(ctx, q, webhookStateKeyCursor, strconv.FormatInt(maxSeq, 10))
		if err != nil {
			return fmt.Errorf("state: initialize webhook cursor: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read webhook cursor init count: %w", err)
		}
		if n == 0 {
			return errWebhookCursorAlreadyInitialized
		}
		return nil
	})
	if errors.Is(err, errWebhookCursorAlreadyInitialized) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// errWebhookCursorAlreadyInitialized 是 InitializeWebhookCursorIfEmpty 的
// 并发收口信号（INSERT ... DO NOTHING 零行 = 输家）。
var errWebhookCursorAlreadyInitialized = errors.New("webhook cursor already initialized")

// WebhookAttemptResult 是一次投递尝试的结论（RecordWebhookAttempt 入参）。
type WebhookAttemptResult struct {
	// OK = 2xx 到手。OK 时 NextRetryAt 被忽略（终态 ok）。
	OK bool
	// ResponseCode 是 HTTP 响应码（传输失败 = 0——落库 NULL）。
	ResponseCode int
	// ErrText 是失败摘要（调用方单行化；本层截断）。
	ErrText string
	// NextRetryAt 非零 = 还有重试预算（pending + 定时）；零值 = 终态
	// failed（重试预算耗尽）。重试时刻由投递器按退避表计算。
	NextRetryAt time.Time
}

// RecordWebhookAttempt 记录一次投递尝试：attempts 恒增；成功 → 终态 ok；
// 失败且有 NextRetryAt → pending + 定时；失败且无 NextRetryAt → 终态
// failed。response_code 覆盖写（最近一次语义）。行不存在返回
// ErrWebhookNotFound（端点被删的竞态：投递器放弃该次结果）。
func (s *Store) RecordWebhookAttempt(ctx context.Context, deliveryID string, r WebhookAttemptResult) error {
	status := WebhookDeliveryFailed
	if r.OK {
		status = WebhookDeliveryOK
	} else if !r.NextRetryAt.IsZero() {
		status = WebhookDeliveryPending
	}
	var responseCode any
	if r.ResponseCode > 0 {
		responseCode = r.ResponseCode
	}
	var nextRetry any
	if !r.OK && !r.NextRetryAt.IsZero() {
		nextRetry = r.NextRetryAt.UTC().UnixNano()
	}
	errText := r.ErrText
	if len(errText) > 500 {
		errText = errText[:500]
	}
	errText = strings.ReplaceAll(errText, "\n", " ")
	if r.OK {
		errText = ""
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET status = ?, attempts = attempts + 1,
			response_code = ?, last_error = ?, next_retry_at = ?, updated_at = ?
			WHERE id = ?`,
		status, responseCode, errText, nextRetry, nowNano(), deliveryID)
	if err != nil {
		return fmt.Errorf("state: record webhook attempt: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read webhook attempt count: %w", err)
	}
	if n == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// DueWebhookDeliveries 返回到期可投的 pending 行（next_retry_at 为 NULL
// 或 ≤ now；created_at 升序——先欠先还）。skipIDs 是在途集合（投递器
// 已入队未收口的行——重复入队防御）。
func (s *Store) DueWebhookDeliveries(ctx context.Context, now time.Time, limit int, skipIDs map[string]struct{}) ([]WebhookDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+webhookDeliveryCols+` FROM webhook_deliveries
			WHERE status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)
			ORDER BY created_at ASC, id ASC LIMIT ?`,
		WebhookDeliveryPending, now.UTC().UnixNano(), limit*2)
	if err != nil {
		return nil, fmt.Errorf("state: query due webhook deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		if _, skip := skipIDs[d.ID]; skip {
			continue
		}
		out = append(out, d)
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate due webhook deliveries: %w", err)
	}
	return out, nil
}

// PostponeWebhookDelivery 推迟一条 pending 行（不计尝试、不改终态）——
// 端点停用期间投递器把到期行推后重扫（停用不是投递失败，不烧重试预算）。
func (s *Store) PostponeWebhookDelivery(ctx context.Context, deliveryID string, nextRetry time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET next_retry_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
		nextRetry.UTC().UnixNano(), nowNano(), deliveryID, WebhookDeliveryPending)
	if err != nil {
		return fmt.Errorf("state: postpone webhook delivery: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read webhook postpone count: %w", err)
	}
	if n == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// GetWebhookDelivery 按 ID 取台账行。
func (s *Store) GetWebhookDelivery(ctx context.Context, id string) (WebhookDelivery, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+webhookDeliveryCols+` FROM webhook_deliveries WHERE id = ?`, id)
	return scanWebhookDelivery(row)
}

// ListWebhookDeliveries 按可选端点/状态过滤台账（created_at 降序——最新
// 在前；limit ≤0 回落 50，天花板 500）。
func (s *Store) ListWebhookDeliveries(ctx context.Context, endpointID, status string, limit int) ([]WebhookDelivery, error) {
	switch status {
	case "", WebhookDeliveryPending, WebhookDeliveryOK, WebhookDeliveryFailed:
	default:
		return nil, fmt.Errorf("state: list webhook deliveries: invalid status filter %q", status)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT ` + webhookDeliveryCols + ` FROM webhook_deliveries WHERE 1=1`
	args := []any{}
	if endpointID != "" {
		q += ` AND endpoint_id = ?`
		args = append(args, endpointID)
	}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("state: list webhook deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []WebhookDelivery{}
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate webhook deliveries: %w", err)
	}
	return out, nil
}

// LatestWebhookTerminalDeliveries 返回每端点最近一条终态（ok|failed）台账
// 行（system status notifications 组件的判红数据源：最近终态 = failed 即
// 端点连续终败）。无终态行的端点不在结果中。
func (s *Store) LatestWebhookTerminalDeliveries(ctx context.Context) (map[string]WebhookDelivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+webhookDeliveryCols+` FROM webhook_deliveries AS d
			WHERE d.status IN ('ok', 'failed')
			AND d.updated_at = (
				SELECT MAX(d2.updated_at) FROM webhook_deliveries AS d2
				WHERE d2.endpoint_id = d.endpoint_id AND d2.status IN ('ok', 'failed'))`)
	if err != nil {
		return nil, fmt.Errorf("state: query latest webhook terminal deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]WebhookDelivery{}
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, err
		}
		if prev, ok := out[d.EndpointID]; ok && !d.UpdatedAt.Before(prev.UpdatedAt) {
			out[d.EndpointID] = d
			continue
		}
		if _, ok := out[d.EndpointID]; !ok {
			out[d.EndpointID] = d
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate latest webhook terminal deliveries: %w", err)
	}
	return out, nil
}

// DefaultWebhookDeliveryRetentionDays 是投递台账保留天数（设计 §5.2：
// janitor prune 7d——台账窗口与事件 30d 窗解耦，投递事实只保一周）。
const DefaultWebhookDeliveryRetentionDays = 7

// PruneExpiredWebhookDeliveries 删除早于 cutoff 的台账行（终态与 pending
// 一体清理——7d 前仍 pending 的行是僵尸：端点已删/禁用超窗，恢复无意义，
// 保留只会稀释读面）。分批形态同 PruneExpiredEvents。
func (s *Store) PruneExpiredWebhookDeliveries(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.deleteBatched(ctx,
		`DELETE FROM webhook_deliveries WHERE id IN (
			SELECT id FROM webhook_deliveries WHERE created_at < ? LIMIT ?)`,
		cutoff.UnixNano(), pruneBatchSize)
}
