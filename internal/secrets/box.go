// Package secrets 是平台密钥方案的 envelope 加密实现（architecture §2.3
// 密钥方案 + §2.4 密钥行）：
//
//   - envelope 加密 = age（filippo.io/age，依赖复核选定——密钥即文件，
//     与「主密钥存控制面主机文件、权限保护、与备份分离」形态天然契合）；
//   - 主密钥 = age X25519 identity，首次启动生成、文件权限保护（POSIX
//     下要求 owner 独占；Windows 无同语义位，检查降级为文件存在+可解析）；
//   - env 密文入 env_vars 表（state 层不解释密文）；secret 值永不进
//     事件/审计/日志（state-model §2.9——负面测试钉死）；
//   - Swarm secret 引用 `fleetly-<app>-<name>-<hash8>`：值轮换即换名
//     换引用（hash8 = 内容 sha256 前 8）。
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filippo.io/age"
)

// DefaultKeyPath 是主密钥文件缺省路径（可经配置 secrets.key_path 覆盖）。
const DefaultKeyPath = "./fleetly.key"

// ErrWeakPermissions 表示主密钥文件权限过宽（POSIX：group/other 有任何
// 访问位）。密钥与备份分离、文件权限保护是架构 §2.3 的硬要求——fail-fast
// 拒绝加载，不静默降级。
var ErrWeakPermissions = errors.New("secrets: master key file permissions too open")

// Box 是 envelope 加解密器：持主密钥（age identity），Encrypt/Decrypt 对称
// 使用。零值不可用；经 EnsureKey/LoadKey 构造。
type Box struct {
	identity age.Identity
	// path 是主密钥文件路径（日志展示用，不含密钥材料本身）。
	path string
}

// EnsureKey 加载主密钥；文件不存在时生成新密钥并落盘（首次启动生成 +
// 返回 created=true，调用方必须提示用户妥善保存——密钥丢失 = 全部 env
// 密文不可解，与备份分离保存）。路径权限检查失败 → ErrWeakPermissions。
func EnsureKey(path string) (*Box, bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, false, fmt.Errorf("secrets: key path is empty")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		box, err := generate(path)
		if err != nil {
			return nil, false, err
		}
		return box, true, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("secrets: stat key %s: %w", path, err)
	}
	box, err := LoadKey(path)
	if err != nil {
		return nil, false, err
	}
	return box, false, nil
}

// LoadKey 从文件加载既有主密钥（不自动生成——CLI 等只读消费方显式加载）。
func LoadKey(path string) (*Box, error) {
	if err := checkKeyPermissions(path); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来自操作员配置（secrets.key_path），非不可信输入
	if err != nil {
		return nil, fmt.Errorf("secrets: read key %s: %w", path, err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("secrets: parse key %s: %w", path, err)
	}
	return &Box{identity: identity, path: path}, nil
}

// generate 生成新主密钥并落盘（0600；写失败即失败，不留半截文件）。
func generate(path string) (*Box, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("secrets: generate identity: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("secrets: mkdir key dir: %w", err)
	}
	// 文件内容 = identity 私钥一行（age 标准形态，可被 age CLI 识别）。
	if err := os.WriteFile(path, []byte(identity.String()+"\n"), 0o600); err != nil { //nolint:gosec // 密钥文件本身，权限即语义
		return nil, fmt.Errorf("secrets: write key %s: %w", path, err)
	}
	if err := checkKeyPermissions(path); err != nil {
		return nil, fmt.Errorf("secrets: generated key failed permission check: %w", err)
	}
	return &Box{identity: identity, path: path}, nil
}

// checkKeyPermissions 校验主密钥文件权限：POSIX 要求 owner 独占（0o077
// 位全零）。Windows 文件模式不承载 POSIX 权限语义（os.Stat 恒报 0666），
// 检查降级为跳过——真实加固靠部署面（安装文档要求密钥目录 ACL 收敛），
// 如实降级不伪装检查。
func checkKeyPermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("secrets: stat key %s: %w", path, err)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		return fmt.Errorf("%w: %s mode %o (want owner-only, e.g. 600)", ErrWeakPermissions, path, perm)
	}
	return nil
}

// Path 返回主密钥文件路径。
func (b *Box) Path() string {
	if b == nil {
		return ""
	}
	return b.path
}

// Recipient 返回主公钥（age recipient 字符串）。指纹/展示用：备份台账与
// 恢复校验用它对齐「密钥指纹」（state-model §2.7 恢复顺序②），不承载
// 解密能力。
func (b *Box) Recipient() (string, error) {
	if b == nil || b.identity == nil {
		return "", errors.New("secrets: box not initialized")
	}
	r, ok := b.identity.(*age.X25519Identity)
	if !ok {
		return "", errors.New("secrets: identity is not X25519")
	}
	return r.Recipient().String(), nil
}

// Encrypt 以主公钥 envelope 加密（输出 = age 标准密文，ASCII armored 的
// 二进制流形态）。
func (b *Box) Encrypt(plaintext []byte) ([]byte, error) {
	if b == nil || b.identity == nil {
		return nil, errors.New("secrets: box not initialized")
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, b.identity.(*age.X25519Identity).Recipient())
	if err != nil {
		return nil, fmt.Errorf("secrets: open encrypt stream: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("secrets: write plaintext: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("secrets: close encrypt stream: %w", err)
	}
	return buf.Bytes(), nil
}

// Decrypt 以主密钥解 envelope。错误信息不含密文/明文材料（age 库错误本身
// 不回显内容；本包不把输入拼进错误——负面测试钉死）。
func (b *Box) Decrypt(ciphertext []byte) ([]byte, error) {
	if b == nil || b.identity == nil {
		return nil, errors.New("secrets: box not initialized")
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), b.identity)
	if err != nil {
		return nil, fmt.Errorf("secrets: open decrypt stream: %w", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("secrets: read plaintext: %w", err)
	}
	return out, nil
}

// CheckHealth 报告密钥就绪（fleetlyd secrets 服务结构性 Checker：
// 密钥已加载即可用——解密能力由 Encrypt/Decrypt 自证，健康检查不做额外
// 加解密自旋）。
func (b *Box) CheckHealth() error {
	if b == nil || b.identity == nil {
		return errors.New("secrets: master key not initialized")
	}
	return nil
}
