package cmd

// Hooks 上下文动词（F0.13 CLI 面）：per-App Git 触发配置的 set/get/rotate。
// secret 只在 set（首次铸造）与 rotate 输出出现一次；同一串兼作 GitHub
// webhook secret。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
)

// watchSlice 是可重复 --watch 旗标的值形态（repeatable string flag）。
type watchSlice []string

func (w *watchSlice) String() string { return strings.Join(*w, ",") }
func (w *watchSlice) Set(v string) error {
	*w = append(*w, v)
	return nil
}

func newHooksSetVerb() commands.Command {
	const name = "set"
	var app, repo, branch, dockerfile string
	var watch watchSlice
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Configure the per-app git trigger (mints a hook token on first set; secret shown once)",
		usage:    "hooks set --app APP_ID --repo URL [--branch BRANCH] [--dockerfile PATH] [--watch PATH]...",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&repo, "repo", "", "git repository URL builds are checked out from (required)")
			fs.StringVar(&branch, "branch", "", "branch filter (default: every branch; tags never trigger)")
			fs.StringVar(&dockerfile, "dockerfile", "", "dockerfile path inside the repo (default \"Dockerfile\")")
			fs.Var(&watch, "watch", "watched path prefix, repeatable (default: every change triggers)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" || repo == "" {
				return usageErr(name, "--app and --repo are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{
				AppId: app, Repo: repo, Branch: branch, Dockerfile: dockerfile, WatchPaths: watch,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				printHookConfig(env, resp.GetHook())
				if resp.GetSecret() != "" {
					printHookSecret(env, resp.GetSecret())
				} else {
					_, _ = fmt.Fprintln(env.Stdout, "secret: (unchanged; rotate to get a new one)")
				}
			})
		},
	}
}

func newHooksGetVerb() commands.Command {
	const name = "get"
	var app string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show the per-app git trigger configuration",
		usage:    "hooks get --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Hooks.GetGitHook(ctx, &deliveryv1.GetGitHookRequest{AppId: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetHook(), func() {
				printHookConfig(env, resp.GetHook())
				_, _ = fmt.Fprintf(env.Stdout, "webhook URL: %s<token> (token prefix %s)\n",
					fleetlygrpc.HooksURLPrefix, resp.GetHook().GetTokenPrefix())
			})
		},
	}
}

func newHooksRotateVerb() commands.Command {
	const name = "rotate"
	var app string
	return &flaggedVerb{
		name:     name,
		synopsis: "Rotate the hook token (old token dies immediately; secret shown once)",
		usage:    "hooks rotate --app APP_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&app, "app", "", "app id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Hooks.RotateHookToken(ctx, &deliveryv1.RotateHookTokenRequest{AppId: app})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				printHookConfig(env, resp.GetHook())
				printHookSecret(env, resp.GetSecret())
			})
		},
	}
}

// printHookConfig 渲染配置面（人类形态共用段）。
func printHookConfig(env *commands.Environment, h *deliveryv1.GitHook) {
	watch := "(every change triggers)"
	if len(h.GetWatchPaths()) > 0 {
		watch = strings.Join(h.GetWatchPaths(), ", ")
	}
	branch := h.GetBranch()
	if branch == "" {
		branch = "(every branch; tags never trigger)"
	}
	_, _ = fmt.Fprintf(env.Stdout, "app %s\nrepo %s\nbranch %s\ndockerfile %s\nwatch paths %s\n",
		h.GetAppId(), h.GetRepo(), branch, h.GetDockerfile(), watch)
}

// printHookSecret 渲染一次性明文与 GitHub 配置指引。
func printHookSecret(env *commands.Environment, secret string) {
	_, _ = fmt.Fprintf(env.Stdout, "secret (shown once): %s\nwebhook URL: %s%s (prepend your gateway address)\n"+
		"GitHub webhook secret: the same string above (rotating replaces both usages)\n",
		secret, fleetlygrpc.HooksURLPrefix, secret)
}
