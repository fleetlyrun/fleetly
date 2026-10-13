package engine

// 构建期 Git 访问材料解析（ADR-0073 阶段 B 决策 1）：repo 以干净 URL 冻
// 结，材料在 clone 前按 host+属主解析——Project 级 GitHub App → Global
// （Project 遮蔽 Global）→ 无材料（nil = 匿名 clone，公有仓合法形态）。
// 实体命中而材料不可用（解封/铸造失败）= 精确失败上抛，不静默降级匿名
// （与 ADR-0036 凭证铸造失败不降级同口径）。明文只进 clone argv，错误
// 输出经脱敏（cloneSource 侧）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/github"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/githubapp"
)

// installTokenUser 是 installation token 的 clone 用户名（GitHub App
// installation token 的官方口径）；token 作为 password 进 userinfo。
const installTokenUser = "x-access-token"

// defaultGitHost 是 github.com 面的仓库名（GHES 行的 git host 从 api_base
// 派生）。
const defaultGitHost = "github.com"

// gitSecretPrefix 是 Git 访问材料 Secret 的命名约定（ADR-0073 阶段 A：
// `git:<host>`；镜像 registry:<host> 定律——值 JSON username/secret，
// age 信封、读面只回指纹全套既有执法；username 空 = defaultGitUser）。
const gitSecretPrefix = "git:"

// defaultGitUser 是材料 username 缺省（GitHub 对任意非空用户名合法；
// GitLab 惯用 oauth2）。
const defaultGitUser = "oauth2"

// gitCredentialJSON 是 `git:<host>` Secret 值的形态（镜像
// registryCredentialJSON——registry:<host> 同族命名约定）。
type gitCredentialJSON struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}

// gitMaterial 是解析出的 clone 材料（token 明文，只进 argv 与脱敏面）。
type gitMaterial struct {
	username string
	token    string
}

// resolveGitMaterial 解析仓库的构建期访问材料。repo 解析失败（非 https/
// 畸形 URL）返回无材料——受理面已拦新写入，存量冻结行走防御轨（clone
// 原样失败并给出诚实错误）。
func (e *Engine) resolveGitMaterial(ctx context.Context, projectID, repoURL string) (*gitMaterial, error) {
	host, owner, ok := gitHostOwner(repoURL)
	if !ok {
		return nil, nil
	}
	use := func(row *githubapp.App) (*gitMaterial, error) {
		if gitHostOf(row.APIBase) != host {
			return nil, nil // host 失配（如 github.com 行对 GHES 仓）= 无材料
		}
		if e.cipher == nil {
			return nil, fmt.Errorf("github app material for %s present but the master key is not available", owner)
		}
		plain, err := e.cipher.Open(row.PrivateKeyCiphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt github app material for %s: %w", owner, err)
		}
		token, err := e.ghClient.InstallationToken(ctx, &github.App{
			EntityID: row.ID, KeyFingerprint: row.PrivateKeyFingerprint,
			AppID: row.AppID, InstallationID: row.InstallationID,
			APIBase: row.APIBase, PrivateKeyPEM: plain,
		})
		if err != nil {
			return nil, fmt.Errorf("mint installation token for %s: %w", owner, err)
		}
		return &gitMaterial{username: installTokenUser, token: token}, nil
	}
	row, err := e.ghApps.GetByProjectAndOwner(ctx, e.db.Runner(), projectID, owner)
	switch {
	case err == nil:
		return use(row)
	case !errors.Is(err, state.ErrNotFound):
		return nil, fmt.Errorf("lookup github app material for %s: %w", owner, err)
	}
	row, err = e.ghApps.GetGlobalByOwner(ctx, e.db.Runner(), owner)
	switch {
	case err == nil:
		return use(row)
	case !errors.Is(err, state.ErrNotFound):
		return nil, fmt.Errorf("lookup global github app material for %s: %w", owner, err)
	}
	return e.resolveGitSecretMaterial(ctx, projectID, host)
}

