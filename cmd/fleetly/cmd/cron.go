package cmd

// 定时任务命令（E5 Cron）：`fleetly cron trigger <app> <service>` 手动触发
// 一次（走与到点触发完全相同的调度链——服务端同事务写 cron.manual_triggered
// 审计；重叠/节点不可用返回 skipped 行而非错误，与调度器处置一致）；
// `fleetly cron runs <app> [service]` 读运行台账（scheduled_at 倒序，保留窗
// 每 schedule 最近 20 条）。全部经 RPC（CLI-over-SDK 纪律）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// cronCmd 是外层动词 `cron`：分发 trigger/runs。
type cronCmd struct {
	sub *commands.App
}

func newCronCmd() *cronCmd {
	sub := commands.New()
	sub.Register(&cronTriggerCmd{}, &cronRunsCmd{})
	sub.VerbTitle = "cron subcommands:"
	return &cronCmd{sub: sub}
}

func (c *cronCmd) Name() string { return "cron" }
func (c *cronCmd) Synopsis() string {
	return "trigger a scheduled service once and list cron run history (E5 scheduled jobs)"
}
func (c *cronCmd) Usage() string {
	return "cron <trigger|runs> [flags] ..."
}

func (c *cronCmd) SetFlags(_ *flag.FlagSet) {}

func (c *cronCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (trigger|runs)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// cronTriggerCmd 实现 `fleetly cron trigger <app> <service>`。
type cronTriggerCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *cronTriggerCmd) Name() string { return "trigger" }
func (c *cronTriggerCmd) Synopsis() string {
	return "manually trigger one run of a service's fleetly.cron schedule (same path as scheduled fires; audited)"
}
func (c *cronTriggerCmd) Usage() string {
	return "cron trigger [--addr <host:port>] [--token <tok>] [--json] <app> <service>"
}

func (c *cronTriggerCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *cronTriggerCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Cron().TriggerCronRun(ctx, &serverv1.TriggerCronRunRequest{
			App:     args[0],
			Service: args[1],
		})
		if err != nil {
			return err
		}
		run := resp.GetRun()
		if c.jsonOut {
			return writeJSON(env.Stdout, toCronRunJSON(run))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "cron run %s\n  app: %s\n  service: %s (%s)\n  status: %s",
			run.GetId(), args[0], run.GetService(), run.GetExpression(), run.GetStatus())
		if run.GetSkipReason() != "" {
			fmt.Fprintf(&b, "\n  skip_reason: %s (no job created; the scheduler records the same outcome on scheduled fires)", run.GetSkipReason())
		}
		if run.GetJobService() != "" {
			fmt.Fprintf(&b, "\n  job_service: %s", run.GetJobService())
		}
		if run.GetError() != "" {
			fmt.Fprintf(&b, "\n  error: %s", run.GetError())
		}
		b.WriteString("\n")
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// cronRunsCmd 实现 `fleetly cron runs <app> [service]`。
type cronRunsCmd struct {
	jsonOut bool
	limit   int
	conn    connFlags
}

func (c *cronRunsCmd) Name() string { return "runs" }
func (c *cronRunsCmd) Synopsis() string {
	return "list cron runs of an app (newest first; optionally narrowed to one service)"
}
func (c *cronRunsCmd) Usage() string {
	return "cron runs [--addr <host:port>] [--token <tok>] [--limit N] [--json] <app> [service]"
}

func (c *cronRunsCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.IntVar(&c.limit, "limit", 20, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *cronRunsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 1 or 2 arguments (<app> [service]), got %d", len(args))}
	}
	service := ""
	if len(args) > 1 {
		service = args[1]
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Cron().ListCronRuns(ctx, &serverv1.ListCronRunsRequest{
			App:     args[0],
			Service: service,
			Limit:   int32Clamp(c.limit),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			out := struct {
				App  string        `json:"app"`
				Rows []cronRunJSON `json:"runs"`
			}{App: resp.GetApp(), Rows: make([]cronRunJSON, 0, len(resp.GetRuns()))}
			for _, r := range resp.GetRuns() {
				out.Rows = append(out.Rows, toCronRunJSON(r))
			}
			return writeJSON(env.Stdout, out)
		}
		if len(resp.GetRuns()) == 0 {
			_, err := fmt.Fprintf(env.Stdout, "%s: no cron runs (declare fleetly.cron in the service's labels, deploy, and wait for a scheduled or manual trigger)\n", resp.GetApp())
			return err
		}
		for _, r := range resp.GetRuns() {
			line := fmt.Sprintf("%s  %-9s %-38s %s %s",
				r.GetId(), r.GetStatus(), r.GetService(), r.GetExpression(), r.GetScheduledAt().AsTime().Format("2006-01-02T15:04:05Z"))
			if r.GetSkipReason() != "" {
				line += "  skip=" + r.GetSkipReason()
			}
			if r.GetError() != "" {
				line += "  error=" + r.GetError()
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
		return nil
	})
}

// cronRunJSON 是台账行的机器可读投影（时间 RFC3339 字符串）。
type cronRunJSON struct {
	ID          string `json:"id"`
	Service     string `json:"service"`
	Expression  string `json:"expression"`
	ScheduledAt string `json:"scheduled_at"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	Status      string `json:"status"`
	SkipReason  string `json:"skip_reason,omitempty"`
	JobService  string `json:"job_service,omitempty"`
	Error       string `json:"error,omitempty"`
}

func toCronRunJSON(r *serverv1.CronRunView) cronRunJSON {
	out := cronRunJSON{
		ID:         r.GetId(),
		Service:    r.GetService(),
		Expression: r.GetExpression(),
		Status:     r.GetStatus(),
		SkipReason: r.GetSkipReason(),
		JobService: r.GetJobService(),
		Error:      r.GetError(),
	}
	if ts := r.GetScheduledAt(); ts != nil {
		out.ScheduledAt = tstampRFC3339(ts)
	}
	if ts := r.GetStartedAt(); ts != nil {
		out.StartedAt = tstampRFC3339(ts)
	}
	if ts := r.GetFinishedAt(); ts != nil {
		out.FinishedAt = tstampRFC3339(ts)
	}
	return out
}

// 编译期断言：cron 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &cronCmd{}
	_ commands.Flagged = &cronCmd{}
	_ commands.Command = &cronTriggerCmd{}
	_ commands.Flagged = &cronTriggerCmd{}
	_ commands.Command = &cronRunsCmd{}
	_ commands.Flagged = &cronRunsCmd{}
)
