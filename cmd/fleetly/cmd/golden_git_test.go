package cmd

// golden 快照（T2.19 新动词面）：git keys 与 apps webhook 生命周期。
// 夹具与归一化复用 golden_test.go 的框架（startCLI/compareGolden/normalize
// ——新增 fingerprint 归一规则）。`go test ./cmd/fleetly/cmd -run TestGolden -update`
// 再生成。

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// newEd25519PubKeyFile 生成一对 ed25519 并把公钥写 authorized_keys 单行
// 文件，返回 (路径, 指纹)。
func newEd25519PubKeyFile(t *testing.T, dir string) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_ = priv
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("to ssh key: %v", err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub)))
	path := filepath.Join(dir, "test_key.pub")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path, gossh.FingerprintSHA256(sshPub)
}

// TestGoldenGitKeysLifecycle git keys add/list/rm --json 全生命周期。
func TestGoldenGitKeysLifecycle(t *testing.T) {
	startCLI(t)
	keyPath, fingerprint := newEd25519PubKeyFile(t, t.TempDir())

	code, out, errOut := runCLIConn(t, "git", "keys", "add", "--json", "--note", "operator laptop", keyPath)
	if code != 0 {
		t.Fatalf("add: code=%d stderr=%s", code, errOut)
	}
	var added struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
		t.Fatalf("add resp: %s err=%v", out, err)
	}
	if added.Fingerprint != fingerprint {
		t.Fatalf("fingerprint mismatch: %s vs %s", added.Fingerprint, fingerprint)
	}
	compareGolden(t, "git_keys_add", out)

	code, out, _ = runCLIConn(t, "git", "keys", "list", "--json")
	if code != 0 {
		t.Fatalf("list: code=%d", code)
	}
	compareGolden(t, "git_keys_list", out)

	code, out, _ = runCLIConn(t, "git", "keys", "rm", "--json", added.ID)
	if code != 0 {
		t.Fatalf("rm: code=%d", code)
	}
	compareGolden(t, "git_keys_rm", out)

	// rm 后列表为空（EmitUnpopulated=false 语义：空集不输出 → "{}"）。
	code, out, _ = runCLIConn(t, "git", "keys", "list", "--json")
	if code != 0 || strings.TrimSpace(out) != "{}" {
		t.Fatalf("list after rm: code=%d out=%s", code, out)
	}
}

// TestGoldenAppsWebhook webhook 配置面：set-secret → show → set-source →
// show（无敏感投影——secret 只回 configured 位）。
func TestGoldenAppsWebhook(t *testing.T) {
	env := startCLI(t)
	env.CreateApp(t, "my-api")

	code, out, errOut := runCLIConn(t, "apps", "webhook", "set-secret", "--json", "my-api", "a-webhook-secret-16ch")
	if code != 0 {
		t.Fatalf("set-secret: code=%d stderr=%s", code, errOut)
	}
	compareGolden(t, "apps_webhook_set_secret", out)

	code, out, _ = runCLIConn(t, "apps", "webhook", "show", "--json", "my-api")
	if code != 0 {
		t.Fatalf("show: code=%d", code)
	}
	compareGolden(t, "apps_webhook_show", out)

	code, out, errOut = runCLIConn(t, "apps", "webhook", "set-source", "--json",
		"--branch", "main", "--auth-kind", "none", "my-api", "https://example.com/acme/web.git")
	if code != 0 {
		t.Fatalf("set-source: code=%d stderr=%s", code, errOut)
	}
	compareGolden(t, "apps_webhook_set_source", out)

	// 弱 secret 服务端拒绝（≥16 字符——验签是端点唯一认证）。
	code, _, errOut = runCLIConn(t, "apps", "webhook", "set-secret", "--json", "my-api", "short")
	if code != 1 || !strings.Contains(errOut, "16") {
		t.Fatalf("weak secret: code=%d stderr=%q", code, errOut)
	}
}