// resolveGitSecretMaterial 是解析序第三位（ADR-0073 阶段 A 决策 6）：
// Project Secret 保留名 `git:<host>`，值 JSON username/secret——镜像
// registry:<host> 定律（materials.go）：ErrNotFound = 匿名合法，库故障/
// 解封失败/坏 JSON 精确失败上抛。
func (e *Engine) resolveGitSecretMaterial(ctx context.Context, projectID, host string) (*gitMaterial, error) {
	name := gitSecretPrefix + host
	row, err := e.secrets.GetByName(ctx, e.db.Runner(), projectID, name)
	if errors.Is(err, state.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup git access material for %s: %w", host, err)
	}
	if e.cipher == nil {
		return nil, fmt.Errorf("git access material %s present but the master key is not available", name)
	}
	plain, err := e.cipher.Open(row.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt git access material %s: %w", name, err)
	}
	var cred gitCredentialJSON
	if err := json.Unmarshal(plain, &cred); err != nil {
		return nil, fmt.Errorf("git access material secret %s: bad JSON: %w", name, err)
	}
	username := cred.Username
	if username == "" {
		username = defaultGitUser
	}
	return &gitMaterial{username: username, token: cred.Secret}, nil
}

// gitHostOwner 拆 https 仓库 URL 的 host 与首段路径（owner；.git 后缀可
// 有可无——匹配只在 owner 段）。非 https/畸形 URL 返回 false。
func gitHostOwner(repoURL string) (host, owner string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", "", false
	}
	seg := strings.TrimPrefix(u.Path, "/")
	for i := 0; i < len(seg); i++ {
		if seg[i] == '/' {
			owner = seg[:i]
			break
		}
	}
	if owner == "" {
		return "", "", false
	}
	return u.Host, strings.TrimSuffix(owner, ".git"), true
}

// gitHostOf 派生实体的 git host（api_base host；空 = github.com；畸形
// api_base 原样返回——host 匹配自然失配，clone 走匿名）。
func gitHostOf(apiBase string) string {
	if strings.TrimSpace(apiBase) == "" {
		return defaultGitHost
	}
	if u, err := url.Parse(strings.TrimSpace(apiBase)); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(apiBase)
}

// assembleCloneURL 组装带凭据的 clone URL：userinfo 经 url.UserPassword
// 转义（token 含保留字符安全），明文只进本返回值——调用方原样送 argv，
// 错误输出由 cloneSource 脱敏。
func assembleCloneURL(repoURL, username, token string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("assemble clone URL: %q is not an absolute URL", repoURL)
	}
	u.User = url.UserPassword(username, token)
	return u.String(), nil
}

// prepareCloneURL 解析访问材料并组装 clone argv URL：返回值原样进 argv；
// secrets 是错误输出的字面脱敏清单（带凭据 URL 整体 + token）。无材料
// 命中 = 原 URL + 空清单（匿名 clone）。
func (e *Engine) prepareCloneURL(ctx context.Context, projectID, repoURL string) (string, []string, error) {
	mat, err := e.resolveGitMaterial(ctx, projectID, repoURL)
	if err != nil {
		return "", nil, err
	}
	if mat == nil {
		return repoURL, nil, nil
	}
	u, err := assembleCloneURL(repoURL, mat.username, mat.token)
	if err != nil {
		return "", nil, err
	}
	return u, []string{u, mat.token}, nil
}

// ResolveGitHead 解析分支当前 HEAD（SyncGitHook 控制面拉取通道的 commit
// 锚）：与 clone 同一材料解析面（私有仓 URL 内嵌访问材料，argv 单点；
// 失败走同一脱敏收口）。branch 为 "HEAD" 时取仓库缺省分支现值。
func (e *Engine) ResolveGitHead(ctx context.Context, projectID, repoURL, branch string) (string, error) {
	cloneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url, secrets, err := e.prepareCloneURL(ctx, projectID, repoURL)
	if err != nil {
		return "", err
	}
	pattern := "refs/heads/" + branch
	if branch == "HEAD" {
		pattern = "HEAD"
	}
	// nolint:gosec // G204 同 cloneSource：repo 受理面已 https 白名单+控制
	// 字符拒绝，argv 常量构造非注入面。
	args := []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"ls-remote", url, pattern,
	}
	//nolint:gosec // G204 同 cloneSource：变离子进程的信任域在受理面
	//（repo https 白名单 + argv 常量构造，58f901d 防线延续）。
	cmd := exec.CommandContext(cloneCtx, "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", cloneErr(err, out, url, secrets)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == pattern && isHexSHA(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("git ls-remote: %s not found in %s", pattern, repoURL)
}
