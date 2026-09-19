package gitserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// webhook 拉源（T2.19）：webhook 触发后 daemon 对该 app 的 bare 仓库执行
// git fetch（app 配置 source：url + branch + 认证形态 none|https_token|
// ssh_key）。认证材料 envelope 密文落库，消费时才解密：
//   - https_token → GIT_CONFIG_* 环境变量注入 http.extraHeader（凭据不进
//     进程参数位，ps 不可见——比 -c 形态收敛一个暴露面）；
//   - ssh_key → 私钥写临时文件（0600）+ GIT_SSH_COMMAND 指向；known_hosts
//     语义 v0.1 取 accept-new（首连自动收录，不再交互确认）——单操作员
//     口径成立，TOFU 时刻以 git.hostkey_first_seen 审计落痕（整改④：
//     known_hosts 目标文件经 UserKnownHostsFile 钉在 <git.root>/known_hosts，
//     fetch 前后行集对比，新增指纹行即入审计）。
//
// 远端不可达/fetch 失败 → ErrFetchFailed（api/webhook 层映射 E_RUNTIME_
// UNAVAILABLE——errcode 零新增，复用既有管线错误族）。
//
// URL 协议白名单（整改②）：source url 在进入 git 命令行前前置收口——
// git 的 ext::/fd:: 传输伪协议可达 daemon 权限任意命令执行，`-` 前缀是
// 参数注入形态；git 命令侧再以 -c protocol.ext.allow=never /
// protocol.fd.allow=never 纵深一层（不设全局 protocol.allow=never——会
// 连带杀掉 file://，那是 v0.1 有意保留的本地裸仓库形态）。

// ErrFetchFailed 是拉源失败的包内哨兵。
var ErrFetchFailed = errors.New("git fetch failed")

// ErrSourceNotConfigured 表示 app 未配置拉源 source（webhook 拉源前置）。
var ErrSourceNotConfigured = errors.New("app source not configured")

// sourceURLPrefixes 是拉源 URL 的协议白名单（整改②，绑定口径）：
// https/http/ssh + git@（scp 词形）+ file:/// —— file 为 v0.1 有意保留
// （单操作员口径：operator 即 root，本地裸仓库形态供测试与 journey 使用，
// 无多租户越权面）；其余形态一律拒绝。
var sourceURLPrefixes = []string{"https://", "http://", "ssh://", "file:///", "git@"}

// validateSourceURL 校验拉源 URL 白名单：`-` 开头拒绝（参数注入形态）；
// 前缀不在白名单内拒绝（错误信息点名 ext::/fd:: 禁用）；scheme:// 之后的
// authority 组件（user@host 两段）任一以 `-` 开头拒绝（R4 残面收口——
// ssh://-oProxyCommand=...@h/repo 的首部本 body 合法，`-` 词形进入 ssh
// 目的地/参数位）。
func validateSourceURL(raw string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("gitserver: source url must not start with '-' (option-injection form rejected): %q", raw)
	}
	for _, p := range sourceURLPrefixes {
		if strings.HasPrefix(raw, p) {
			return validateSourceURLAuthority(raw)
		}
	}
	return fmt.Errorf("gitserver: source url scheme not allowed: %q (allowed prefixes: https://, http://, ssh://, git@, file:///; "+
		"git transport pseudo-protocols ext:: and fd:: are disabled)", raw)
}

// validateSourceURLAuthority 校验 URL authority 组件（R4）：`://` 之后
// （git@ scp 词形则剥掉前缀后）到首个 `/`、`?`、`#` 之前的段按 user@host
// 拆分（@ 取最后一个），user 与 host 任一以 `-` 开头即拒绝；file:/// 的
// 空 authority 天然通过。
func validateSourceURLAuthority(raw string) error {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+len("://"):]
	}
	rest = strings.TrimPrefix(rest, "git@")
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	user, host := "", rest
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		user, host = rest[:i], rest[i+1:]
	}
	for _, part := range []string{user, host} {
		if strings.HasPrefix(part, "-") {
			return fmt.Errorf("gitserver: source url authority component must not start with '-' "+
				"(option-injection form rejected): %q", raw)
		}
	}
	return nil
}

// sourceURLHost 提取拉源 URL 的主机标识（hostkey 审计 diff_summary 用）：
// ssh://[user@]host[:port]/path → host[:port]；git@host:path（scp 词形）→
// host。提取失败返回空串（审计仍落，仅缺 host 字段——观测面降级不阻断）。
func sourceURLHost(raw string) string {
	if strings.HasPrefix(raw, "ssh://") {
		// ssh:// 形态 net/url 可解析；Host 保留端口（known_hosts 非 22 端口
		// 条目即 [host]:port 词形，与审计口径一致）。
		if u, err := url.Parse(raw); err == nil {
			return u.Host
		}
		return ""
	}
	if strings.HasPrefix(raw, "git@") {
		rest := strings.TrimPrefix(raw, "git@")
		if i := strings.IndexByte(rest, ':'); i > 0 {
			return rest[:i]
		}
	}
	return ""
}

