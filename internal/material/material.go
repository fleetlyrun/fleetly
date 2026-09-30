// Package material 承载材料面（ADR-0014）：Secret 值的 age 信封加密
// （X25519）、KEK 管理与值指纹。值永不回显：一切读路径只回指纹，明文
// 仅在注入/分发路径经内存短驻。
//
// KEK 轮换 runbook（随 Platform Backup 文档化）：
//  1. 生成新 identity：fleetly 密钥目录写新 master 文件（不覆盖旧文件）；
//  2. 遍历 secrets 表逐条 Open(旧) → Seal(新) 同事务重写；
//  3. 全部行迁移后删除旧 master 文件；Platform Backup 含密封密钥，恢复
//     走解封闭环，KEK 单独保管提示面向运维（不在本包范围）。
package material

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
)

// kekFileName 是 KEK 文件名（age X25519 identity 文本形态）。
const kekFileName = "master.agekey"

// Cipher 是信封加密面（单 KEK；构造后不可变）。
type Cipher struct {
	identity *age.X25519Identity
}

// LoadCipher 打开数据根下的 KEK；不存在时生成并落盘（首启初始化——
// 0600 权限，数据根内平台私有）。
func LoadCipher(dataRoot string) (*Cipher, error) {
	dir := filepath.Join(dataRoot, "keys")
	path := filepath.Join(dir, kekFileName)
	data, err := os.ReadFile(path) //nolint:gosec // 平台私有 KEK，权限由写入端钉 0600
	if err == nil {
		id, err := age.ParseX25519Identity(string(bytes.TrimSpace(data)))
		if err != nil {
			return nil, fmt.Errorf("material: parse master key: %w", err)
		}
		return &Cipher{identity: id}, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("material: read master key: %w", err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("material: generate master key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(id.String()), 0o600); err != nil {
		return nil, fmt.Errorf("material: persist master key: %w", err)
	}
	return &Cipher{identity: id}, nil
}

// Seal 用 KEK 信封加密明文（age armor 文本；密文落 secrets.ciphertext）。
func (c *Cipher) Seal(plaintext []byte) ([]byte, error) {
	buf := &bytes.Buffer{}
	w, err := age.Encrypt(buf, c.identity.Recipient())
	if err != nil {
		return nil, fmt.Errorf("material: seal: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Open 解开信封（注入/分发路径专用；返回值不得落日志或回显面）。
func (c *Cipher) Open(ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), c.identity)
	if err != nil {
		return nil, fmt.Errorf("material: open: %w", err)
	}
	return io.ReadAll(r)
}

// Fingerprint 是值指纹（sha256 前 16 hex；值永不回显，指纹作对账锚）。
func Fingerprint(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:8])
}
