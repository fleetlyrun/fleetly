package main

// fleetly logs 命令（T2.18 新增动词——日志管线 RPC 面自 T2.20 起已有）：
//
//	follow  —— 实时跟随（server-stream；ctx 取消即断流，重连 = 重新
//	           follow，回放窗口由服务端 ring 承载）；
//	history —— 落盘检索（时间窗/服务/来源过滤；source ∈ container|build）。
//
// --json 时流式输出 JSONL 逐行（一帧一行）；人读模式带时间戳/服务前缀。

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// logsCmd 是外层动词 `logs`：分发 follow/history。
type logsCmd struct {
	sub *commands.App
}

func newLogsCmd() *logsCmd {
	sub := commands.New()
	sub.Register(&logsFollowCmd{}, &logsHistoryCmd{})
	sub.VerbTitle = "logs subcommands:"
	return &logsCmd{sub: sub}
}

func (c *logsCmd) Name() string     { return "logs" }
func (c *logsCmd) Synopsis() string { return "app logs (live follow + history search)" }
func (c *logsCmd) Usage() string    { return "logs <follow|history> [flags] <app>" }

func (c *logsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *logsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (follow|history)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// logsFollowCmd 实现 `fleetly logs follow <app> [--service <svc>]`。
type logsFollowCmd struct {
	service string
	jsonOut bool
	conn    connFlags
}

func (c *logsFollowCmd) Name() string { return "follow" }
func (c *logsFollowCmd) Synopsis() string {
	return "follow app logs (live stream; Ctrl-C / cancel to stop)"
}
func (c *logsFollowCmd) Usage() string {
	return "logs follow [--addr <host:port>] [--token <tok>] [--service <svc>] [--json] <app>"
}

func (c *logsFollowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.service, "service", "", "compose service name (empty = all services of the app)")
	fs.BoolVar(&c.jsonOut, "json", false, "output JSONL (one frame per line)")
}

func (c *logsFollowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		return cl.FollowLogs(ctx, args[0], c.service, func(frame *serverv1.FollowLogsResponse) error {
			return emitLogEntry(env, c.jsonOut, frame.GetEntry())
		})
	})
}

// logsHistoryCmd 实现 `fleetly logs history <app> [--service] [--source]
// [--limit]`。
type logsHistoryCmd struct {
	service string
	source  string
	limit   int
	jsonOut bool
	conn    connFlags
}

func (c *logsHistoryCmd) Name() string { return "history" }
func (c *logsHistoryCmd) Synopsis() string {
	return "search persisted logs (time window / service / source filter)"
}
func (c *logsHistoryCmd) Usage() string {
	return "logs history [--addr <host:port>] [--token <tok>] [--service <svc>] [--source container|build] [--limit N] [--json] <app>"
}

func (c *logsHistoryCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.service, "service", "", "compose service name (empty = all)")
	fs.StringVar(&c.source, "source", "", "source filter: container | build (empty = all)")
	fs.IntVar(&c.limit, "limit", 200, "max rows (ceiling 1000; newest kept)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *logsHistoryCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Logs().ListHistoryLogs(ctx, &serverv1.ListHistoryLogsRequest{
			App: args[0], Service: c.service, Source: c.source, Limit: int32Clamp(c.limit),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s: %d log entries\n", args[0], len(resp.GetEntries()))
		_, err = fmt.Fprint(env.Stdout, b.String())
		if err != nil {
			return err
		}
		for _, e := range resp.GetEntries() {
			if err := emitLogEntry(env, false, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// emitLogEntry 输出单条日志（--json：JSONL 一行；人读：时间/服务/通道前
// 缀 + 原行）。
func emitLogEntry(env *commands.Environment, jsonOut bool, e *serverv1.LogEntryView) error {
	if jsonOut {
		return writeProtoJSONL(env.Stdout, e)
	}
	at := ""
	if t := e.GetAt(); t != nil {
		at = t.AsTime().Format(time.RFC3339)
	}
	channel := "out"
	if e.GetStderr() {
		channel = "err"
	}
	_, err := fmt.Fprintf(env.Stdout, "%s [%s/%s] %s\n", at, e.GetService(), channel, e.GetLine())
	return err
}

// 编译期断言：logs 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &logsCmd{}
	_ commands.Flagged = &logsCmd{}
	_ commands.Command = &logsFollowCmd{}
	_ commands.Flagged = &logsFollowCmd{}
	_ commands.Command = &logsHistoryCmd{}
	_ commands.Flagged = &logsHistoryCmd{}
)
