// Package material 承载材料面（ADR-0014）的纯密码学：Secret 值的 age 信封
// 加密（X25519）、值指纹与同源配对。值永不回显：一切读路径只回指纹，
// 明文仅在注入/分发路径经内存短驻。
//
// P1a-3a 起 KEK 文件面（装载/首启生成/退役管理/权限落盘）移基础设施层
// （internal/infrastructure/keyring）——本包零文件系统依赖；Cipher 经
// NewCipher 由装载侧注入，消费方（engine materials、githubapps put、
// admin rewrap、api）拿到的行为面不变。
package material

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"filippo.io/age"
)

// Cipher 是信封加密面：现役 KEK 唯一 Seal；Open 现役优先、退役兜底。
// 构造后不可变。单 key 形态（无退役 key）行为与历史版本完全一致。
type Cipher struct {
	identity *age.X25519Identity
	retired  []*age.X25519Identity
}

// NewCipher 依现役/退役 KEK 构造加密面（装载侧 keyring 的注入入口；
// retired 装载序由调用方保持确定——文件名排序）。
func NewCipher(identity *age.X25519Identity, retired []*age.X25519Identity) *Cipher {
	return &Cipher{identity: identity, retired: retired}
}

// HasActive 报告现役 KEK 在场（缺席由装载侧裁决：生成或报错）。
func (c *Cipher) HasActive() bool { return c.identity != nil }

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

// Open 解开信封（现役优先、退役兜底；注入/分发路径与维护面共用；返回
// 值不得落日志或回显面）。全部 key 均不可解时上抛现役的错误形态（错误
// 文本最可诊断）。
func (c *Cipher) Open(ciphertext []byte) ([]byte, error) {
	plaintext, err := openWith(c.identity, ciphertext)
	if err == nil {
		return plaintext, nil
	}
	for _, id := range c.retired {
		if plaintext, rerr := openWith(id, ciphertext); rerr == nil {
			return plaintext, nil
		}
	}
	return nil, err
}

// OpensWithActive 报告密文能否由现役 KEK 解开（不产出明文）：admin rewrap
// 的"已现行"幂等分类判定——true 即该行无需重封。
func (c *Cipher) OpensWithActive(ciphertext []byte) bool {
	_, err := openWith(c.identity, ciphertext)
	return err == nil
}

func openWith(identity *age.X25519Identity, ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
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

// SealedValue 是一次密封写的产品：密文 + 值指纹的同源配对（派生自同一次
// Produce 的同一明文）。写侧密封的唯一入口（2026-10-03 架构评审候选 7
// 收口：secret.put / database 凭证 / hook secret 双面此前各自手搓
// "Seal + Fingerprint" 两连调——配对一致性只靠两行相邻调用自觉，一处
// 重构错位即"封的是 A、指纹打的是 B"的静默错账）。维护面 admin rewrap
// 的单密文重封不经本入口（指纹恒定，行上不动）。
type SealedValue struct {
	Ciphertext  []byte
	Fingerprint string
}

// Produce 密封明文并铸同源指纹。
func (c *Cipher) Produce(plaintext []byte) (SealedValue, error) {
	ct, err := c.Seal(plaintext)
	if err != nil {
		return SealedValue{}, err
	}
	return SealedValue{Ciphertext: ct, Fingerprint: Fingerprint(plaintext)}, nil
}

// String 是脱敏形态（日志/错误文本安全面）：只露指纹——密文会滚（age
// 随机 nonce，无对账价值），明文永不进字符串面。
func (v SealedValue) String() string {
	return "sealed:" + v.Fingerprint
}
