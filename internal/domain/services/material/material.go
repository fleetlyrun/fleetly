// Package material 承载材料面（ADR-0014）：Secret 值的 age 信封加密
// （X25519）、KEK 管理与值指纹。值永不回显：一切读路径只回指纹，明文
// 仅在注入/分发路径经内存短驻。
//
// KEK 轮换（工具化于 `fleetlyd admin rewrap`，操作序见 runbook）：
//  1. 停机窗口内：现役 master.agekey 改名为 master-<标记>.agekey（退役，
//     仅解封），新 KEK 写入 keys/master.agekey（现役，唯一 Seal）；
//  2. `fleetlyd admin rewrap`（dry-run 报告 → --execute）：全部密文行
//     用现役 KEK 重封（secrets 表 + app_hooks 的 webhook secret），单事务；
//  3. 全部行迁移后删除退役 key 文件；Platform Backup 含密封密钥，恢复
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

// kekFileName 是现役 KEK 文件名（age X25519 identity 文本形态）。
const kekFileName = "master.agekey"

// retiredKEKPattern 是退役 KEK 文件名形态（轮换窗口内仅解封用；现役名
// master.agekey 因缺 `-` 段不命中）。退役 key 只进解封面，绝不参与 Seal
// ——轮换方向恒为新 key 封。
const retiredKEKPattern = "master-*.agekey"

// Cipher 是信封加密面：现役 KEK 唯一 Seal；Open 现役优先、退役兜底。
// 构造后不可变。单 key 形态（无退役文件）行为与历史版本完全一致。
type Cipher struct {
	identity *age.X25519Identity
	retired  []*age.X25519Identity
}

// LoadCipher 打开数据根下的 KEK；现役文件不存在时生成并落盘（首启初始
// 化——0600 权限，数据根内平台私有）。退役 KEK（master-*.agekey，轮换
// 窗口在场）一并装载参与解封——轮换中重启 daemon 不破坏旧密文的注入
// 路径；全部行重封后文件删除即自然退出解封面。
func LoadCipher(dataRoot string) (*Cipher, error) {
	c, err := loadCipherDir(dataRoot)
	if err != nil {
		return nil, err
	}
	if c.identity != nil {
		return c, nil
	}
	dir := filepath.Join(dataRoot, "keys")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("material: generate master key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, kekFileName), []byte(id.String()), 0o600); err != nil {
		return nil, fmt.Errorf("material: persist master key: %w", err)
	}
	c.identity = id
	return c, nil
}

// LoadExistingCipher 是维护面（admin rewrap）装载形态：现役 KEK 缺席即
// 错，绝不静默生成——维护操作不允许顺手落一把没人知道用途的新 key。
func LoadExistingCipher(dataRoot string) (*Cipher, error) {
	c, err := loadCipherDir(dataRoot)
	if err != nil {
		return nil, err
	}
	if c.identity == nil {
		return nil, fmt.Errorf("material: no active master key at %s (keys/%s missing); place the new active key there first",
			dataRoot, kekFileName)
	}
	return c, nil
}

// loadCipherDir 读密钥目录：现役 master.agekey + 全部退役 master-*.agekey
// （文件名排序，装载序确定）。现役缺席不报错（由调用方裁决生成或报错）；
// 退役文件解析失败即错——静默跳过会把"解不开的行"拖到 rewrap 报告才
// 暴露，且掩盖文件放置失误。
func loadCipherDir(dataRoot string) (*Cipher, error) {
	dir := filepath.Join(dataRoot, "keys")
	c := &Cipher{}
	data, err := os.ReadFile(filepath.Join(dir, kekFileName)) //nolint:gosec // 平台私有 KEK，权限由写入端钉 0600
	switch {
	case err == nil:
		id, perr := age.ParseX25519Identity(string(bytes.TrimSpace(data)))
		if perr != nil {
			return nil, fmt.Errorf("material: parse master key: %w", perr)
		}
		c.identity = id
	case os.IsNotExist(err):
		// 现役缺席：LoadCipher 生成 / LoadExistingCipher 报错。
	default:
		return nil, fmt.Errorf("material: read master key: %w", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return c, nil
		}
		return nil, fmt.Errorf("material: read keys dir: %w", rerr)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ok, merr := filepath.Match(retiredKEKPattern, e.Name())
		if merr != nil || !ok {
			continue
		}
		data, derr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // 平台私有 KEK，权限由写入端钉 0600
		if derr != nil {
			return nil, fmt.Errorf("material: read retired key %s: %w", e.Name(), derr)
		}
		id, perr := age.ParseX25519Identity(string(bytes.TrimSpace(data)))
		if perr != nil {
			return nil, fmt.Errorf("material: parse retired key %s: %w", e.Name(), perr)
		}
		c.retired = append(c.retired, id)
	}
	return c, nil
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
