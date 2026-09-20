package main

// fleetly events 命令（T2.18 新增动词——事件流 RPC 面自 T2.17 起已有）：
// `events watch` 消费平台事件流（seq 游标 server-stream）。断线重连带
// --since-seq 续读；游标早于保留窗时流上先收到 cursor_expired 信封帧后
// 正常关闭（消费方以 oldest_seq 重新拉全量——events.proto 取舍注记）。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// eventsCmd 是外层动词 `events`：分发 watch。
type eventsCmd struct {
	sub *commands.App
}

func newEventsCmd() *eventsCmd {
	sub := commands.New()
	sub.Register(&eventsWatchCmd{})
	sub.VerbTitle = "events subcommands:"
	return &eventsCmd{sub: sub}
}

func (c *eventsCmd) Name() string     { return "events" }
func (c *eventsCmd) Synopsis() string { return "platform event stream (seq cursor)" }
func (c *eventsCmd) Usage() string    { return "events <watch> [flags]" }

func (c *eventsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *eventsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (watch)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// eventsWatchCmd 实现 `fleetly events watch [--since-seq N]`。
type eventsWatchCmd struct {
	sinceSeq int64
	jsonOut  bool
	conn     connFlags
}

func (c *eventsWatchCmd) Name() string { return "watch" }
func (c *eventsWatchCmd) Synopsis() string {
	return "watch platform events (since_seq cursor; 0 = retention window start)"
}
func (c *eventsWatchCmd) Usage() string {
	return "events watch [--addr <host:port>] [--token <tok>] [--since-seq N] [--json]"
}

func (c *eventsWatchCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.Int64Var(&c.sinceSeq, "since-seq", 0, "return events with seq > N (reconnect cursor; 0 = start)")
	fs.BoolVar(&c.jsonOut, "json", false, "output JSONL (one frame per line)")
}

func (c *eventsWatchCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	err := c.conn.withClient(func(cl *fleetlyClient) error {
		return cl.WatchEvents(ctx, c.sinceSeq, func(frame *serverv1.WatchEventsResponse) error {
			if c.jsonOut {
				return writeProtoJSONL(env.Stdout, frame)
			}
			switch f := frame.GetFrame().(type) {
			case *serverv1.WatchEventsResponse_Event:
				ev := f.Event
				at := ""
				if t := ev.GetAt(); t != nil {
					at = t.AsTime().Format("2006-01-02T15:04:05Z")
				}
				_, err := fmt.Fprintf(env.Stdout, "#%d %s %s %s\n", ev.GetSeq(), at, ev.GetName(), ev.GetSubject())
				return err
			case *serverv1.WatchEventsResponse_CursorExpired:
				_, err := fmt.Fprintf(env.Stdout, "cursor expired（seq ≤ %d 已被保留策略清理；以 --since-seq %d 重新拉全量）: %s\n",
					f.CursorExpired.GetOldestSeq()-1, f.CursorExpired.GetOldestSeq(), f.CursorExpired.GetMessage())
				return err
			default:
				return fmt.Errorf("unknown event frame: %T", frame.GetFrame())
			}
		})
	})
	if isCleanCancel(ctx, err) {
		return nil // Ctrl-C/SIGTERM：流随 ctx 取消干净收尾，exit 0（S17-D3）
	}
	return err
}

// 编译期断言：events 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &eventsCmd{}
	_ commands.Flagged = &eventsCmd{}
	_ commands.Command = &eventsWatchCmd{}
	_ commands.Flagged = &eventsWatchCmd{}
)
