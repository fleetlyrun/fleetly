package cmd

// Telemetry 上下文动词：events list（F0.23 读路径；流式 follow N1）与
// logs（F0.25：RuntimeLogs 直读，诚实标注"仅实时+最近缓冲"）。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/lynx-go/commands"
	"google.golang.org/protobuf/proto"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

func newEventsListVerb() commands.Command {
	const name = "list"
	var after, limit int64
	return &flaggedVerb{
		name: name, synopsis: "List events from the outbox (cursor: after-seq; streaming follow lands in N1)",
		usage: "events list [--after-seq N] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.Int64Var(&after, "after-seq", 0, "return events after this seq (cursor)")
			fs.Int64Var(&limit, "limit", 100, "max events (capped at 1000)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close()                                                                                             //nolint:errcheck // 进程退出路径
			resp, err := c.Events.ListEvents(ctx, &telemetryv1.ListEventsRequest{AfterSeq: after, Limit: int32(limit)}) //nolint:gosec // 限额在服务端钳制
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "last_seq=%d\n", resp.GetLastSeq())
				_, _ = fmt.Fprintln(env.Stdout, "SEQ\tNAME\tAGGREGATE\tID\tCREATED")
				for _, ev := range resp.GetEvents() {
					_, _ = fmt.Fprintf(env.Stdout, "%d\t%s\t%s\t%s\t%s\n", ev.GetSeq(), ev.GetName(), ev.GetAggregate(), ev.GetAggregateId(), ev.GetCreatedAt())
				}
			})
		},
	}
}

// newEventsFollowVerb 是订阅面（F1.2）：重放保留窗 + 持续跟随（Ctrl-C
// 收口）。--replay 只重放随后退出（golden/脚本友好的有界形态）。
func newEventsFollowVerb() commands.Command {
	const name = "follow"
	var after int64
	var replay bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Follow platform events from the outbox (replay the retention window, then keep streaming)",
		usage:    "events follow [--after-seq N] [--replay]",
		setFlags: func(fs *flag.FlagSet) {
			fs.Int64Var(&after, "after-seq", 0, "resume after this seq (0 = start of the retention window; a trimmed cursor returns 410 E_EVENTS_GONE)")
			fs.BoolVar(&replay, "replay", false, "replay the retention window and exit (bounded form for scripts)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			// 流式动词：拨号豁免请求级 deadline（logs 同款纪律）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			stream, err := c.Events.StreamEvents(ctx, &telemetryv1.StreamEventsRequest{AfterSeq: after, Follow: !replay})
			if err != nil {
				return err
			}
			compact := func(m proto.Message) (string, error) {
				data, err := protoJSONMarshal.Marshal(m)
				if err != nil {
					return "", err
				}
				var buf bytes.Buffer
				if err := json.Compact(&buf, data); err != nil {
					return "", err
				}
				return buf.String(), nil
			}
			for {
				frame, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				ev := frame.GetEvent()
				if jsonOut {
					line, err := compact(frame)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(env.Stdout, line)
					continue
				}
				_, _ = fmt.Fprintf(env.Stdout, "%d\t%s\t%s\t%s\t%s\n", ev.GetSeq(), ev.GetName(), ev.GetAggregate(), ev.GetAggregateId(), ev.GetCreatedAt())
			}
		},
	}
}

func newLogsVerb() commands.Command {
	const name = "logs"
	var app, process, since, until, text string
	var tail int64
	var follow bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Stream container logs for an app (--text switches to the persisted-search path across the retention window)",
		usage:    "logs --app APP_ID [--process NAME] [--tail N] [--since T] [--until T] [--text SUBSTRING] [--follow]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&process, "process", "", "filter by process name")
			fs.Int64Var(&tail, "tail", 0, "tail lines (0 = all buffered)")
			fs.StringVar(&since, "since", "", "time window start (RFC3339, e.g. 2026-10-01T00:00:00Z)")
			fs.StringVar(&until, "until", "", "time window end (RFC3339)")
			fs.StringVar(&text, "text", "", "substring filter; switches to the persisted-search path (retention window; needs logging)")
			fs.BoolVar(&follow, "follow", false, "keep streaming new output")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			// 流式动词：拨号豁免请求级 deadline（follow 会话长存活，
			// 客户端 deadline 会腰斩尾随流；服务端流面不经 unary 超时拦截器）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			stream, err := c.Logs.StreamLogs(ctx, &telemetryv1.StreamLogsRequest{
				AppId: app, Process: process, TailLines: tail, Follow: follow,
				Since: since, Until: until, Text: text,
			})
			if err != nil {
				return err
			}
			// 流式行经 json.Compact 归一（protojson 的空白形态按二进制
			// 撒种，golden 确定性必须显式归一——render 单点同款纪律）。
			compact := func(m proto.Message) (string, error) {
				data, err := protoJSONMarshal.Marshal(m)
				if err != nil {
					return "", err
				}
				var buf bytes.Buffer
				if err := json.Compact(&buf, data); err != nil {
					return "", err
				}
				return buf.String(), nil
			}
			for {
				frame, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				if jsonOut {
					line, err := compact(frame)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(env.Stdout, line)
					continue
				}
				_, _ = fmt.Fprintf(env.Stdout, "%s %s %s\n", frame.GetTime(), frame.GetWorkloadId(), string(frame.GetLine()))
			}
		},
	}
}
