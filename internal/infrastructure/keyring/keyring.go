// Package keyring 承载 KEK 文件面（ADR-0014）：现役/退役 age X25519 KEK
// 的装载、首启生成与权限落盘。P1a-3a 拆自 domain/services/material——
// 文件系统属基础设施，纯密码学（Seal/Open/指纹/同源配对）留 domain；
// 本包装载产物 material.Cipher 经装配点注入消费方（engine materials、
// githubapps put、admin rewrap、api）。
//
// KEK 轮换（工具化于 `fleetlyd admin rewrap`，操作序见 runbook）：
//  1. 停机窗口内：现役 master.agekey 改名为 master-<标记>.agekey（退役，
//     仅解封），新 KEK 写入 keys/master.agekey（现役，唯一 Seal）；
//  2. `fleetlyd admin rewrap`（dry-run 报告 → --execute）：全部密文行
//     用现役 KEK 重封（secrets 表 + app_hooks 的 webhook secret），单事务；
//  3. 全部行迁移后删除退役 key 文件；Platform Backup 含密封密钥，恢复
//     走解封闭环，KEK 单独保管提示面向运维（不在本包范围）。
package keyring

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"

	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
)

// kekFileName 是现役 KEK 文件名（age X25519 identity 文本形态）。
const kekFileName = "master.agekey"

// retiredKEKPattern 是退役 KEK 文件名形态（轮换窗口内仅解封用；现役名
// master.agekey 因缺 `-` 段不命中）。退役 key 只进解封面，绝不参与 Seal
// ——轮换方向恒为新 key 封。
const retiredKEKPattern = "master-*.agekey"

// keysDirName 是 KEK 目录名（数据根下，0700 平台私有）。
const keysDirName = "keys"

// LoadCipher 打开数据根下的 KEK；现役文件不存在时生成并落盘（首启初始
// 化——0600 权限，数据根内平台私有）。退役 KEK（master-*.agekey，轮换
// 窗口在场）一并装载参与解封——轮换中重启 daemon 不破坏旧密文的注入
// 路径；全部行重封后文件删除即自然退出解封面。
func LoadCipher(dataRoot string) (*material.Cipher, error) {
	c, err := loadCipherDir(dataRoot)
	if err != nil {
		return nil, err
	}
	if c.HasActive() {
		return c, nil
	}
	dir := filepath.Join(dataRoot, keysDirName)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("material: generate master key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("material: persist master key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, kekFileName), []byte(id.String()), 0o600); err != nil {
		return nil, fmt.Errorf("material: persist master key: %w", err)
	}
	return material.NewCipher(id, nil), nil
}

// LoadExistingCipher 是维护面（admin rewrap）装载形态：现役 KEK 缺席即
// 错，绝不静默生成——维护操作不允许顺手落一把没人知道用途的新 key。
func LoadExistingCipher(dataRoot string) (*material.Cipher, error) {
	c, err := loadCipherDir(dataRoot)
	if err != nil {
		return nil, err
	}
	if !c.HasActive() {
		return nil, fmt.Errorf("material: no active master key at %s (keys/%s missing); place the new active key there first",
			dataRoot, kekFileName)
	}
	return c, nil
}

// loadCipherDir 读密钥目录：现役 master.agekey + 全部退役 master-*.agekey
// （文件名排序，装载序确定）。现役缺席不报错（由调用方裁决生成或报错）；
// 退役文件解析失败即错——静默跳过会把"解不开的行"拖到 rewrap 报告才
// 暴露，且掩盖文件放置失误。
func loadCipherDir(dataRoot string) (*material.Cipher, error) {
	dir := filepath.Join(dataRoot, keysDirName)
	var active *age.X25519Identity
	var retired []*age.X25519Identity
	data, err := os.ReadFile(filepath.Join(dir, kekFileName)) //nolint:gosec // 平台私有 KEK，权限由写入端钉 0600
	switch {
	case err == nil:
		id, perr := age.ParseX25519Identity(string(bytes.TrimSpace(data)))
		if perr != nil {
			return nil, fmt.Errorf("material: parse master key: %w", perr)
		}
		active = id
	case os.IsNotExist(err):
		// 现役缺席：LoadCipher 生成 / LoadExistingCipher 报错。
	default:
		return nil, fmt.Errorf("material: read master key: %w", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return material.NewCipher(active, retired), nil
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
		retired = append(retired, id)
	}
	return material.NewCipher(active, retired), nil
}
