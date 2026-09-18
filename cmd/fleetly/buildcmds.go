package main

// 构建命令（T2.18 CLI-over-SDK 改造）：`fleetly build <compose>` 经 RPC
// 触发预构建（BuildsService.TriggerBuild：compose 内容字节上行，服务端
// 裁决构建目标并逐服务入队）并等待完成（轮询 GetBuild）→ 输出不可变
// digest；`fleetly builds list <app>` 查构建历史。
//
// 执行拓扑与旧直连形态一致：入队（builds queued 行）与执行（fleetlyd
// build.queue 服务扫描认领）跨进程解耦——daemon 不在运行时构建停留
// queued（--timeout 超时给出可行动提示）。镜像模式服务（仅 image 无
// build）= 无构建直通：不建行、报告引用。

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/compose"
)

// buildPollInterval 是 CLI 轮询构建行的周期。
const buildPollInterval = time.Second

// defaultBuildTimeout 是 build 等待缺省上限（冷构建受外网主导、方差极大
// ——Spike A 实测 85s~900s 量级；45 分钟覆盖慢网首建）。
const defaultBuildTimeout = 45 * time.Minute

// buildCmd 实现 `fleetly build <compose-file>`。
type buildCmd struct {
	service string
	jsonOut bool
	timeout time.Duration
	conn    connFlags
}

func (c *buildCmd) Name() string { return "build" }
func (c *buildCmd) Synopsis() string {
	return "build services of a compose file (queued via fleetlyd, waits and prints digests)"
}
func (c *buildCmd) Usage() string {
	return "build [--service <name>] [--addr <host:port>] [--token <tok>] [--timeout <duration>] [--json] <compose-file>"
}

func (c *buildCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.service, "service", "", "build only this service (default: all build-mode services)")
	fs.DurationVar(&c.timeout, "timeout", defaultBuildTimeout, "wait limit for builds to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *buildCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	// base_dir 是构建上下文解析基准（compose 文件所在目录的绝对路径）——
	// v0.1 单机同宿主语义：daemon 与 CLI 共享文件系统。
	composeAbs, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve compose path: %w", err)
	}
	content, err := osReadFile(args[0])
	if err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Builds().TriggerBuild(ctx, &serverv1.TriggerBuildRequest{
			Compose: content,
			Service: c.service,
			BaseDir: filepath.Dir(composeAbs),
		})
		if err != nil {
			return err
		}
		results, err := c.wait(ctx, env, cl, resp.GetBuilds(), c.timeout)
		if err != nil {
			return err
		}
		passthrough := make([]passthroughService, 0, len(resp.GetPassthrough()))
		for _, p := range resp.GetPassthrough() {
			passthrough = append(passthrough, passthroughService{Service: p.GetService(), Image: p.GetImage()})
		}
		if err := c.emit(env, resp.GetApp(), results, passthrough, fromComposeWarnings(resp.GetWarnings())); err != nil {
			return err
		}
		for _, r := range results {
			if r.GetStatus() != "succeeded" {
				if r.GetErrorCode() != "" {
					return fmt.Errorf("build %s (%s) failed: %s（构建日志 %s）", r.GetId(), r.GetService(), r.GetErrorCode(), r.GetLogPath())
				}
				return fmt.Errorf("build %s (%s) ended as %s", r.GetId(), r.GetService(), r.GetStatus())
			}
		}
		return nil
	})
}

// passthroughService 是镜像模式服务的直通报告。
type passthroughService struct {
	Service string `json:"service"`
	Image   string `json:"image"`
}

// wait 轮询至全部构建到终态或超时。超时区分「仍 queued」（daemon 未运行）
// 与「执行中超时」（卡住的构建），给出不同提示。
func (c *buildCmd) wait(ctx context.Context, env *commands.Environment, cl *fleetlyClient, queued []*serverv1.BuildView, timeout time.Duration) ([]*serverv1.BuildView, error) {
	if len(queued) == 0 {
		return nil, nil
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	pending := make(map[string]bool, len(queued))
	for _, b := range queued {
		pending[b.GetId()] = true
	}
	out := make([]*serverv1.BuildView, 0, len(queued))
	for len(pending) > 0 {
		select {
		case <-deadline.C:
			n := 0
			for id := range pending {
				if resp, err := cl.Builds().GetBuild(ctx, &serverv1.GetBuildRequest{Id: id}); err == nil && resp.GetBuild().GetStatus() == "queued" {
					n++
				}
			}
			if n == len(pending) {
				return out, fmt.Errorf("builds stayed queued for %s——fleetlyd 未运行？构建由 fleetlyd 的 build.queue 服务执行（本命令只入队与等待）", timeout)
			}
			return out, fmt.Errorf("builds did not finish within %s（在途构建继续执行，fleetly builds list 可查）", timeout)
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(buildPollInterval):
		}
		for id := range pending {
			resp, err := cl.Builds().GetBuild(ctx, &serverv1.GetBuildRequest{Id: id})
			if err != nil {
				return out, err
			}
			rec := resp.GetBuild()
			if rec.GetStatus() == "queued" || rec.GetStatus() == "building" {
				continue
			}
			delete(pending, id)
			out = append(out, rec)
			if !c.jsonOut {
				line := fmt.Sprintf("build %s (%s) %s", rec.GetId(), rec.GetService(), rec.GetStatus())
				if rec.GetStatus() == "succeeded" {
					line += "  " + rec.GetImageRef() + "@" + rec.GetImageDigest()
				}
				if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
					return out, err
				}
			}
		}
	}
	return out, nil
}

