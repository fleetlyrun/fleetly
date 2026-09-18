package state

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// tokens 表读写（T2.17；state-model §2.9）：哈希存储，明文永不落库。
// 调用方（internal/api）生成明文 token 并计算 sha256 hex 落库；本层提供
// 建/列/吊销/哈希认证原语与 fail-closed 审计（token 生命周期动作必入审计）。
//
// scope 词表（read/deploy/admin）不进数据库约束——scopes 列存逗号分隔词表，
// 语义与蕴含判定（admin ⊃ deploy ⊃ read）在 internal/api 承载；本层只做
// 非空防御。

// Token 是一条 API token 行（只读投影；明文不存在于任何通道）。
type Token struct {
	ID string
	// TokenHash 是明文 token 的 sha256 hex（64 字符）。
	TokenHash string
	// Name 是人读备注（proto 面 note；建行时落）。
	Name string
	// Scopes 是逗号分隔 scope 词表（如 "read,deploy"）。
	Scopes string
	// HashPrefix 是 TokenHash 前 12 hex（识别用，非凭据）。
	HashPrefix string
	CreatedAt  time.Time
	// LastUsedAt 零值 = 从未使用。
	LastUsedAt time.Time
	// RevokedAt 零值 = 在册。
	RevokedAt time.Time
}

// tokRowCols 是 token 行查询列清单（命名避开 "token" 前缀——gosec G101
// 对凭据样常量名误报；列清单本身非凭据）。
const tokRowCols = `id, token_hash, name, scopes, created_at, last_used_at, revoked_at`

// token 相关哨兵错误。
var (
	// ErrTokenNotFound 表示目标 token 不存在。
	ErrTokenNotFound = errors.New("token not found")
	// ErrTokenRevoked 表示 token 已吊销（认证拒绝路径）。
	ErrTokenRevoked = errors.New("token revoked")
	// ErrTokenInvalid 表示凭据不匹配（哈希查无此行或常量时间比对失败）。
	ErrTokenInvalid = errors.New("token invalid")
)

// HashToken 计算明文 token 的存储哈希（sha256 hex，64 字符）——落库与认证
// 双方共用此形态，防止出现两种哈希口径。
func HashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// TokenWrite 是一次 token 写入。
type TokenWrite struct {
	// ID 留空自动生成 ULID。
	ID string
	// Hash 必须是 HashToken 产物形态（64 hex sha256）；非法形态拒绝
	//（防御性，防旁路写入明文）。
	Hash string
	// Name 是人读备注（proto 面 note）。
	Name string
	// Scopes 是逗号分隔 scope 词表（调用方已归一，如 "read,deploy"）。
	Scopes string
	// ActorTokenID 是发起写入的调用方 token（可空——bootstrap 种子无调用
	// 方 token）；入审计 fail-closed。
	ActorTokenID string
	// Actor 是审计主体（human/ai_agent/system；空回落 human——系统代签发
	// 的 token 如 git push 钩子回调 token 传 system，T2.19）。
	Actor string
}

// CreateToken 在事务内落一条 token 行（哈希形态入参）并与审计同事务
// fail-closed。
func (s *Store) CreateToken(ctx context.Context, w TokenWrite) (Token, error) {
	if len(w.Hash) != 64 {
		return Token{}, fmt.Errorf("state: create token: hash must be 64-char sha256 hex, got %d chars", len(w.Hash))
	}
	if w.Scopes == "" {
		return Token{}, fmt.Errorf("state: create token: scopes is empty")
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	actor := w.Actor
	if actor == "" {
		actor = "human"
	}
	var out Token
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		const q = `INSERT INTO tokens (id, token_hash, name, scopes, created_at) VALUES (?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q, id, w.Hash, w.Name, w.Scopes, now); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("state: create token: duplicate hash (token already exists)")
			}
			return fmt.Errorf("state: insert token: %w", err)
		}
		if err := tx.WriteAudit(ctx, AuditEntry{
			Actor:        actor,
			ActorTokenID: w.ActorTokenID,
			Action:       "token.create",
			Target:       "token:" + id,
			Result:       "ok",
			DiffSummary:  `{"scopes":"` + w.Scopes + `"}`,
		}); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT `+tokRowCols+` FROM tokens WHERE id = ?`, id)
		t, err := scanToken(row)
		if err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return Token{}, err
	}
	return out, nil
}

