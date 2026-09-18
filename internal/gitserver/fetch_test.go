package gitserver

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 拉源计划构造测试（认证材料形态的单元断言面——构造与执行分离，不真实
// 触网）：none / https_token / ssh_key 三态 + 未配置拒绝 + 解密往返。

func TestBuildFetchAuthKinds(t *testing.T) {
	src, st, box, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "my-api")
	if err != nil {
		t.Fatal(err)
	}

	// 未配置 source → ErrSourceNotConfigured。
	if _, err := src.buildFetch(ctx, app.ID, "/repo"); err == nil || err != ErrSourceNotConfigured {
		t.Fatalf("unconfigured err = %v, want ErrSourceNotConfigured", err)
	}

	// none：恒有 GIT_TERMINAL_PROMPT=0，无附加材料。
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "https://example.com/acme/web.git", Branch: "main", AuthKind: state.SourceAuthNone,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := src.buildFetch(ctx, app.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if plan.RepoPath != "/repo" || len(plan.Args) != 4 || plan.Args[0] != "fetch" {
		t.Fatalf("plan = %+v", plan)
	}
	if !strings.Contains(plan.Args[3], "refs/heads/main:refs/heads/main") {
		t.Fatalf("refspec = %s", plan.Args[3])
	}
	if len(plan.Env) != 1 || plan.Env[0] != "GIT_TERMINAL_PROMPT=0" {
		t.Fatalf("none env = %v", plan.Env)
	}

	// https_token：token 经 GIT_CONFIG_* 环境注入（不进进程参数位）。
	cipher, err := box.Encrypt([]byte("tok-abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "https://example.com/acme/web.git", Branch: "release",
		AuthKind: state.SourceAuthToken, AuthSecret: string(cipher),
	}); err != nil {
		t.Fatal(err)
	}
	plan, err = src.buildFetch(ctx, app.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan.Env, "\n")
	for _, want := range []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "Authorization: token tok-abc123"} {
		if !strings.Contains(joined, want) {
			t.Errorf("https_token env missing %q: %v", want, plan.Env)
		}
	}
	if strings.Contains(strings.Join(plan.Args, " "), "tok-abc123") {
		t.Error("token leaked into git args")
	}
	if plan.Cleanup != nil {
		t.Error("token path cleanup set unexpectedly")
	}

	// ssh_key：私钥写临时文件（0600），GIT_SSH_COMMAND 指向。
	keyCipher, err := box.Encrypt([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nTEST\n-----END OPENSSH PRIVATE KEY-----\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "ssh://git@example.com/acme/web.git", Branch: "main",
		AuthKind: state.SourceAuthSSHKey, AuthSecret: string(keyCipher),
	}); err != nil {
		t.Fatal(err)
	}
	plan, err = src.buildFetch(ctx, app.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(plan.Env, "\n")
	if !strings.Contains(joined, "GIT_SSH_COMMAND=ssh -i ") ||
		!strings.Contains(joined, "StrictHostKeyChecking=accept-new") ||
		!strings.Contains(joined, "BatchMode=yes") {
		t.Errorf("ssh_key env = %v", plan.Env)
	}
	for _, e := range plan.Env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=ssh -i ") {
			keyFile := strings.SplitN(strings.TrimPrefix(e, "GIT_SSH_COMMAND=ssh -i "), " ", 2)[0]
			if !fileExists(keyFile) {
				t.Errorf("temp key file missing: %s", keyFile)
			}
		}
	}
	plan.Cleanup()
}

// fileExists 是测试用存在性探测。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
