package gitserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 仓库面测试：app 名校验、bare 仓库懒创建 + 钩子生成 + token 轮换、
// compose 双文件拒绝。

func TestValidAppName(t *testing.T) {
	for _, name := range []string{"my-api", "web", "a", "a-1-b", "api123"} {
		if !ValidAppName(name) {
			t.Errorf("ValidAppName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "-lead", "Up", "with.dot", "a/b", "a\\b", "..", "with space", "has_underscore"} {
		if ValidAppName(name) {
			t.Errorf("ValidAppName(%q) = true, want false", name)
		}
	}
}

func TestParseGitCommand(t *testing.T) {
	// 合法形态。
	for _, tc := range []struct {
		cmd, sub, app string
	}{
		{"git-receive-pack 'my-api.git'", "receive-pack", "my-api"},
		{`git-upload-pack "my-api.git"`, "upload-pack", "my-api"},
		{"git-receive-pack my-api.git", "receive-pack", "my-api"},
	} {
		sub, app, err := parseGitCommand(tc.cmd)
		if err != nil || sub != tc.sub || app != tc.app {
			t.Errorf("parseGitCommand(%q) = %q,%q,%v; want %q,%q,nil", tc.cmd, sub, app, err, tc.sub, tc.app)
		}
	}
	// 非法词形（白名单即防线——任何其他命令/路径形态拒绝）。
	for _, cmd := range []string{
		"rm -rf /",
		"git-receive-pack",                     // 无参数
		"git-shell -c x",                       // 非白名单子命令
		"cat /etc/passwd",                      // 非白名单子命令
		"git-receive-pack 'a/b.git'",           // 路径分隔符
		"git-receive-pack '../etc.git'",        // 上跳
		"git-receive-pack 'a b.git'",           // 空格
		"git-upload-pack 'x.git' extra",        // 多参数（第二个词并进路径位）
		"git-receive-pack 'UPPER.git'",         // 名字词形非法
		"git-receive-pack 'a\nb.git'",          // 换行注入
		"git-receive-pack 'unterminated.git\"", // 引号不闭合
		"git-receive-pack '/abs/path.git'",     // 绝对路径
	} {
		if _, _, err := parseGitCommand(cmd); err == nil {
			t.Errorf("parseGitCommand(%q) accepted, want reject", cmd)
		}
	}
}

func TestEnsureBareRepoAndCompose(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	// 首次：懒创建。
	path, created, err := src.EnsureBareRepo(ctx, "my-api")
	if err != nil || !created {
		t.Fatalf("EnsureBareRepo: created=%v err=%v", created, err)
	}
	if fi, err := os.Stat(filepath.Join(path, "HEAD")); err != nil || fi.IsDir() {
		t.Fatalf("bare repo not initialized at %s", path)
	}
	// 钩子在位且含回调端点与 app 路径（token 明文只进钩子文件）。
	hookRaw, err := os.ReadFile(filepath.Join(path, "hooks", "post-receive")) //nolint:gosec // G304：路径为包内 repoPath 拼接的测试受控值
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	hook := string(hookRaw)
	if !strings.Contains(hook, src.Config().HookEndpoint+"/v1/apps/my-api/deployments/git") {
		t.Fatal("hook missing callback endpoint")
	}
	if !strings.Contains(hook, "TOKEN='flthk_") {
		t.Fatal("hook missing hook token (flthk_ form)")
	}
	// 钩子 token 入 tokens 表（deploy scope、系统 actor、按名可识别）。
	tokens, err := st.ListTokens(ctx)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("hook token not persisted: %v", err)
	}
	if tokens[0].Name != hookTokenName("my-api") || tokens[0].Scopes != "deploy" {
		t.Fatalf("hook token row = %+v", tokens[0])
	}

	// 二次：幂等（不重建、不轮换 token——钩子仍在位）。
	_, created2, err := src.EnsureBareRepo(ctx, "my-api")
	if err != nil || created2 {
		t.Fatalf("second EnsureBareRepo: created=%v err=%v", created2, err)
	}
	tokens2, _ := st.ListTokens(ctx)
	if len(tokens2) != 1 {
		t.Fatalf("idempotent call rotated token: %d rows", len(tokens2))
	}

	// 钩子缺失（模拟重建）：重写并轮换 token（旧 token 吊销、新 token 在册）。
	if err := os.Remove(filepath.Join(path, "hooks", "post-receive")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	tokens3, _ := st.ListTokens(ctx)
	if len(tokens3) != 1 || tokens3[0].ID == tokens[0].ID {
		t.Fatalf("rotation expected exactly one NEW token row, got %+v (old %s)", tokens3, tokens[0].ID)
	}
	// 新 token 的明文与旧钩子文件中的不同（轮换即换密）。
	hook2, err := os.ReadFile(filepath.Join(path, "hooks", "post-receive")) //nolint:gosec // G304：同上（测试受控路径）
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hook2), "TOKEN='flthk_") {
		t.Fatal("rewritten hook missing fresh token")
	}
	// 旧 token 已吊销（全量含吊销行的语义由 HasAnyToken 类查询承载——此处
	// 校验在册行只有新 token 即可）。
}

