package gitserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// 拉源计划构造测试（认证材料形态的单元断言面——构造与执行分离，不真实
// 触网）：none / https_token / ssh_key 三态 + 未配置拒绝 + 解密往返 +
// URL 协议白名单（整改②）+ TOFU 首连审计链（整改④）。

func TestBuildFetchAuthKinds(t *testing.T) {
	src, st, box, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "my-api")
	if err != nil {
		t.Fatal(err)
	}

	// 未配置 source → ErrSourceNotConfigured。
	if _, err := src.buildFetch(ctx, app.ID, "/repo"); err == nil || err != ErrSourceNotConfigured {
		t.Fatalf("unconfigured err = %v, want ErrSourceNotConfigured", err)
	}

	// none：恒有 GIT_TERMINAL_PROMPT=0，无附加材料；git 参数携带传输协议
	// 禁用对（整改②纵深：ext/fd 伪协议从协议层禁用）。
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "https://example.com/acme/web.git", Branch: "main", AuthKind: state.SourceAuthNone,
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := src.buildFetch(ctx, app.ID, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if plan.RepoPath != "/repo" || len(plan.Args) != 8 || plan.Args[4] != "fetch" {
		t.Fatalf("plan = %+v", plan)
	}
	wantArgs := []string{"-c", "protocol.ext.allow=never", "-c", "protocol.fd.allow=never"}
	if got := strings.Join(plan.Args[:4], " "); got != strings.Join(wantArgs, " ") {
		t.Fatalf("protocol deny args = %q, want %q", got, strings.Join(wantArgs, " "))
	}
	if !strings.Contains(plan.Args[7], "refs/heads/main:refs/heads/main") {
		t.Fatalf("refspec = %s", plan.Args[7])
	}
	if len(plan.Env) != 1 || plan.Env[0] != "GIT_TERMINAL_PROMPT=0" {
		t.Fatalf("none env = %v", plan.Env)
	}
	if plan.KnownHostsFile != "" {
		t.Fatalf("none auth must not set known_hosts target, got %q", plan.KnownHostsFile)
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

	// ssh_key：私钥写临时文件（0600），GIT_SSH_COMMAND 指向；known_hosts
	// 目标文件钉在 <git.root>/known_hosts（整改④的审计观测对象）。
	keyCipher, err := box.Encrypt([]byte("-----BEGIN OPENSSH PRIVATE KEY-----\nTEST\n-----END OPENSSH PRIVATE KEY-----\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "ssh://git@example.com:2222/acme/web.git", Branch: "main",
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
	if !strings.Contains(joined, "UserKnownHostsFile=") {
		t.Errorf("ssh_key env missing UserKnownHostsFile pin: %v", plan.Env)
	}
	if want := filepath.Join(src.Config().Root, "known_hosts"); plan.KnownHostsFile != want {
		t.Fatalf("known_hosts target = %q, want %q", plan.KnownHostsFile, want)
	}
	// 非标准端口的主机标识保留端口（known_hosts [host]:port 词形口径）。
	if plan.RemoteHost != "example.com:2222" {
		t.Fatalf("remote host = %q, want example.com:2222", plan.RemoteHost)
	}
	var sshCmd, keyFile string
	for _, e := range plan.Env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=ssh -i ") {
			sshCmd = strings.TrimPrefix(e, "GIT_SSH_COMMAND=")
			quoted := strings.SplitN(strings.TrimPrefix(e, "GIT_SSH_COMMAND=ssh -i "), " ", 2)[0]
			keyFile = strings.Trim(quoted, "'")
		}
	}
	if keyFile == "" || !fileExists(keyFile) {
		t.Errorf("temp key file missing: %q", keyFile)
	}
	// M5-10：keyFile 与 known_hosts 都必须是单引号词形（含空格路径不碎裂）。
	if !strings.Contains(sshCmd, "-i '"+keyFile+"'") {
		t.Errorf("ssh -i not in quoted form: %s", sshCmd)
	}
	if !strings.Contains(sshCmd, "-o UserKnownHostsFile='") {
		t.Errorf("UserKnownHostsFile not in quoted form: %s", sshCmd)
	}
	// H3：SSH 传输层超时三件套在位。
	for _, want := range []string{
		"-o ConnectTimeout=15", "-o ServerAliveInterval=30", "-o ServerAliveCountMax=3",
	} {
		if !strings.Contains(sshCmd, want) {
			t.Errorf("ssh command missing timeout option %q: %s", want, sshCmd)
		}
	}
	// M5-8：Cleanup 是目录级回收——key 所在临时目录整体消失。
	plan.Cleanup()
	if _, err := os.Stat(filepath.Dir(keyFile)); !os.IsNotExist(err) {
		t.Errorf("temp key dir left behind: %s", filepath.Dir(keyFile))
	}
}

// TestBuildFetchHTTPSOnlyForTokenAuth E7⑤（S19）：https_token 认证强制
// https:// 源——http:// + token 的组合在拉源计划构造期拒绝（明文链路会把
// token 泄露给窃听者）；http:// + none 的匿名明文拉取仍允许（本地/journey
// 形态）。
func TestBuildFetchHTTPSOnlyForTokenAuth(t *testing.T) {
	src, st, box, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "tok-app")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.Encrypt([]byte("token-at-least-16ch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "http://example.com/acme/web.git", Branch: "main",
		AuthKind: state.SourceAuthToken, AuthSecret: string(cipher),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.buildFetch(ctx, app.ID, "/repo"); err == nil || !strings.Contains(err.Error(), "https_token auth requires https://") {
		t.Fatalf("http + https_token err = %v, want scheme rejection", err)
	}
	// 匿名 http 仍允许（无凭据可泄露）。
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "http://example.com/acme/web.git", Branch: "main", AuthKind: state.SourceAuthNone,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.buildFetch(ctx, app.ID, "/repo"); err != nil {
		t.Fatalf("http + none must stay allowed: %v", err)
	}
}

// TestSourceURLWhitelist URL 协议白名单（整改②验收）：ext::/fd:: 传输伪
// 协议与 `-` 参数注入形态拒绝且错误信息点名；https/http/ssh/git@/file///
// 放行（file 为 v0.1 有意保留的本地裸仓库形态）。
func TestSourceURLWhitelist(t *testing.T) {
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "wl-app")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		url     string
		wantErr bool
		errHit  string // 期望命中的错误信息片段（wantErr 时核对）
	}{
		{"ext::sh -c id", true, "ext::"},
		{"fd::/dev/stdin", true, "fd::"},
		{"--upload-pack=evil https://x", true, "'-'"},
		{"-u=evil", true, "'-'"},
		{"git://example.com/acme/web.git", true, "scheme not allowed"},
		// R4 残面收口：authority 组件（user/host 段）`-` 开头拒绝。
		{"ssh://-oProxyCommand=x@h/repo", true, "authority"},
		{"ssh://user@-evil/repo", true, "authority"},
		{"git@-evil/acme/web.git", true, "authority"},
		{"https://-evil.example.com/acme/web.git", true, "authority"},
		{"http://user@-host/path.git", true, "authority"},
		{"https://example.com/acme/web.git", false, ""},
		{"http://example.com/acme/web.git", false, ""},
		{"ssh://git@example.com/acme/web.git", false, ""},
		{"ssh://git@example.com:2222/acme/web.git", false, ""},
		{"git@example.com:acme/web.git", false, ""},
		{"file:///var/lib/fleetly/git/repo.git", false, ""},
	}
	for _, tc := range cases {
		if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
			URL: tc.url, Branch: "main", AuthKind: state.SourceAuthNone,
		}); err != nil {
			t.Fatalf("SetAppSource %q: %v", tc.url, err)
		}
		_, err := src.buildFetch(ctx, app.ID, "/repo")
		if tc.wantErr {
			if err == nil {
				t.Errorf("url %q: want rejection, got plan", tc.url)
				continue
			}
			if tc.errHit != "" && !strings.Contains(err.Error(), tc.errHit) {
				t.Errorf("url %q: error %q missing %q", tc.url, err.Error(), tc.errHit)
			}
			continue
		}
		if err != nil {
			t.Errorf("url %q: want allowed, got %v", tc.url, err)
		}
	}
}

