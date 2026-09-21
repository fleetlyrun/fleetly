package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// restic repo 口令的库内存取（E3-3/D-S3-5，设计 §2.3）：
//
// 口令 32B 随机（生成在调用方 internal/statebackup，crypto/rand）以
// envelope 密文（internal/secrets）存 platform_settings 键
// s3.restic_password——**内部键**：不在 SaveS3Settings 的 PUT 全量键集、
// 不进 LoadS3Settings 的 typed 投影（用户设置面天然不见它；负面测试在
// 本包与 api 面各钉一道）。
//
// 分离语义（D-S3-5）：口令与主密钥文件（fleetly.key）分离 = 独立条目
// 独立轮换能力——口令泄露不暴露数据（还需端点凭证）、主密钥泄露不暴露
// 远端 repo（还需端点与口令）；envelope 主密钥仍是同一把 age key，分离
// 不是另立密钥体系。
//
// 裁决注记（设计 §2.3）：口令生成/轮换**不建事件、不扩审计 action 词表**
// ——这是平台内部凭据生命周期，不是用户可见变更；可观测面 = 日志中的
// 口令指纹（sha256 前 8 hex，不含材料）。

// LoadResticPasswordCiphertext 读取已存的 restic repo 口令密文。
// found=false = 尚未生成（上传轨据此走惰性生成路径）。
func (s *Store) LoadResticPasswordCiphertext(ctx context.Context) (ciphertext string, found bool, err error) {
	var v string
	err = s.db.QueryRowContext(ctx,
		`SELECT value FROM platform_settings WHERE key = ?`, S3KeyResticPassword).Scan(&v)
	if err == nil {
		return v, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("state: load %s: %w", S3KeyResticPassword, err)
}

// SaveResticPasswordCiphertext 落库口令密文（幂等 upsert；密文入参——
// 本层不解释密文，绝不把任何值拼进错误）。不写审计、不发事件（§2.3
// 裁决：内部凭据生命周期，审计 action 词表不扩）。
func (s *Store) SaveResticPasswordCiphertext(ctx context.Context, ciphertext string) error {
	if ciphertext == "" {
		return fmt.Errorf("state: save %s: empty ciphertext", S3KeyResticPassword)
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		const q = `INSERT INTO platform_settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
		if _, err := tx.ExecContext(ctx, q, S3KeyResticPassword, ciphertext, nowNano()); err != nil {
			return fmt.Errorf("state: upsert %s: %w", S3KeyResticPassword, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