// buildResultJSON 是 build --json 输出形态。
type buildResultJSON struct {
	App         string               `json:"app"`
	Builds      []buildJSON          `json:"builds"`
	Passthrough []passthroughService `json:"passthrough,omitempty"`
	Warnings    []compose.Warning    `json:"warnings,omitempty"`
}

// emit 输出构建结果（--json 机器形态 / 人读摘要）。
func (c *buildCmd) emit(env *commands.Environment, appName string, results []*serverv1.BuildView, passthrough []passthroughService, warnings []compose.Warning) error {
	if c.jsonOut {
		out := buildResultJSON{App: appName, Builds: make([]buildJSON, 0, len(results))}
		for _, rec := range results {
			out.Builds = append(out.Builds, toBuildJSON(rec))
		}
		out.Passthrough = passthrough
		out.Warnings = warnings
		return writeJSON(env.Stdout, out)
	}
	for _, p := range passthrough {
		if _, err := fmt.Fprintf(env.Stdout, "service %s: image mode（无构建直通）%s\n", p.Service, p.Image); err != nil {
			return err
		}
	}
	var b strings.Builder
	writeWarnings(&b, warnings)
	_, err := fmt.Fprint(env.Stdout, b.String())
	return err
}

// ── fleetly builds ──────────────────────────────────────────────────────────

// buildsCmd 是外层动词 `builds`：分发 list。
type buildsCmd struct {
	sub *commands.App
}

func newBuildsCmd() *buildsCmd {
	sub := commands.New()
	sub.Register(&buildsListCmd{})
	sub.VerbTitle = "builds subcommands:"
	return &buildsCmd{sub: sub}
}

func (c *buildsCmd) Name() string { return "builds" }
func (c *buildsCmd) Synopsis() string {
	return "build history (state: queued/building/succeeded/failed)"
}
func (c *buildsCmd) Usage() string {
	return "builds list [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *buildsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *buildsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// buildsListCmd 实现 `fleetly builds list <app>`。
type buildsListCmd struct {
	jsonOut bool
	limit   int
	conn    connFlags
}

func (c *buildsListCmd) Name() string { return "list" }
func (c *buildsListCmd) Synopsis() string {
	return "list builds of an app (newest first, digest per succeeded build)"
}
func (c *buildsListCmd) Usage() string {
	return "builds list [--addr <host:port>] [--token <tok>] [--limit N] [--json] <app>"
}

func (c *buildsListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.IntVar(&c.limit, "limit", 20, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *buildsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Builds().ListBuilds(ctx, &serverv1.ListBuildsRequest{App: args[0], Limit: int32Clamp(c.limit)})
		if err != nil {
			return err
		}
		if c.jsonOut {
			out := make([]buildJSON, 0, len(resp.GetBuilds()))
			for _, rec := range resp.GetBuilds() {
				out = append(out, toBuildJSON(rec))
			}
			return writeJSON(env.Stdout, out)
		}
		if len(resp.GetBuilds()) == 0 {
			_, err = fmt.Fprintf(env.Stdout, "%s: no builds（fleetly build <compose> 发起构建）\n", args[0])
			return err
		}
		for _, rec := range resp.GetBuilds() {
			line := fmt.Sprintf("%s  %-9s %-10s %-10s", rec.GetId(), rec.GetStatus(), rec.GetDriver(), rec.GetService())
			if rec.GetImageDigest() != "" {
				line += "  " + rec.GetImageDigest()
			}
			if rec.GetErrorCode() != "" {
				line += "  error=" + rec.GetErrorCode()
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
		return nil
	})
}

// 编译期断言：构建命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &buildCmd{}
	_ commands.Flagged = &buildCmd{}
	_ commands.Command = &buildsCmd{}
	_ commands.Flagged = &buildsCmd{}
	_ commands.Command = &buildsListCmd{}
	_ commands.Flagged = &buildsListCmd{}
)
