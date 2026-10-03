package cmd

// Automation 上下文动词（F1.5/F1.6 CLI 面）：Task（one-shot/resident 池）与
// Run 的创建/列表/缩放/排空/续期/等待。`--wait` 走 WaitRun 流（F1.3 收口
// 面——不自写轮询）；双级稳定 DNS 在创建输出与 --json 形态可见。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
)

// envSlice 是可重复 --env K=V 旗标的值形态（变量注入面）。
type envSlice []string

func (e *envSlice) String() string { return strings.Join(*e, ",") }
func (e *envSlice) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("--env expects KEY=VALUE (got %q)", v)
	}
	*e = append(*e, v)
	return nil
}

// envMap 把旗标值解析为变量 map。
func (e envSlice) envMap() map[string]string {
	if len(e) == 0 {
		return nil
	}
	m := make(map[string]string, len(e))
	for _, kv := range e {
		parts := strings.SplitN(kv, "=", 2)
		m[parts[0]] = parts[1]
	}
	return m
}

// stringSlice 是可重复字符串旗标的通用形态（--secret-ref）。
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// waitRunFrames 经 WaitRun 收流至终态（F1.3 收口面）：逐帧回调，返回终态。
// 流式长等待——调用方须以 noDeadline 拨号。
func waitRunFrames(ctx context.Context, rc automationv1.RunsServiceClient, runID string, onFrame func(*automationv1.WaitRunResponse) error) (string, error) {
	stream, err := rc.WaitRun(ctx, &automationv1.WaitRunRequest{Id: runID})
	if err != nil {
		return "", err
	}
	final := ""
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return final, nil
		}
		if err != nil {
			return final, err
		}
		final = frame.GetRun().GetState()
		if onFrame != nil {
			if err := onFrame(frame); err != nil {
				return final, err
			}
		}
	}
}

// waitRunOnFrame 渲染一帧 Run 等待输出。
func waitRunOnFrame(env *commands.Environment, jsonOut bool, runID string) func(*automationv1.WaitRunResponse) error {
	return func(frame *automationv1.WaitRunResponse) error {
		if jsonOut {
			data, err := protoJSONMarshal.Marshal(frame)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := json.Compact(&buf, data); err != nil {
				return err
			}
			_, err = fmt.Fprintln(env.Stdout, buf.String())
			return err
		}
		r := frame.GetRun()
		line := fmt.Sprintf("run %s %s", runID, r.GetState())
		if r.GetStopReason() != "" {
			line += " (" + r.GetStopReason() + ")"
		}
		_, err := fmt.Fprintln(env.Stdout, line)
		return err
	}
}

// requireRunOK 是等待原语的终态裁决：failed 终态非零退出（stopped 家族是
// 正常收口——具体起因在 stop_reason，由调用方语义判定）。
func requireRunOK(runID, final string) error {
	if final == "failed" {
		return fmt.Errorf("run %s ended in state %s", runID, final)
	}
	return nil
}