// fetchPlan 是一次拉源的执行计划（构造与执行分离——认证材料形态的单元
// 测试断言构造面，不真实触网）。
type fetchPlan struct {
	// RepoPath 是 bare 仓库路径（exec 的 cwd）。
	RepoPath string
	// Args 是 git fetch 参数（-c 传输协议禁用对 + fetch --quiet <url>
	// <refspec>）。
	Args []string
	// Env 是追加的环境变量（GIT_TERMINAL_PROMPT=0 恒在）。
	Env []string
	// Cleanup 释放临时资源（ssh_key 临时私钥文件）；可为 nil。
	Cleanup func()
	// KnownHostsFile 是 ssh_key 形态下 GIT_SSH_COMMAND 钉住的 known_hosts
	// 目标文件（整改④：TOFU 首连指纹入审计的观测对象）；非 ssh 形态为空。
	KnownHostsFile string
	// RemoteHost 是拉源 URL 的主机标识（审计 diff_summary 用；提取失败为空）。
	RemoteHost string
}

// buildFetch 构造拉源计划（认证材料在此解密；不执行任何 git 命令）。
// appID 必须已存在；source url 未配置返回 ErrSourceNotConfigured，协议
// 白名单外返回错误（整改②）。
func (s *Source) buildFetch(ctx context.Context, appID, repoPath string) (fetchPlan, error) {
	cfg, err := s.st.GetAppGitConfig(ctx, appID)
	if err != nil {
		return fetchPlan{}, fmt.Errorf("gitserver: read app source config: %w", err)
	}
	if cfg.SourceURL == "" {
		return fetchPlan{}, ErrSourceNotConfigured
	}
	if err := validateSourceURL(cfg.SourceURL); err != nil {
		return fetchPlan{}, err
	}
	branch := cfg.Branch
	if branch == "" {
		branch = state.DefaultGitBranch
	}
	knownHosts := filepath.Join(s.cfg.Root, "known_hosts")
	plan := fetchPlan{
		RepoPath: repoPath,
		Args: []string{
			// 传输层纵深（整改②）：白名单之外再从 git 协议层禁掉 ext/fd
			// 伪协议（config 域内精确禁用，不碰全局 protocol.allow——file://
			// 必须存活）。
			"-c", "protocol.ext.allow=never",
			"-c", "protocol.fd.allow=never",
			"fetch", "--quiet",
			cfg.SourceURL,
			"+refs/heads/" + branch + ":refs/heads/" + branch,
		},
		Env:        []string{"GIT_TERMINAL_PROMPT=0"},
		RemoteHost: sourceURLHost(cfg.SourceURL),
	}
	switch cfg.AuthKind {
	case state.SourceAuthToken:
		token, derr := s.box.Decrypt([]byte(cfg.AuthSecret))
		if derr != nil {
			return fetchPlan{}, fmt.Errorf("gitserver: decrypt https_token: %w", derr)
		}
		plan.Env = append(plan.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: token "+string(token),
		)
	case state.SourceAuthSSHKey:
		key, derr := s.box.Decrypt([]byte(cfg.AuthSecret))
		if derr != nil {
			return fetchPlan{}, fmt.Errorf("gitserver: decrypt ssh_key: %w", derr)
		}
		keyFile, derr := writeTempKey(key)
		if derr != nil {
			return fetchPlan{}, derr
		}
		plan.Cleanup = func() { _ = os.Remove(keyFile) }
		// UserKnownHostsFile 钉住收录目标（整改④的观测面）：accept-new 的
		// TOFU 收录落在平台数据根内（daemon 可控、可审计），而非 ssh 默认
		// 的 ~/.ssh/known_hosts（systemd ProtectHome 下不可写且无从对账）。
		plan.KnownHostsFile = knownHosts
		plan.Env = append(plan.Env,
			"GIT_SSH_COMMAND=ssh -i "+keyFile+" -o UserKnownHostsFile="+knownHosts+
				" -o StrictHostKeyChecking=accept-new -o BatchMode=yes")
	case state.SourceAuthNone:
		// 匿名拉取，无附加材料。
	}
	return plan, nil
}