// ListTokens 返回全部在册（未吊销）token（created_at 升序）。
func (s *Store) ListTokens(ctx context.Context) ([]Token, error) {
	const q = `SELECT ` + tokRowCols + ` FROM tokens WHERE revoked_at IS NULL ORDER BY created_at ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("state: list tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate tokens: %w", err)
	}
	return out, nil
}

// HasAnyToken 报告是否已存在任意 token 行（含已吊销）——bootstrap 判定
// 「首启无任何 token」的谓词（吊销过的也算存在过：bootstrap 一次性语义）。
func (s *Store) HasAnyToken(ctx context.Context) (bool, error) {
	const q = `SELECT COUNT(1) FROM tokens`
	var n int64
	if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return false, fmt.Errorf("state: count tokens: %w", err)
	}
	return n > 0, nil
}

// AuthenticateToken 按明文认证：哈希查行 → 常量时间二次比对（belt-and-
// suspenders：行查找按哈希等值走索引，不泄漏明文时序；二次比对保证比对
// 通道本身常量时间）→ 吊销检查 → 盖 last_used_at。成功返回在册 token。
func (s *Store) AuthenticateToken(ctx context.Context, plaintext string) (Token, error) {
	hash := HashToken(plaintext)
	row := s.db.QueryRowContext(ctx, `SELECT `+tokRowCols+` FROM tokens WHERE token_hash = ?`, hash)
	t, err := scanToken(row)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return Token{}, ErrTokenInvalid
		}
		return Token{}, err
	}
	if subtle.ConstantTimeCompare([]byte(t.TokenHash), []byte(hash)) != 1 {
		return Token{}, ErrTokenInvalid
	}
	if !t.RevokedAt.IsZero() {
		return Token{}, ErrTokenRevoked
	}
	now := time.Now().UTC()
	if err := s.TouchTokenUsed(ctx, t.ID); err != nil {
		// last_used 是尽力而为的观测面：盖失败不拒绝已认证请求。
		_ = err
	} else {
		t.LastUsedAt = now
	}
	return t, nil
}

// TouchTokenUsed 刷新 last_used_at（尽力而为观测面；token 已消失时静默）。
func (s *Store) TouchTokenUsed(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE id = ?`, nowNano(), id)
	if err != nil {
		return fmt.Errorf("state: touch token used: %w", err)
	}
	return nil
}

// RevokeToken 吊销 token（幂等：已吊销返回成功；不存在返回
// ErrTokenNotFound）。与审计同事务 fail-closed。
func (s *Store) RevokeToken(ctx context.Context, id, actorTokenID string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		res, err := tx.ExecContext(ctx,
			`UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
		if err != nil {
			return fmt.Errorf("state: revoke token: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read revoke count: %w", err)
		}
		if n == 0 {
			// 区分「已吊销（幂等成功）」与「不存在」。
			var one int
			err := tx.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE id = ?`, id).Scan(&one)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrTokenNotFound
				}
				return fmt.Errorf("state: probe token: %w", err)
			}
			return nil // 已吊销：幂等成功
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:        "human",
			ActorTokenID: actorTokenID,
			Action:       "token.revoke",
			Target:       "token:" + id,
			Result:       "ok",
		})
	})
}

// scanToken 从单行构造 Token。
func scanToken(row interface{ Scan(dest ...any) error }) (Token, error) {
	var t Token
	var lastUsed, revoked sql.NullInt64
	var created int64
	if err := row.Scan(&t.ID, &t.TokenHash, &t.Name, &t.Scopes, &created, &lastUsed, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, ErrTokenNotFound
		}
		return Token{}, fmt.Errorf("state: scan token: %w", err)
	}
	t.CreatedAt = time.Unix(0, created).UTC()
	if lastUsed.Valid {
		t.LastUsedAt = time.Unix(0, lastUsed.Int64).UTC()
	}
	if revoked.Valid {
		t.RevokedAt = time.Unix(0, revoked.Int64).UTC()
	}
	if len(t.TokenHash) >= 12 {
		t.HashPrefix = t.TokenHash[:12]
	}
	return t, nil
}