func newTasksCreateVerb() commands.Command {
	const name = "create"
	var project, taskName, form, image, networkGroup string
	var command string
	var env envSlice
	var secretRefs stringSlice
	var cpuMillis, memoryMb, ttl, concurrency int64
	var idem idemKeyFlag
	var wait bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a task (one-shot execution or resident instance pool)",
		usage:    "tasks create --project PROJECT_ID --image REF [--name NAME] [--form one-shot|resident] [--concurrency N] [--ttl-seconds S] [--network-group GROUP] [--env KEY=VALUE]... [--secret-ref NAME]... [--cpu-millis N] [--memory-mb N] [--wait]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&taskName, "name", "", "human-readable name, unique per project while active")
			fs.StringVar(&form, "form", "", "task form: one-shot or resident (default: derived from concurrency > 1)")
			fs.StringVar(&image, "image", "", "image reference (direct image deploy, required)")
			fs.StringVar(&command, "command", "", "entrypoint override as a comma-separated list (default: image entrypoint)")
			fs.Var(&env, "env", "non-sensitive environment variable KEY=VALUE, repeatable")
			fs.Var(&secretRefs, "secret-ref", "project secret to inject (by name), repeatable")
			fs.Int64Var(&cpuMillis, "cpu-millis", 0, "per-run CPU limit in milli-cores (1000 = 1 CPU)")
			fs.Int64Var(&memoryMb, "memory-mb", 0, "per-run memory limit in MB")
			fs.Int64Var(&ttl, "ttl-seconds", 0, "run lifetime cap in seconds (absolute deadline; max 86400; 0 = no TTL)")
			fs.Int64Var(&concurrency, "concurrency", 0, "desired concurrency for resident pools (one-shot is always 1)")
			fs.StringVar(&networkGroup, "network-group", "", "task network group name (runs attach at creation; app processes cross-attach via taskGroup:<name>)")
			fs.BoolVar(&wait, "wait", false, "wait for the run to reach a terminal state (streams state transitions; non-zero exit on failure)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env2 *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			if image == "" {
				return usageErr(name, "--image is required")
			}
			var dialOpts []dialOption
			if wait {
				dialOpts = append(dialOpts, noDeadline())
			}
			ctx, cancel, c, err := dialFromEnv(ctx, dialOpts...)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			var cmdList []string
			if command != "" {
				cmdList = strings.Split(command, ",")
			}
			resp, err := c.Tasks.CreateTask(ctx, &automationv1.CreateTaskRequest{
				ProjectId: project, Name: taskName, Form: form, Image: image,
				Command: cmdList, Env: env.envMap(), SecretRefs: secretRefs,
				CpuMillis: cpuMillis, MemoryMb: memoryMb,
				TtlSeconds: ttl, DesiredConcurrency: concurrency,
				NetworkGroup: networkGroup,
			})
			if err != nil {
				return err
			}
			t := resp.GetTask()
			if wait {
				// 等首个 Run 出现（补足随驱动环；WaitRun 需要 run id）。
				runID := ""
				for i := 0; i < 30 && runID == ""; i++ {
					runs, rerr := c.Runs.ListRuns(ctx, &automationv1.ListRunsRequest{TaskId: t.GetId(), Limit: 1})
					if rerr == nil && len(runs.GetRuns()) > 0 {
						runID = runs.GetRuns()[0].GetId()
						break
					}
					if !sleepCtxCLI(ctx) {
						return ctx.Err()
					}
				}
				if runID == "" {
					return fmt.Errorf("task %s created but no run appeared within the wait window", t.GetId())
				}
				final, werr := waitRunFrames(ctx, c.Runs, runID, waitRunOnFrame(env2, jsonOut, runID))
				if werr != nil {
					return werr
				}
				return requireRunOK(runID, final)
			}
			return renderOut(env2, jsonOut, t, func() {
				_, _ = fmt.Fprintf(env2.Stdout, "task %s created (%s, %s)\n", t.GetId(), t.GetForm(), t.GetDnsName())
			})
		},
	}
}

func newTasksListVerb() commands.Command {
	const name = "list"
	var project, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List tasks in a project (newest first)",
		usage:    "tasks list --project PROJECT_ID [--after TASK_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last task id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tasks.ListTasks(ctx, &automationv1.ListTasksRequest{
				ProjectId: project, AfterTaskId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tFORM\tSTATE\tRUNS\tDNS\tUPDATED")
				for _, t := range resp.GetTasks() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%d/%d\t%s\t%s\n",
						t.GetId(), t.GetName(), t.GetForm(), t.GetState(),
						t.GetActiveRunCount(), t.GetDesiredConcurrency(), t.GetDnsName(), t.GetUpdatedAt())
				}
			})
		},
	}
}

func newTasksGetVerb() commands.Command {
	const name = "get"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one task",
		usage:    "tasks get --task TASK_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "task", "", "task id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--task is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tasks.GetTask(ctx, &automationv1.GetTaskRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetTask(), func() {
				t := resp.GetTask()
				_, _ = fmt.Fprintf(env.Stdout, "id: %s\nname: %s\nform: %s\nstate: %s\nimage: %s\ndns: %s\nnetwork-group: %s\nconcurrency: %d/%d\nlease-deadline: %s\n",
					t.GetId(), t.GetName(), t.GetForm(), t.GetState(), t.GetImage(), t.GetDnsName(),
					t.GetNetworkGroup(), t.GetActiveRunCount(), t.GetDesiredConcurrency(), t.GetLeaseDeadline())
			})
		},
	}
}

func newTasksScaleVerb() commands.Command {
	const name = "scale"
	var id string
	var concurrency int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Set a resident pool's desired concurrency",
		usage:    "tasks scale --task TASK_ID --concurrency N",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "task", "", "task id (required)")
			fs.Int64Var(&concurrency, "concurrency", 0, "desired run count (0 drains the pool to zero)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--task is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tasks.ScaleTask(ctx, &automationv1.ScaleTaskRequest{Id: id, DesiredConcurrency: concurrency})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetTask(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "task %s desired concurrency set to %d\n", id, resp.GetTask().GetDesiredConcurrency())
			})
		},
	}
}

func newTasksStopVerb() commands.Command {
	const name = "stop"
	var id string
	var force bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Drain a task (stop replenishing; --force also stops in-flight runs with grace)",
		usage:    "tasks stop --task TASK_ID [--force]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&id, "task", "", "task id (required)")
			fs.BoolVar(&force, "force", false, "grace-stop in-flight runs now (default: let them finish or run out their TTL)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--task is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tasks.StopTask(ctx, &automationv1.StopTaskRequest{Id: id, Force: force})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetTask(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "task %s draining (state %s)\n", id, resp.GetTask().GetState())
			})
		},
	}
}