// FetchRemote 执行拉源：确保 bare 仓库在位 → 按计划 fetch。失败一律包装
// ErrFetchFailed（调用方映射错误族信封）。
func (s *Source) FetchRemote(ctx context.Context, app string) error {
	path, _, err := s.EnsureBareRepo(ctx, app)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	appRow, err := s.st.GetAppByName(ctx, app)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	plan, err := s.buildFetch(ctx, appRow.ID, path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	defer func() {
		if plan.Cleanup != nil {
			plan.Cleanup()
		}
	}()
	// 整改④：accept-new 首连静默收录的 TOFU 时刻必须落审计——fetch 前
	// 记录 known_hosts 行集，fetch 后对比，新增指纹行即写审计。
	var khBefore map[string]struct{}
	if plan.KnownHostsFile != "" {
		khBefore = snapshotKnownHosts(plan.KnownHostsFile)
	}
	if err := s.fetchFn(ctx, plan); err != nil {
		s.log.Warn("gitserver: fetch remote failed", "app", app, "error", err.Error())
		return fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	if plan.KnownHostsFile != "" {
		s.auditHostKeyFirstSeen(ctx, appRow.ID, app, plan.RemoteHost, plan.KnownHostsFile, khBefore)
	}
	return nil
}

// runFetch 执行拉源计划（cwd = bare 仓库；env 追加在进程环境之上）。
func runFetch(ctx context.Context, plan fetchPlan) error {
	cmd := exec.CommandContext(ctx, "git", plan.Args...) //nolint:gosec // G204：参数为包内构造词形（-c 协议禁用对 + fetch + 白名单校验过的 url/refspec）
	cmd.Dir = plan.RepoPath
	cmd.Env = append(os.Environ(), plan.Env...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, errOut.String())
	}
	return nil
}

// snapshotKnownHosts 读取 known_hosts 目标文件的行集（④ 整改的对照基准；
// 文件不存在/不可读 → nil——首连前即此形态）。
func snapshotKnownHosts(path string) map[string]struct{} {
	raw, err := os.ReadFile(path) //nolint:gosec // G304：路径来自包内构造（<git.root>/known_hosts），非不可信输入
	if err != nil {
		return nil
	}
	out := make(map[string]struct{})
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = struct{}{}
	}
	return out
}

// auditHostKeyFirstSeen 对比 fetch 前后的 known_hosts 行集：出现新增行
// （accept-new 首连收录即此形态）→ 写 git.hostkey_first_seen 审计
// （actor=system；target 与既有 app:<id> 口径一致；diff_summary 带 host +
// 新增指纹行；审计动作词不入 errcode/eventcode 注册表——审计词汇不受
// 注册表约束）。审计写失败只告警——拉源已成功，审计是观测落痕，不回滚
// 已完成的事实。
//
// 已知保真度边界（二轮 R5 落档，接受不改）：known_hosts 为共享单文件且
// 本函数的「快照 → fetch → 对比」区间无锁——并发 ssh 拉源（多 app 同时
// 首连同一 host 或不同 host）可能串账/漏账审计（两请求快照到同一前态，
// 新增行被重复记或互相吞掉）。这是审计保真度问题，非控制面伤；按 host
// 粒度对账或对快照-对比区间加锁留 v0.2。
func (s *Source) auditHostKeyFirstSeen(ctx context.Context, appID, app, host, knownHostsFile string, before map[string]struct{}) {
	after := snapshotKnownHosts(knownHostsFile)
	if len(after) == 0 {
		return
	}
	var added []string
	for line := range after {
		if _, ok := before[line]; !ok {
			added = append(added, line)
		}
	}
	if len(added) == 0 {
		return
	}
	sort.Strings(added)
	var b strings.Builder
	fmt.Fprintf(&b, `{"app":%q,"host":%q,"added":[`, app, host)
	for i, line := range added {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q", line)
	}
	b.WriteString("]}")
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "git.hostkey_first_seen",
			Target:      "app:" + appID,
			Result:      "ok",
			DiffSummary: b.String(),
		})
	}); err != nil {
		s.log.Warn("gitserver: hostkey first-seen audit write failed", "app", app, "error", err.Error())
		return
	}
	s.log.Info("gitserver: ssh host key first seen (TOFU recorded)",
		"app", app, "host", host, "known_hosts", knownHostsFile, "added", len(added))
}

// writeTempKey 把私钥材料写临时文件（0600；调用方 Cleanup 删除）。
func writeTempKey(key []byte) (string, error) {
	dir, err := os.MkdirTemp("", "fleetly-gitkey-")
	if err != nil {
		return "", fmt.Errorf("gitserver: create key temp dir: %w", err)
	}
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, key, 0o600); err != nil { //nolint:gosec // G306：私钥临时文件 0600 即要求
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("gitserver: write key temp file: %w", err)
	}
	return path, nil
}