// TestSourceURLHost 主机标识提取（审计 diff_summary 字段面）。
func TestSourceURLHost(t *testing.T) {
	cases := []struct{ url, want string }{
		{"ssh://git@example.com/acme/web.git", "example.com"},
		{"ssh://git@example.com:2222/acme/web.git", "example.com:2222"},
		{"git@example.com:acme/web.git", "example.com"},
		{"https://example.com/acme/web.git", ""},
		{"file:///tmp/repo.git", ""},
		{"git@no-colon", ""},
	}
	for _, tc := range cases {
		if got := sourceURLHost(tc.url); got != tc.want {
			t.Errorf("sourceURLHost(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestFetchHostKeyFirstSeenAudit TOFU 首连审计链（整改④验收）：首次 ssh
// 拉源后 known_hosts 出现新增指纹行 → git.hostkey_first_seen 审计落地
// （actor=system，diff_summary 带 host + 新增行）；二次拉源无新增 → 不
// 重复记。fetch 执行步骤经接缝注入（不真实触网）：注入函数模拟 accept-new
// 的收录行为（首次追加一行指纹）。
func TestFetchHostKeyFirstSeenAudit(t *testing.T) {
	src, st, box, dir := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "ssh-app")
	if err != nil {
		t.Fatal(err)
	}
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
	// bare 仓库与 known_hosts 目标文件路径就位（EnsureBareRepo 产出 git 根
	// 下的 <app>.git；known_hosts 由注入的 fetch 步骤模拟 ssh 收录写出）。
	khFile := filepath.Join(src.Config().Root, "known_hosts")
	injectedLine := "example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestFingerprintLine"
	src.fetchFn = func(_ context.Context, plan fetchPlan) error {
		if plan.KnownHostsFile != khFile {
			t.Errorf("plan known_hosts = %q, want %q", plan.KnownHostsFile, khFile)
		}
		f, err := os.OpenFile(khFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304：测试受控路径
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = f.WriteString(injectedLine + "\n")
		return err
	}

	if err := src.FetchRemote(ctx, "ssh-app"); err != nil {
		t.Fatalf("first FetchRemote: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, a := range audits {
		if a.Action == "git.hostkey_first_seen" {
			found++
			if a.Actor != "system" || a.Result != "ok" {
				t.Errorf("hostkey audit actor/result = %s/%s, want system/ok", a.Actor, a.Result)
			}
			if !strings.Contains(a.DiffSummary, `"host":"example.com"`) ||
				!strings.Contains(a.DiffSummary, injectedLine) {
				t.Errorf("hostkey audit diff_summary = %s, want host + added fingerprint line", a.DiffSummary)
			}
			if a.Target != "app:"+app.ID {
				t.Errorf("hostkey audit target = %s, want app:%s", a.Target, app.ID)
			}
		}
	}
	if found != 1 {
		t.Fatalf("git.hostkey_first_seen audit count = %d, want 1", found)
	}

	// 二次拉源：known_hosts 无新增行 → 不重复记。
	if err := src.FetchRemote(ctx, "ssh-app"); err != nil {
		t.Fatalf("second FetchRemote: %v", err)
	}
	audits, err = st.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	found = 0
	for _, a := range audits {
		if a.Action == "git.hostkey_first_seen" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("git.hostkey_first_seen audit count after second fetch = %d, want 1 (no duplicate)", found)
	}
	_ = dir
}

// TestFetchHostKeyAuditSkippedForNonSSH 非 ssh 形态（https_token）不设
// known_hosts 观测对象，fetch 后不产生 hostkey 审计——观测面只对 ssh 拉源。
func TestFetchHostKeyAuditSkippedForNonSSH(t *testing.T) {
	src, st, box, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "token-app")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := box.Encrypt([]byte("tok-abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppSource(ctx, app.ID, state.AppSourceWrite{
		URL: "https://example.com/acme/web.git", Branch: "main",
		AuthKind: state.SourceAuthToken, AuthSecret: string(cipher),
	}); err != nil {
		t.Fatal(err)
	}
	src.fetchFn = func(context.Context, fetchPlan) error { return nil } // 注入空 fetch
	if err := src.FetchRemote(ctx, "token-app"); err != nil {
		t.Fatalf("FetchRemote: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range audits {
		if a.Action == "git.hostkey_first_seen" {
			t.Fatal("non-ssh fetch must not write hostkey audit")
		}
	}
}

// gitKeyTempDirs 返回当前残留的 fleetly-gitkey-* 临时目录集（M5-8 零残留
// 断言的 MkdirTemp 计数法：对比 fetch 前后集合，新目录即残留）。
func gitKeyTempDirs(t *testing.T) map[string]struct{} {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "fleetly-gitkey-*"))
	if err != nil {
		t.Fatalf("glob gitkey temp dirs: %v", err)
	}
	out := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		out[m] = struct{}{}
	}
	return out
}

// TestFetchSSHKeyFailureLeavesNoTempResidue M5-8/M5-10/H3 回归：ssh_key 拉源
// 失败（fetch 执行步骤注入失败）后，临时私钥目录必须整目录零残留——
// Cleanup 经 FetchRemote 的 defer 无条件执行（成功/失败路径都到），且回收
// 粒度是目录（os.RemoveAll）而非单个 key 文件。
func TestFetchSSHKeyFailureLeavesNoTempResidue(t *testing.T) {
	requireGit(t) // EnsureBareRepo 经 execGit 真实建仓
	src, st, box, _ := newTestSource(t, 0)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "ssh-residue")
	if err != nil {
		t.Fatal(err)
	}
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
	before := gitKeyTempDirs(t)
	src.fetchFn = func(context.Context, fetchPlan) error { return errors.New("simulated fetch failure") }

	if err := src.FetchRemote(ctx, "ssh-residue"); err == nil || !errors.Is(err, ErrFetchFailed) {
		t.Fatalf("fetch err = %v, want ErrFetchFailed", err)
	}
	after := gitKeyTempDirs(t)
	for dir := range after {
		if _, ok := before[dir]; !ok {
			t.Errorf("temp private key dir left behind after a failed fetch (Cleanup did not reclaim the whole directory): %s", dir)
		}
	}
}

// fileExists 是测试用存在性探测。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