func newTasksDeleteVerb() commands.Command {
	const name = "delete"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a task (removes carriers, terminalizes runs, tombstones the row)",
		usage:    "tasks delete --task TASK_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "task", "", "task id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--task is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			if _, err := c.Tasks.DeleteTask(ctx, &automationv1.DeleteTaskRequest{Id: id}); err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(env.Stdout, map[string]string{"deleted": id})
			}
			_, err = fmt.Fprintf(env.Stdout, "task %s deleted\n", id)
			return err
		},
	}
}

func newTasksRenewVerb() commands.Command {
	const name = "renew"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Renew a resident task's owner lease (revives a drained task)",
		usage:    "tasks renew --task TASK_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "task", "", "task id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--task is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tasks.RenewTask(ctx, &automationv1.RenewTaskRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetTask(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "task %s lease renewed (deadline %s)\n", id, resp.GetTask().GetLeaseDeadline())
			})
		},
	}
}

func newRunsListVerb() commands.Command {
	const name = "list"
	var taskID, projectID, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List runs (newest first; filter by task or project - one is required)",
		usage:    "runs list (--task TASK_ID | --project PROJECT_ID) [--after RUN_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&taskID, "task", "", "filter by task id (recommended for high-volume pools)")
			fs.StringVar(&projectID, "project", "", "filter by project id (ADR-0035: an unfiltered run list spans every team's runs)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last run id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if (taskID == "") == (projectID == "") {
				return usageErr(name, "exactly one of --task or --project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Runs.ListRuns(ctx, &automationv1.ListRunsRequest{
				TaskId: taskID, ProjectId: projectID, AfterRunId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tTASK\tSTATE\tSTOP_REASON\tEXIT\tDNS\tFINISHED")
				for _, r := range resp.GetRuns() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						r.GetId(), r.GetTaskId(), r.GetState(), r.GetStopReason(), r.GetExitCode(), r.GetDnsName(), r.GetFinishedAt())
				}
			})
		},
	}
}

func newRunsGetVerb() commands.Command {
	const name = "get"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one run",
		usage:    "runs get --run RUN_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "run", "", "run id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--run is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Runs.GetRun(ctx, &automationv1.GetRunRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetRun(), func() {
				r := resp.GetRun()
				_, _ = fmt.Fprintf(env.Stdout, "id: %s\ntask: %s\nstate: %s\nstop_reason: %s\nexit_code: %d\ndns: %s\n",
					r.GetId(), r.GetTaskId(), r.GetState(), r.GetStopReason(), r.GetExitCode(), r.GetDnsName())
			})
		},
	}
}

func newRunsStopVerb() commands.Command {
	const name = "stop"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Stop one run (resident pools replenish the slot)",
		usage:    "runs stop --run RUN_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "run", "", "run id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--run is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Runs.StopRun(ctx, &automationv1.StopRunRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetRun(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "run %s %s\n", id, resp.GetRun().GetState())
			})
		},
	}
}

func newRunsWaitVerb() commands.Command {
	const name = "wait"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Wait for a run to reach a terminal state (streams state transitions)",
		usage:    "runs wait --run RUN_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "run", "", "run id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--run is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			final, werr := waitRunFrames(ctx, c.Runs, id, waitRunOnFrame(env, jsonOut, id))
			if werr != nil {
				return werr
			}
			return requireRunOK(id, final)
		},
	}
}

