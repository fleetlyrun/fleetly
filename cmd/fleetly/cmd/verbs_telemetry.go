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

	"github.com/lynxland/commands"
	"google.golang.org/protobuf/proto"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// eventsAfterUnset 是 --after-seq 的「未显式给出」哨兵（负 seq 无意义，
// 任意负值同判）：裸调用走尾窗语义——展示最新 limit 条，而非保留窗头部。
// staging 记录·六十二立案①根因：旧默认（after-seq=0 + limit=100）在
// outbox 越过 100 行后，裸 `events list` 恒定返回头部 100 行——观测面
// 呈「事件流停摆在 seq 100」假象（写路径全程健康、重启无效果），P0 排查
// 被一头雾水消耗。诊断面默认看「最近发生了什么」：tail 缺省与 logs
// --tail / journalctl -n 同族；头部翻页仍可经显式 --after-seq 0 达成。
const eventsAfterUnset = int64(-1)

func newEventsListVerb() commands.Command {
	const name = "list"
	var after, limit int64
	return &flaggedVerb{
		name: name, synopsis: "List events from the outbox (default: the most recent window; --after-seq pages forward from a cursor)",
		usage: "events list [--after-seq N] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.Int64Var(&after, "after-seq", eventsAfterUnset, "return events after this seq (cursor; omit for the most recent window, 0 = start of the retention window)")
			fs.Int64Var(&limit, "limit", 100, "max events (capped at 1000)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				// 未显式给游标 → 尾窗：以 last_seq 换算 after 游标取最新
				// limit 条（不足 limit 条时从保留窗头部起）；earliest 一并
				// 取回供截断提示（人类形态钉「窗外还有更旧事件」的可见性，
				// 不再让截断静默——立案①的另一半根因）。
				tail := after < 0
				cursor, earliest := after, int64(0)
				if tail {
					st, err := c.Events.GetEventStatus(ctx, &telemetryv1.GetEventStatusRequest{})
					if err != nil {
						return err
					}
					earliest = st.GetEarliestSeq()
					cursor = st.GetLastSeq() - limit
					if cursor < 0 {
						cursor = 0
					}
				}
				resp, err := c.Events.ListEvents(ctx, &telemetryv1.ListEventsRequest{AfterSeq: cursor, Limit: int32(limit)}) //nolint:gosec // 限额在服务端钳制
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout, "last_seq=%d\n", resp.GetLastSeq())
					_, _ = fmt.Fprintln(env.Stdout, "SEQ\tNAME\tAGGREGATE\tID\tCREATED")
					for _, ev := range resp.GetEvents() {
						_, _ = fmt.Fprintf(env.Stdout, "%d\t%s\t%s\t%s\t%s\n", ev.GetSeq(), ev.GetName(), ev.GetAggregate(), ev.GetAggregateId(), ev.GetCreatedAt())
					}
					if events := resp.GetEvents(); tail && len(events) > 0 && events[0].GetSeq() > earliest {
						_, _ = fmt.Fprintf(env.Stdout, "(older events not shown; earliest retained seq is %d; page with --after-seq)\n", earliest)
					}
				})
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
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
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
			})
		},
	}
}

func newLogsVerb() commands.Command {
	const name = "logs"
	var app, database, run, process, since, until, text string
	var tail int64
	var follow bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Stream container logs (--app for apps; --database / --run address carrier domains; --text switches to the persisted-search path)",
		usage:    "logs --app APP_ID [--process NAME] | logs --database ID | logs --run ID [--tail N] [--since T] [--until T] [--text SUBSTRING] [--follow]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id or name (exactly one of --app/--database/--run)")
			fs.StringVar(&database, "database", "", "database id or name (carrier domain logs, IA v3 T8)")
			fs.StringVar(&run, "run", "", "run id (single run carrier logs)")
			fs.StringVar(&process, "process", "", "filter by process name (app axis only)")
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
			axes := 0
			for _, v := range []string{app, database, run} {
				if v != "" {
					axes++
				}
			}
			if axes == 0 {
				return usageErr(name, "one of --app / --database / --run is required")
			}
			if axes > 1 {
				return usageErr(name, "--app / --database / --run are mutually exclusive")
			}
			// 流式动词：拨号豁免请求级 deadline（follow 会话长存活，
			// 客户端 deadline 会腰斩尾随流；服务端流面不经 unary 超时拦截器）。
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
				// --app/--database/--run 恰一非空（拨号前互斥校验）：app 与
				// database 侧走 name 解析，run 侧与空侧原样透传。
				logApp, logDB := app, database
				if logApp != "" {
					resolved, rerr := resolveAppID(ctx, c, "", logApp)
					if rerr != nil {
						return rerr
					}
					logApp = resolved
				}
				if logDB != "" {
					resolved, rerr := resolveDatabaseID(ctx, c, "", logDB)
					if rerr != nil {
						return rerr
					}
					logDB = resolved
				}
				stream, err := c.Logs.StreamLogs(ctx, &telemetryv1.StreamLogsRequest{
					AppId: logApp, DatabaseId: logDB, RunId: run, Process: process,
					TailLines: tail, Follow: follow, Since: since, Until: until, Text: text,
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
			})
		},
	}
}