// TestEnsureBareRepoConcurrentTokenConsistency E7③（S19）：并发
// EnsureBareRepo 同 app——per-app 互斥下「钩子内 token ↔ 在册 token」恒
// 一致（旧 TOCTOU 形态：stat 判定与 writeHook/rotateHookToken 的
// 「list→revoke→create」交错可产生失配 token——push 回调永久 401——或多
// 条活 token 残留）。断言两面：每次调用后钩子 token 即时有效 + 终态恰
// 一条在册 token 且与钩子一致。
func TestEnsureBareRepoConcurrentTokenConsistency(t *testing.T) {
	requireGit(t)
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	// 预置「仓库在、钩子缺失」形态：并发调用全部走 writeHook/rotate 路径
	//（TOCTOU 的暴露面）。
	path, _, err := src.EnsureBareRepo(ctx, "race-app")
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if err := os.Remove(filepath.Join(path, "hooks", "post-receive")); err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(path, "hooks", "post-receive")
	hookTokenActive := func() bool {
		raw, err := os.ReadFile(hookPath) //nolint:gosec // G304：包内构造路径的测试受控读取
		if err != nil {
			return false
		}
		tok := hookTokenFromScript(string(raw))
		if tok == "" {
			return false
		}
		tokens, err := st.ListTokens(ctx)
		if err != nil {
			return false
		}
		for _, tk := range tokens {
			if tk.Name == hookTokenName("race-app") && tk.TokenHash == state.HashToken(tok) {
				return true
			}
		}
		return false
	}

	const k = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, _, err := src.EnsureBareRepo(ctx, "race-app"); err != nil {
				t.Errorf("concurrent EnsureBareRepo: %v", err)
				return
			}
			// 即时一致性：调用返回时钩子 token 必然在册有效（旧 TOCTOU 形态
			// 在此处即可观测失配窗口）。
			if !hookTokenActive() {
				t.Errorf("hook token not active immediately after EnsureBareRepo (TOCTOU residue)")
			}
		}()
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.Fatal("concurrent EnsureBareRepo observed hook/db token mismatch (E7③ regression)")
	}

	// 终态：恰一条在册同名 token，且与钩子文件一致。
	if !hookTokenActive() {
		t.Fatal("final hook token not backed by an active token row")
	}
	active := 0
	tokens, err := st.ListTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tokens {
		if tk.Name == hookTokenName("race-app") {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active hook tokens = %d, want exactly 1 (rotation must not leave live residue)", active)
	}
}

func TestComposeFromCommit(t *testing.T) {
	requireGit(t)
	src, _, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	// 正常：单 compose.yaml。
	sourceDir, sha := newSourceRepo(t, composeFixture)
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sourceDir, "push", "--quiet", src.repoPath("my-api"), "main")
	got, err := src.composeFromCommit(ctx, "my-api", sha)
	if err != nil || !strings.Contains(string(got), "nginx:1.27-alpine") {
		t.Fatalf("composeFromCommit: %v (%s)", err, got)
	}

	// 双文件 → 拒绝。
	dir2, _ := newSourceRepo(t, composeFixture)
	if err := os.WriteFile(filepath.Join(dir2, "compose.yml"), []byte(composeFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir2, "add", ".")
	gitRun(t, dir2, "commit", "--quiet", "--amend", "--no-edit")
	shaBoth := gitRun(t, dir2, "rev-parse", "HEAD")
	if _, _, err := src.EnsureBareRepo(ctx, "my-api"); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir2, "push", "--quiet", src.repoPath("my-api"), "main", "--force")
	_, err = src.composeFromCommit(ctx, "my-api", shaBoth)
	if err == nil || !errors.Is(err, errComposeRejected) {
		t.Fatalf("both-files: err=%v, want errComposeRejected", err)
	}

	// 无 compose → 拒绝。
	dir3 := t.TempDir()
	gitRun(t, dir3, "init", "--initial-branch=main", "--quiet")
	if err := os.WriteFile(filepath.Join(dir3, "README.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir3, "add", ".")
	gitRun(t, dir3, "commit", "--quiet", "-m", "no compose")
	sha3 := gitRun(t, dir3, "rev-parse", "HEAD")
	gitRun(t, dir3, "push", "--quiet", src.repoPath("my-api"), "main", "--force")
	_, err = src.composeFromCommit(ctx, "my-api", sha3)
	if err == nil || !errors.Is(err, errComposeRejected) {
		t.Fatalf("no-compose: err=%v, want errComposeRejected", err)
	}

	// 形态防御：非法 sha 拒绝。
	if _, err := src.composeFromCommit(ctx, "my-api", "not-a-sha"); err == nil {
		t.Fatal("invalid sha accepted")
	}
}
