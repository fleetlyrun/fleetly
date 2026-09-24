package gitserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	gossh "golang.org/x/crypto/ssh"
)

// host key 管理（类比平台节点身份的生成-持久-复用，state-model §2.3）：
// 首启不存在则生成 ed25519 并以 OpenSSH PEM 形态持久化（0600），二次启动
// 复用同一私钥——客户端 known_hosts 指纹稳定。私钥内容绝不打印（日志只
// 出指纹；FingerprintSHA256 是公钥指纹，可安全展示）。
//
// FZ-12（v0.3 W3-S2，rbac-teams §6 裁决 D-W0-8）：装载/生成后把 SHA256
// 指纹与 platform_settings 台账 git.hostkey_fingerprint 比对（reconcile，
// 见 internal/state/hostkeysettings.go）——首启建账静默；不同（文件重建/
// 换钥）→ 审计 + 事件 git.hostkey_changed 随台账更新同事务落库。变更检测
// 挂在启动装载（host key 文件进程生命周期内单次装载——运行期换文件在下次
// 启动披露，诚实边界）。GetSystemStatus 披露面经 Fingerprint() 现读文件；
// known_hosts 钉定为客户端文档指引，平台不代管下发。

// hostKeyPath 解析 host key 文件路径（config git.host_key_file；缺省回落
// <root>/host_ed25519）。
func (s *GitTriggers) hostKeyPath() string {
	if s.cfg.HostKeyFile != "" {
		return s.cfg.HostKeyFile
	}
	return filepath.Join(s.cfg.Root, "host_ed25519")
}

// ensureHostKey 装载（必要时生成）host key 并返回 signer；装载/生成后把
// 指纹与台账比对收敛（FZ-12 变更检测挂点——ctx 承载台账写）。
func (s *GitTriggers) ensureHostKey(ctx context.Context) (gossh.Signer, error) {
	path := s.hostKeyPath()
	raw, err := os.ReadFile(path) //nolint:gosec // G304：路径来自平台配置（git.host_key_file）
	if err == nil {
		signer, perr := gossh.ParsePrivateKey(raw)
		if perr != nil {
			return nil, fmt.Errorf("gitserver: parse host key %s: %w", path, perr)
		}
		s.reconcileHostKeyFingerprint(ctx, gossh.FingerprintSHA256(signer.PublicKey()))
		return signer, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("gitserver: read host key %s: %w", path, err)
	}

	// 生成 ed25519 并持久化（OpenSSH 形态 PEM；0600——私钥文件权限）。
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("gitserver: generate host key: %w", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "fleetly git server host key")
	if err != nil {
		return nil, fmt.Errorf("gitserver: marshal host key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(block)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("gitserver: create host key dir: %w", err)
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil { //nolint:gosec // G306：私钥文件 0600 即要求
		return nil, fmt.Errorf("gitserver: write host key %s: %w", path, err)
	}
	signer, err := gossh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("gitserver: reparse generated host key: %w", err)
	}
	s.log.Info("gitserver: host key generated", "path", path,
		"fingerprint", gossh.FingerprintSHA256(signer.PublicKey()))
	s.reconcileHostKeyFingerprint(ctx, gossh.FingerprintSHA256(signer.PublicKey()))
	return signer, nil
}

// Fingerprint 返回当前 host key 的 SHA256 指纹（OpenSSH 形态 SHA256:…；
// FZ-12 披露面——GetSystemStatus 经 api 侧 GitHostKeySource 端口现读）。
// 只读不生成：文件缺失/不可解析（git 面未启用或首启前）如实返回空串。
func (s *GitTriggers) Fingerprint() string {
	raw, err := os.ReadFile(s.hostKeyPath()) //nolint:gosec // G304：路径来自平台配置（git.host_key_file）
	if err != nil {
		return ""
	}
	signer, err := gossh.ParsePrivateKey(raw)
	if err != nil {
		return ""
	}
	return gossh.FingerprintSHA256(signer.PublicKey())
}

// reconcileHostKeyFingerprint 把装载指纹收敛进台账（state 单写点单事务）：
// 首启建账静默、同值无动作、变更（文件重建/换钥）→ 审计 + 事件随台账更新
// 落库。台账写失败只告警——SSH 面照常起服（指纹台账是观测落痕，不阻塞
// git 服务；failure 模式与 fetch 的 first-seen 审计同口径）。
func (s *GitTriggers) reconcileHostKeyFingerprint(ctx context.Context, fingerprint string) {
	changed, err := s.st.ReconcileGitHostKeyFingerprint(ctx, fingerprint)
	if err != nil {
		s.log.Warn("gitserver: host key fingerprint ledger reconcile failed",
			"fingerprint", fingerprint, "error", err.Error())
		return
	}
	if changed {
		s.log.Warn("gitserver: ssh host key changed since the previous load "+
			"(event git.hostkey_changed recorded; clients must re-pin known_hosts)",
			"fingerprint", fingerprint)
	}
}
