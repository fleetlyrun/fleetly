package gitserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// bare 仓库管理与 git 对象库读取（全部 exec 系统 git——宿主 git 为前置
// 条件；无 shell 参与，参数白名单形态注入）。

// appNamePattern 是 app 名严格校验（DNS 类词形；点与路径分隔符不允许
// ——仓库路径 = <root>/<app>.git，校验即路径注入防线）。
var appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// shaPattern 是 git commit 的严格校验（40 位十六进制）。
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidAppName 报告 app 名是否可用于 bare 仓库路径。
func ValidAppName(name string) bool { return appNamePattern.MatchString(name) }

// ValidSHA 报告 commit 是否为 40 位十六进制。
func ValidSHA(sha string) bool { return shaPattern.MatchString(sha) }

// repoPath 返回 app 的 bare 仓库路径（app 名已经 ValidAppName 校验）。
func (s *GitTriggers) repoPath(app string) string {
	return filepath.Join(s.cfg.Root, app+".git")
}

// EnsureBareRepo 懒创建 app 的 bare 仓库并确保 post-receive 钩子在位：
//   - 仓库缺失 → git init --bare + 新钩子 token + 写钩子（0600 + POSIX
//     可执行位；明文 token 只写进钩子文件）；
//   - 仓库在、钩子缺失（仓库重建/手工误删）→ 轮换钩子 token（按名吊销
//     旧 token）+ 重写钩子——daemon 重建仓库时轮换的绑定语义。
//
// 幂等；返回仓库路径与是否发生了创建。E7③（S19）：全程持 per-app 互斥
// （stat 判定与 init/钩子写入/token 轮换之间的 TOCTOU 收口——并发同 app
// 调用方 SSH push/webhook 拉源/管理面交错时不再产生失配 token）。
func (s *GitTriggers) EnsureBareRepo(ctx context.Context, app string) (string, bool, error) {
	if !ValidAppName(app) {
		return "", false, fmt.Errorf("gitserver: invalid app name %q", app)
	}
	unlock := s.lockRepo(app)
	defer unlock()
	if err := os.MkdirAll(s.cfg.Root, 0o750); err != nil {
		return "", false, fmt.Errorf("gitserver: create git root %s: %w", s.cfg.Root, err)
	}
	path := s.repoPath(app)
	hookPath := filepath.Join(path, "hooks", "post-receive")
	_, statErr := os.Stat(filepath.Join(path, "HEAD"))
	_, hookErr := os.Stat(hookPath)
	switch {
	case os.IsNotExist(statErr):
		if out, err := execGit(ctx, "", "init", "--bare", "--quiet", path); err != nil {
			return "", false, fmt.Errorf("gitserver: git init --bare %s: %w (%s)", path, err, out)
		}
		if err := s.writeHook(ctx, app, hookPath); err != nil {
			return "", false, err
		}
		return path, true, nil
	case os.IsNotExist(hookErr):
		// 仓库在而钩子缺失：轮换 token 重写（旧 token 即刻失效）。
		if err := s.writeHook(ctx, app, hookPath); err != nil {
			return "", false, err
		}
		return path, false, nil
	case statErr != nil:
		return "", false, fmt.Errorf("gitserver: stat repo %s: %w", path, statErr)
	case hookErr != nil:
		return "", false, fmt.Errorf("gitserver: stat hook %s: %w", hookPath, hookErr)
	}
	return path, false, nil
}

// lockRepo 取 app 的仓库写入互斥并加锁（E7③）：返回解锁函数；map 自身
// 由 repoMu 保护——分段锁不放大跨 app 的并发代价。
func (s *GitTriggers) lockRepo(app string) func() {
	s.repoMu.Lock()
	mu, ok := s.repoLocks[app]
	if !ok {
		mu = &sync.Mutex{}
		s.repoLocks[app] = mu
	}
	s.repoMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// writeHook 生成钩子 token（先按名吊销旧 token = 轮换）并写 post-receive
// 钩子文件（0600；POSIX 侧补可执行位 0700）。
func (s *GitTriggers) writeHook(ctx context.Context, app, hookPath string) error {
	plaintext, err := s.rotateHookToken(ctx, app)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
		return fmt.Errorf("gitserver: create hooks dir: %w", err)
	}
	script := postReceiveScript(s.webhookEndpoint(app), plaintext)
	if err := os.WriteFile(hookPath, []byte(script), 0o600); err != nil { //nolint:gosec // G306：钩子含回调 token，0600 即要求（POSIX 侧另补 0700 可执行位）
		return fmt.Errorf("gitserver: write hook %s: %w", hookPath, err)
	}
	if err := os.Chmod(hookPath, 0o700); err != nil { //nolint:gosec // G302：钩子是可执行程序，0700 = 属主独享执行（权限最小且必须）
		return fmt.Errorf("gitserver: chmod hook: %w", err)
	}
	return nil
}

// rotateHookToken 轮换 app 的钩子 token：按名吊销在册旧 token，签发新
// token（scope=deploy，明文仅本次返回——只进钩子文件）。
func (s *GitTriggers) rotateHookToken(ctx context.Context, app string) (string, error) {
	name := hookTokenName(app)
	existing, err := s.st.ListTokens(ctx)
	if err != nil {
		return "", fmt.Errorf("gitserver: list hook tokens: %w", err)
	}
	for _, t := range existing {
		if t.Name == name {
			if err := s.st.RevokeToken(ctx, t.ID, ""); err != nil {
				return "", fmt.Errorf("gitserver: revoke old hook token: %w", err)
			}
		}
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gitserver: generate hook token: %w", err)
	}
	plaintext := "flthk_" + hex.EncodeToString(buf)
	if _, err := s.st.CreateToken(ctx, state.TokenWrite{
		Hash:   state.HashToken(plaintext),
		Name:   name,
		Scopes: "deploy",
		Actor:  "system",
	}); err != nil {
		return "", fmt.Errorf("gitserver: create hook token: %w", err)
	}
	return plaintext, nil
}

