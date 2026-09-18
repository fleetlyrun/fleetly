package gitserver

import (
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

// ensureHostKey 装载（必要时生成）host key 并返回 signer。
func (s *Source) ensureHostKey() (gossh.Signer, error) {
	path := s.cfg.HostKeyFile
	if path == "" {
		path = filepath.Join(s.cfg.Root, "host_ed25519")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304：路径来自平台配置（git.host_key_file）
	if err == nil {
		signer, perr := gossh.ParsePrivateKey(raw)
		if perr != nil {
			return nil, fmt.Errorf("gitserver: parse host key %s: %w", path, perr)
		}
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
	return signer, nil
}
