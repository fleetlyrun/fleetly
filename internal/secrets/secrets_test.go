package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newKeyFile 在临时目录生成密钥文件（默认权限），返回路径。
func newKeyFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fleetly.key")
}

// TestEncryptDecryptRoundtrip 验收 2：age envelope roundtrip。
func TestEncryptDecryptRoundtrip(t *testing.T) {
	box, created, err := EnsureKey(newKeyFile(t))
	if err != nil {
		t.Fatalf("ensure key: %v", err)
	}
	if !created {
		t.Fatal("first ensure should create key")
	}

	plaintext := []byte("postgres://user:pass@db:5432/app?sslmode=disable")
	ciphertext, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("ciphertext contains plaintext (envelope broken)")
	}
	got, err := box.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}

	// 二次加载（持久复用同一密钥）仍可解。
	reloaded, err := LoadKey(box.Path())
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}
	got2, err := reloaded.Decrypt(ciphertext)
	if err != nil || !bytes.Equal(got2, plaintext) {
		t.Fatalf("reloaded decrypt = %q err=%v", got2, err)
	}
}

// TestWrongKeyCannotDecrypt 错误密钥解密失败，且错误信息不泄露密文/明文。
func TestWrongKeyCannotDecrypt(t *testing.T) {
	boxA, _, err := EnsureKey(newKeyFile(t))
	if err != nil {
		t.Fatalf("key A: %v", err)
	}
	boxB, _, err := EnsureKey(newKeyFile(t))
	if err != nil {
		t.Fatalf("key B: %v", err)
	}

	plaintext := []byte("top-secret-value")
	ciphertext, err := boxA.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	_, err = boxB.Decrypt(ciphertext)
	if err == nil {
		t.Fatal("wrong key decrypted ciphertext")
	}
	msg := err.Error()
	if strings.Contains(msg, string(plaintext)) || strings.Contains(msg, string(ciphertext)) {
		t.Fatalf("decrypt error leaks material: %s", msg)
	}
}

// TestKeyPersistenceAndFormat 主密钥文件 = age 私钥一行（可被 age CLI 识别
// 的标准形态）；二次 Ensure 不再生成（幂等）。
func TestKeyPersistenceAndFormat(t *testing.T) {
	path := newKeyFile(t)
	box, created, err := EnsureKey(path)
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // 测试读取自建临时密钥
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "AGE-SECRET-KEY-1") {
		t.Fatalf("key file not an age X25519 identity: %q", line[:min(len(line), 20)])
	}
	if _, created2, err := EnsureKey(path); err != nil || created2 {
		t.Fatalf("second ensure: created=%v err=%v (should reuse)", created2, err)
	}
	_ = box
}

// TestCorruptKeyRejected 损坏密钥文件拒绝加载（fail-fast，不静默）。
func TestCorruptKeyRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleetly.key")
	if err := os.WriteFile(path, []byte("not-an-age-key\n"), 0o600); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}
	if _, err := LoadKey(path); err == nil {
		t.Fatal("corrupt key accepted")
	}
}

// TestWeakPermissionsRejected 验收 2：主密钥文件权限过宽 → 拒绝启动
// （ErrWeakPermissions）。Windows 无 POSIX 权限语义，检查按实现如实降级，
// 用例在 Windows 上跳过（CI linux 腿真实执行）。
func TestWeakPermissionsRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX file permission semantics (checkKeyPermissions degrades accordingly)")
	}
	path := newKeyFile(t)
	if _, _, err := EnsureKey(path); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302：故意设置过宽权限以验证拒绝路径
		t.Fatalf("chmod: %v", err)
	}
	_, err := LoadKey(path)
	if !errors.Is(err, ErrWeakPermissions) {
		t.Fatalf("err = %v, want ErrWeakPermissions", err)
	}
}

// TestRecipientFingerprint 主公钥形态稳定（age1... 前缀），二次加载一致。
func TestRecipientFingerprint(t *testing.T) {
	path := newKeyFile(t)
	box, _, err := EnsureKey(path)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	r1, err := box.Recipient()
	if err != nil || !strings.HasPrefix(r1, "age1") {
		t.Fatalf("recipient = %q err=%v", r1, err)
	}
	box2, err := LoadKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	r2, _ := box2.Recipient()
	if r1 != r2 {
		t.Fatalf("recipient changed after reload: %s vs %s", r1, r2)
	}
}

// TestSecretRefNamingAndRotation 验收 1/5 的 secrets 侧：引用名逐字对照
// 设计字符串；值轮换 → hash8 变 → 名变（换引用）。
func TestSecretRefNamingAndRotation(t *testing.T) {
	ref, err := SecretRef("my-api", "database_url", "value-v1")
	if err != nil {
		t.Fatalf("ref: %v", err)
	}
	if ref.Name != "fleetly-my-api-database_url-"+ref.Hash8 {
		t.Fatalf("secret name = %s, hash8 = %s", ref.Name, ref.Hash8)
	}
	rotated, err := SecretRef("my-api", "database_url", "value-v2")
	if err != nil {
		t.Fatalf("rotated ref: %v", err)
	}
	if rotated.Hash8 == ref.Hash8 || rotated.Name == ref.Name {
		t.Fatalf("rotation did not change ref: %s vs %s", ref.Name, rotated.Name)
	}
}

// TestNoMaterialInErrors 负面断言（state-model §2.9）：全部构造/加密路径
// 的错误信息不含明文值。
func TestNoMaterialInErrors(t *testing.T) {
	_, err := SecretRef("my-api", "bad name", "leaky-plaintext")
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
	if strings.Contains(err.Error(), "leaky-plaintext") {
		t.Fatalf("error leaks value: %s", err)
	}
	var nilBox *Box
	if _, err := nilBox.Encrypt([]byte("x")); err == nil {
		t.Fatal("nil box encrypt should fail")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