// hookTokenMarker 是钩子脚本中 token 赋值行的固定前缀（B3 读取面与
// postReceiveScript 的写入面共用同一词形）。
const hookTokenMarker = "TOKEN='"

// hookTokenFromScript 从 post-receive 钩子脚本提取 token 明文（TOKEN='<token>'
// 单引号词形）；未命中返回空串。
func hookTokenFromScript(script string) string {
	i := strings.Index(script, hookTokenMarker)
	if i < 0 {
		return ""
	}
	rest := script[i+len(hookTokenMarker):]
	if j := strings.IndexByte(rest, '\''); j >= 0 {
		return rest[:j]
	}
	return ""
}

// SecretValues 实现 logs.SecretValuesSource（B3 脱敏值集扩面）：钩子 token
// 的明文只落 post-receive 钩子文件（state 仅存哈希，无解密面）——从钩子
// 文件读取并入该 app 的日志脱敏值集。app 行/钩子文件缺失返回 nil（脱敏
// 面降级，不阻断采集）。
func (s *GitTriggers) SecretValues(ctx context.Context, appID string) []string {
	app, err := s.st.GetAppByID(ctx, appID)
	if err != nil {
		return nil
	}
	if !ValidAppName(app.Name) {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(s.repoPath(app.Name), "hooks", "post-receive")) //nolint:gosec // G304：路径为包内构造（app 名经白名单校验）
	if err != nil {
		return nil
	}
	if tok := hookTokenFromScript(string(raw)); tok != "" {
		return []string{tok}
	}
	return nil
}

// postReceiveScript 生成 post-receive 钩子脚本（POSIX sh；Git for Windows
// 与 Linux 皆可运行）：逐行读 old new ref，仅转发 refs/heads/*（分支过滤
// 与幂等语义在 daemon 侧）；post-receive 无法否决推送——回调失败只如实
// 打印到 pusher stderr，推送照常成功。
func postReceiveScript(endpoint, token string) string {
	return `#!/bin/sh
# fleetly-managed post-receive hook (generated by fleetlyd; do not edit).
# Forwards head-branch updates to the fleetlyd control plane (DeployFromGit).
# The daemon applies branch filtering and (app, sha) dedup; a push that does
# not match the app's configured branch is accepted but triggers no deploy.
# Callback failures never fail the push (post-receive semantics).
URL='` + endpoint + `'
TOKEN='` + token + `'
ZERO=0000000000000000000000000000000000000000
post() {
  payload=$(printf '{"sha":"%s","ref":"%s"}' "$1" "$2")
  if command -v curl >/dev/null 2>&1; then
    curl -sS -m 30 -w '\nfleetly-http=%{http_code}' -X POST \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
      -d "$payload" "$URL"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- -T 30 --header="Authorization: Bearer $TOKEN" \
      --header='Content-Type: application/json' --post-data="$payload" "$URL"
  else
    echo 'fleetly: neither curl nor wget available for deploy hook' >&2
    return 1
  fi
}
while read -r _old new ref; do
  case "$ref" in refs/heads/*) ;; *) continue ;; esac
  if [ "$new" = "$ZERO" ]; then continue; fi
  echo "fleetly: notifying control plane for $ref"
  resp=$(post "$new" "$ref")
  if [ -n "$resp" ]; then
    echo "fleetly: $resp"
  fi
done
exit 0
`
}

// composeFromCommit 从 bare 仓库读指定 commit 的 compose 文件：约定在仓库
// 根的 compose.yaml / compose.yml——两者都在 → 拒绝（歧义信封，compose
// 族）；都缺 → 拒绝。compose 真源在 git 对象库，不信任客户端传字节。
func (s *GitTriggers) composeFromCommit(ctx context.Context, app, sha string) ([]byte, error) {
	path := s.repoPath(app)
	yamlBytes, yamlErr := gitShow(ctx, path, sha, "compose.yaml")
	ymlBytes, ymlErr := gitShow(ctx, path, sha, "compose.yml")
	switch {
	case yamlErr == nil && ymlErr == nil:
		return nil, fmt.Errorf("gitserver: %w: app %s at %s has both compose.yaml and compose.yml (ambiguous; refusing to deploy)",
			errComposeRejected, app, sha)
	case yamlErr == nil:
		return yamlBytes, nil
	case ymlErr == nil:
		return ymlBytes, nil
	default:
		return nil, fmt.Errorf("gitserver: %w: app %s at %s: no compose.yaml / compose.yml at the repo root",
			errComposeRejected, app, sha)
	}
}

// errComposeRejected 是 compose 读取失败的包内哨兵（api/webhook 层映射为
// E_COMPOSE_UNSUPPORTED——errcode 零新增，复用既有 compose 码族）。
var errComposeRejected = fmt.Errorf("compose unavailable")

// gitShow 执行 git show <sha>:<file>（cwd = bare 仓库）。
func gitShow(ctx context.Context, repoPath, sha, file string) ([]byte, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "show", sha+":"+file) //nolint:gosec // G204：参数为校验后的常量词形（sha 40hex + 固定文件名）
	cmd.Dir = repoPath
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git show %s:%s: %s", sha, file, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

// execGit 执行系统 git（cwd 可空）。
func execGit(ctx context.Context, cwd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // G204：参数为包内常量白名单词形
	if cwd != "" {
		cmd.Dir = cwd
	}
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()) + strings.TrimSpace(errOut.String()),
			fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), nil
}