// sleepCtxCLI 是 --wait 首帧轮询的短窗睡眠（1s；ctx 取消即返 false）。
func sleepCtxCLI(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ---- Schedules（F1.7，ADR-0018 时区 cron） ----

func newSchedulesCreateVerb() commands.Command {
	const name = "create"
	var project, schedName, cronExpr, timezone, image, networkGroup, command string
	var env envSlice
	var secretRefs stringSlice
	var cpuMillis, memoryMb, ttl int64
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a schedule (timezone-aware cron firing one-shot tasks)",
		usage:    "schedules create --project PROJECT_ID --cron EXPR --image REF [--name NAME] [--timezone IANA_NAME] [--command A,B,...] [--env KEY=VALUE]... [--secret-ref NAME]... [--cpu-millis N] [--memory-mb N] [--ttl-seconds S] [--network-group GROUP]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&schedName, "name", "", "human-readable name, unique per project while active")
			fs.StringVar(&cronExpr, "cron", "", "5-field cron expression: minute hour day-of-month month day-of-week (required)")
			fs.StringVar(&timezone, "timezone", "", "IANA timezone name interpreted as wall clock across DST (default UTC)")
			fs.StringVar(&image, "image", "", "image reference fired each occurrence (required)")
			fs.StringVar(&command, "command", "", "entrypoint override as a comma-separated list (default: image entrypoint)")
			fs.Var(&env, "env", "non-sensitive environment variable KEY=VALUE, repeatable")
			fs.Var(&secretRefs, "secret-ref", "project secret to inject (by name), repeatable")
			fs.Int64Var(&cpuMillis, "cpu-millis", 0, "per-run CPU limit in milli-cores (1000 = 1 CPU)")
			fs.Int64Var(&memoryMb, "memory-mb", 0, "per-run memory limit in MB")
			fs.Int64Var(&ttl, "ttl-seconds", 0, "per-run lifetime cap in seconds (absolute deadline; max 86400; 0 = no TTL)")
			fs.StringVar(&networkGroup, "network-group", "", "task network group name (fired runs attach at creation)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env2 *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			if cronExpr == "" {
				return usageErr(name, "--cron is required")
			}
			if image == "" {
				return usageErr(name, "--image is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			var cmdList []string
			if command != "" {
				cmdList = strings.Split(command, ",")
			}
			resp, err := c.Schedules.CreateSchedule(ctx, &automationv1.CreateScheduleRequest{
				ProjectId: project, Name: schedName, Cron: cronExpr, Timezone: timezone,
				Image: image, Command: cmdList, Env: env.envMap(), SecretRefs: secretRefs,
				CpuMillis: cpuMillis, MemoryMb: memoryMb, TtlSeconds: ttl, NetworkGroup: networkGroup,
			})
			if err != nil {
				return err
			}
			s := resp.GetSchedule()
			return renderOut(env2, jsonOut, s, func() {
				_, _ = fmt.Fprintf(env2.Stdout, "schedule %s created (next fire %s, %s %s)\n",
					s.GetId(), s.GetNextFireAt(), s.GetCron(), s.GetTimezone())
			})
		},
	}
}

func newSchedulesListVerb() commands.Command {
	const name = "list"
	var project, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List schedules in a project (newest first)",
		usage:    "schedules list --project PROJECT_ID [--after SCHEDULE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last schedule id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Schedules.ListSchedules(ctx, &automationv1.ListSchedulesRequest{
				ProjectId: project, AfterScheduleId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tCRON\tTIMEZONE\tNEXT_FIRE\tSTATE\tLAST_TASK")
				for _, s := range resp.GetSchedules() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						s.GetId(), s.GetName(), s.GetCron(), s.GetTimezone(),
						s.GetNextFireAt(), s.GetState(), s.GetLastTaskId())
				}
			})
		},
	}
}

func newSchedulesGetVerb() commands.Command {
	const name = "get"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one schedule",
		usage:    "schedules get --schedule SCHEDULE_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "schedule", "", "schedule id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--schedule is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Schedules.GetSchedule(ctx, &automationv1.GetScheduleRequest{Id: id})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetSchedule(), func() {
				s := resp.GetSchedule()
				_, _ = fmt.Fprintf(env.Stdout, "id: %s\nname: %s\nstate: %s\ncron: %s (%s)\nnext_fire: %s\nlast_task: %s\nimage: %s\n",
					s.GetId(), s.GetName(), s.GetState(), s.GetCron(), s.GetTimezone(),
					s.GetNextFireAt(), s.GetLastTaskId(), s.GetImage())
			})
		},
	}
}

func newSchedulesTriggerVerb() commands.Command {
	const name = "trigger"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Fire a schedule now (RunNow semantics; the cron cadence is not shifted)",
		usage:    "schedules trigger --schedule SCHEDULE_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "schedule", "", "schedule id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--schedule is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Schedules.TriggerSchedule(ctx, &automationv1.TriggerScheduleRequest{Id: id})
			if err != nil {
				return err
			}
			s := resp.GetSchedule()
			return renderOut(env, jsonOut, s, func() {
				_, _ = fmt.Fprintf(env.Stdout, "schedule %s fired (task %s, next fire %s)\n",
					id, s.GetLastTaskId(), s.GetNextFireAt())
			})
		},
	}
}

func newSchedulesDeleteVerb() commands.Command {
	const name = "delete"
	var id string
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a schedule (tombstone; already-spawned tasks run to completion)",
		usage:    "schedules delete --schedule SCHEDULE_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&id, "schedule", "", "schedule id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if id == "" {
				return usageErr(name, "--schedule is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			if _, err := c.Schedules.DeleteSchedule(ctx, &automationv1.DeleteScheduleRequest{Id: id}); err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(env.Stdout, map[string]string{"deleted": id})
			}
			_, err = fmt.Fprintf(env.Stdout, "schedule %s deleted\n", id)
			return err
		},
	}
}
