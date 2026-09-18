package state

import (
	"context"
	"database/sql"
	"fmt"
)

// meta 表：平台自身身份与小规模 settings（state-model §2.1「平台身份与
// 凭证」行的 settings 载体；v0.1 冻结清单未单列业务表，键值结构随阶段
// 只增键不改表）。
const (
	// MetaKeyPlatformNodeID 是本机平台节点 ID 的 meta 键（值 = n_<ULID>，
	// state-model §2.3：首次启动生成、此后持久复用）。
	MetaKeyPlatformNodeID = "platform_node_id"
)

// GetMeta 读取 meta 键值；键不存在返回 ("", nil)（调用方以空串判定缺失）。
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	const q = `SELECT value FROM meta WHERE key = ?`
	var v string
	err := s.db.QueryRowContext(ctx, q, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("state: get meta %s: %w", key, err)
	}
	return v, nil
}

// SetMeta 写入（upsert）meta 键值。
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		return tx.SetMeta(ctx, key, value)
	})
	if err != nil {
		return fmt.Errorf("state: set meta %s: %w", key, err)
	}
	return nil
}

// SetMeta 在事务内 upsert meta 键值（供与审计/业务写同事务组合）。
func (t *Tx) SetMeta(ctx context.Context, key, value string) error {
	const q = `INSERT INTO meta (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
	if _, err := t.ExecContext(ctx, q, key, value, nowNano()); err != nil {
		return fmt.Errorf("state: upsert meta %s: %w", key, err)
	}
	return nil
}
