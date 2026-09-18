package gitserver

import (
	"os"
	"path/filepath"
	"testing"
)

// 配置与状态层加法面的单元断言。

func TestConfigNormalize(t *testing.T) {
	cfg := Config{}.Normalize()
	if cfg.Addr != DefaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Addr, DefaultAddr)
	}
	if cfg.ReplayTTL != DefaultReplayTTL {
		t.Errorf("ReplayTTL = %s, want %s", cfg.ReplayTTL, DefaultReplayTTL)
	}
	if err := (Config{Enabled: true}).Validate(); err == nil {
		t.Error("empty root accepted in enabled mode")
	}
	if err := (Config{Enabled: true, Root: "/x"}).Validate(); err == nil {
		t.Error("empty hook endpoint accepted in enabled mode")
	}
	if err := (Config{Enabled: false}).Validate(); err != nil {
		t.Errorf("disabled mode validate = %v", err)
	}
	if err := (Config{Enabled: true, Root: "/x", HookEndpoint: "http://127.0.0.1:8420"}).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestHostKeyReuse(t *testing.T) {
	src, _, _, dir := newTestSource(t, 0)
	src.cfg.Root = dir // host key 落在 Root 下（缺省路径形态）
	s1, err := src.ensureHostKey()
	if err != nil {
		t.Fatalf("ensureHostKey: %v", err)
	}
	s2, err := src.ensureHostKey()
	if err != nil {
		t.Fatalf("second ensureHostKey: %v", err)
	}
	// 二次装载复用同一私钥（公钥字节等值 = 指纹等值；known_hosts 稳定）。
	if string(s1.PublicKey().Marshal()) != string(s2.PublicKey().Marshal()) {
		t.Fatal("host key regenerated on second start")
	}
	// 私钥文件已持久化（缺省路径 = <Root>/host_ed25519）。
	if _, err := os.Stat(filepath.Join(dir, "host_ed25519")); err != nil {
		t.Fatalf("host key file missing: %v", err)
	}
}
