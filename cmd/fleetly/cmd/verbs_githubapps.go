package cmd

// GitHubApps 动词（ADR-0073 阶段 B CLI 面）：私有 Git 仓库接入材料的
// put/list/delete/repos。私钥只进 put（--private-key-file 或 stdin），
// 输出只回指纹；受理即验证——App/Installation/私钥三元组当场对 GitHub
// 校验并解析属主（请求不携带属主，服务端填充）。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
)

// ghKeyMaterial 读私钥 PEM：--private-key-file 指定文件，缺省读进程
// stdin（Environment 无 stdin 注入面，os.Stdin 直读——valueOrStdin 同款）。
func ghKeyMaterial(path string) ([]byte, error) {
	if path == "" {
		return io.ReadAll(os.Stdin)
	}
	//nolint:gosec // G304 路径来自用户旗标(CLI 本地工具,与 secrets/ 部署
	// 文件旗标同信任域)
	return os.ReadFile(path)
}

// ghScopeFlag 是 (--project | --global) 互斥必一的解析公因子（oneof 包装
// 由调用方自组——生成接口未导出）。global=true 时 projectID 恒空。
func ghScopeFlag(project string, global bool) (projectID string, isGlobal bool, err error) {
	if project != "" && global {
		return "", false, usageErr("put", "set exactly one of --project or --global")
	}
	if project == "" && !global {
		return "", false, usageErr("put", "set exactly one of --project or --global")
	}
	return project, global, nil
}

func newGitHubAppsPutVerb() commands.Command {
	const name = "put"
	var project string
	var global bool
	var appID, installationID int64
	var keyFile, apiBase string
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Configure a GitHub App for private-repo clones (verified against GitHub; the key is write-only)",
		usage:    "github-apps put (--project PROJECT_ID | --global) --app-id N --installation-id N [--private-key-file PATH] [--api-base URL]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (project scope)")
			fs.BoolVar(&global, "global", false, "platform scope (requires a platform-admin token; serves every project)")
			fs.Int64Var(&appID, "app-id", 0, "numeric GitHub App ID (required)")
			fs.Int64Var(&installationID, "installation-id", 0, "numeric installation ID (required)")
			fs.StringVar(&keyFile, "private-key-file", "", "PEM private key file (default: read stdin)")
			fs.StringVar(&apiBase, "api-base", "", "GitHub API base for GitHub Enterprise Server (default: https://api.github.com)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if appID <= 0 || installationID <= 0 {
				return usageErr(name, "--app-id and --installation-id are required positive integers")
			}
			projectID, isGlobal, err := ghScopeFlag(project, global)
			if err != nil {
				return err
			}
			key, err := ghKeyMaterial(keyFile)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(key)) == "" {
				return usageErr(name, "a private key is required (--private-key-file PATH or stdin)")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.GitHubApps.SetGitHubApp(ctx, ghPutRequest(projectID, isGlobal, appID, installationID, string(key), apiBase))
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetApp(), func() {
				printGitHubApp(env, resp.GetApp())
				_, _ = fmt.Fprintln(env.Stdout, "verified against github (owner resolved automatically)")
			})
		},
	}
}

func newGitHubAppsListVerb() commands.Command {
	const name = "list"
	var project string
	var global bool
	return &flaggedVerb{
		name:     name,
		synopsis: "List configured GitHub Apps (fingerprints only; keys never leave the store)",
		usage:    "github-apps list (--project PROJECT_ID | --global)",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (project scope)")
			fs.BoolVar(&global, "global", false, "platform scope (requires a platform-admin token)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			projectID, isGlobal, err := ghScopeFlag(project, global)
			if err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.GitHubApps.ListGitHubApps(ctx, ghListRequest(projectID, isGlobal))
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				if len(resp.GetApps()) == 0 {
					_, _ = fmt.Fprintln(env.Stdout, "no github apps configured")
					return
				}
				for i, a := range resp.GetApps() {
					if i > 0 {
						_, _ = fmt.Fprintln(env.Stdout)
					}
					printGitHubApp(env, a)
				}
			})
		},
	}
}

func newGitHubAppsDeleteVerb() commands.Command {
	const name = "delete"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Remove a GitHub App (clone resolution loses this row immediately)",
		usage:    "github-apps delete --id ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "id", "", "github app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if id == "" {
				return usageErr(name, "--id is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.GitHubApps.DeleteGitHubApp(ctx, &deliveryv1.DeleteGitHubAppRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "deleted github app %s\n", id)
			})
		},
	}
}

func newGitHubAppsReposVerb() commands.Command {
	const name = "repos"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "List repositories reachable via the installation (mints a short-lived token; installation health check)",
		usage:    "github-apps repos --id ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "id", "", "github app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if id == "" {
				return usageErr(name, "--id is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.GitHubApps.ListGitHubAppRepos(ctx, &deliveryv1.ListGitHubAppReposRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				if len(resp.GetRepos()) == 0 {
					_, _ = fmt.Fprintln(env.Stdout, "no repositories reachable via this installation")
					return
				}
				for _, r := range resp.GetRepos() {
					vis := "public"
					if r.GetPrivate() {
						vis = "private"
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s (%s)\n", r.GetFullName(), vis)
				}
			})
		},
	}
}

// ghPutRequest / ghListRequest 组装 oneof 包装（生成接口未导出，concrete
// struct 隐式满足——字段赋值合法，无需命名接口类型）。
func ghPutRequest(projectID string, isGlobal bool, appID, installationID int64, keyPEM, apiBase string) *deliveryv1.SetGitHubAppRequest {
	req := &deliveryv1.SetGitHubAppRequest{
		AppId: appID, InstallationId: installationID, PrivateKeyPem: keyPEM, ApiBase: apiBase,
	}
	if isGlobal {
		req.Scope = &deliveryv1.SetGitHubAppRequest_Global{Global: true}
	} else {
		req.Scope = &deliveryv1.SetGitHubAppRequest_ProjectId{ProjectId: projectID}
	}
	return req
}

func ghListRequest(projectID string, isGlobal bool) *deliveryv1.ListGitHubAppsRequest {
	req := &deliveryv1.ListGitHubAppsRequest{}
	if isGlobal {
		req.Scope = &deliveryv1.ListGitHubAppsRequest_Global{Global: true}
	} else {
		req.Scope = &deliveryv1.ListGitHubAppsRequest_ProjectId{ProjectId: projectID}
	}
	return req
}

// printGitHubApp 渲染配置面（人类形态共用段；私钥只回指纹）。
func printGitHubApp(env *commands.Environment, a *deliveryv1.GitHubApp) {
	scope := "project " + a.GetProjectId()
	if a.GetProjectId() == "" {
		scope = "global"
	}
	apiBase := a.GetApiBase()
	if apiBase == "" {
		apiBase = "(default github.com)"
	}
	_, _ = fmt.Fprintf(env.Stdout, "app %s\nscope %s\nowner %s\napp id %d\ninstallation %d\napi base %s\nprivate key fingerprint %s\n",
		a.GetId(), scope, a.GetAccountLogin(), a.GetAppId(), a.GetInstallationId(), apiBase, a.GetPrivateKeyFingerprint())
}
