package gitserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// webhook 拉源（T2.19）：webhook 触发后 daemon 对该 app 的 bare 仓库执行
// git fetch（app 配置 source：url + branch + 认证形态 none|https_token|
// ssh_key）。认证材料 envelope 密文落库，消费时才解密：
//   - https_token → GIT_CONFIG_* 环境变量注入 http.extraHeader（凭据不进
//     进程参数位，ps 不可见——比 -c 形态收敛一个暴露面）；
//   - ssh_key → 私钥写临时文件（0600）+ GIT_SSH_COMMAND 指向；known_hosts
//     语义 v0.1 取 accept-new（首连自动收录，不再交互确认）——已知取舍，
//     待冻结轮复核。
//
// 远端不可达/fetch 失败 → ErrFetchFailed（api/webhook 层映射 E_RUNTIME_
// UNAVAILABLE——errcode 零新增，复用既有管线错误族）。

// ErrFetchFailed 是拉源失败的包内哨兵。
var ErrFetchFailed = errors.New("git fetch failed")

// ErrSourceNotConfigured 表示 app 未配置拉源 source（webhook 拉源前置）。
var ErrSourceNotConfigured = errors.New("app source not configured")

// fetchPlan 是一次拉源的执行计划（构造与执行分离——认证材料形态的单元
// 测试断言构造面，不真实触网）。
type fetchPlan struct {
	// RepoPath 是 bare 仓库路径（exec 的 cwd）。
	RepoPath string
	// Args 是 git fetch 参数（fetch --quiet <url> <refspec>）。
	Args []string
	// Env 是追加的环境变量（GIT_TERMINAL_PROMPT=0 恒在）。
	Env []string
	// Cleanup 释放临时资源（ssh_key 临时私钥文件）；可为 nil。
	Cleanup func()
}

// buildFetch 构造拉源计划（认证材料在此解密；不执行任何 git 命令）。
// appID 必须已存在；source url 未配置返回 ErrSourceNotConfigured。
func (s *Source) buildFetch(ctx context.Context, appID, repoPath string) (fetchPlan, error) {
	cfg, err := s.st.GetAppGitConfig(ctx, appID)
	if err != nil {
		return fetchPlan{}, fmt.Errorf("gitserver: read app source config: %w", err)
	}
	if cfg.SourceURL == "" {
		return fetchPlan{}, ErrSourceNotConfigured
	}
	branch := cfg.Branch
	if branch == "" {
		branch = state.DefaultGitBranch
	}
	plan := fetchPlan{
		RepoPath: repoPath,
		Args: []string{
			"fetch", "--quiet",
			cfg.SourceURL,
			"+refs/heads/" + branch + ":refs/heads/" + branch,
		},
		Env: []string{"GIT_TERMINAL_PROMPT=0"},
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
		plan.Env = append(plan.Env,
			"GIT_SSH_COMMAND=ssh -i "+keyFile+" -o StrictHostKeyChecking=accept-new -o BatchMode=yes")
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
	if err := runFetch(ctx, plan); err != nil {
		s.log.Warn("gitserver: fetch remote failed", "app", app, "error", err.Error())
		return fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	return nil
}

// runFetch 执行拉源计划（cwd = bare 仓库；env 追加在进程环境之上）。
func runFetch(ctx context.Context, plan fetchPlan) error {
	cmd := exec.CommandContext(ctx, "git", plan.Args...) //nolint:gosec // G204：参数为包内白名单词形（fetch 子命令 + 配置 url/refspec）
	cmd.Dir = plan.RepoPath
	cmd.Env = append(os.Environ(), plan.Env...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", plan.Args[0], err, errOut.String())
	}
	return nil
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
