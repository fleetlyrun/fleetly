package main

// 回滚与版本/漂移命令（T2.18 CLI-over-SDK 改造）：`fleetly rollback <app>`
//（RPC 入队 kind=rollback 部署并等待终态）、`fleetly revisions list <app>`
//（保留窗内可回滚目标——列表即选项）、`fleetly drift show|converge|
// enable|disable`（运行域漂移检测面：show 即时判定、converge 人工一次性
// 收敛、enable/disable 收敛 opt-in 的人工重置入口）。全部经 SDK 消费平台
// ——对账语义（EnqueueRollback/DriftShow/Converge）单一事实源在 daemon。

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// revisionKeepVersions 是保留窗宽度（state 层 RevisionKeepVersions 的契
// 约值=5；CLI 不 import 任何 state 装配包，数值随 proto/文档冻结同步）。
const revisionKeepVersions = 5

// ── fleetly rollback ────────────────────────────────────────────────────────

// rollbackCmd 实现 `fleetly rollback <app> [--to <revision>]`。
type rollbackCmd struct {
	jsonOut bool
	timeout time.Duration
	to      string
	conn    connFlags
}

func (c *rollbackCmd) Name() string { return "rollback" }
func (c *rollbackCmd) Synopsis() string {
	return "roll an app back to a revision (snapshot replay; default = latest revision)"
}
func (c *rollbackCmd) Usage() string {
	return "rollback [--addr <host:port>] [--token <tok>] [--to <revision>] [--timeout <duration>] [--json] <app>"
}

func (c *rollbackCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.to, "to", "", "target revision id (default: latest revision in the retention window)")
	fs.DurationVar(&c.timeout, "timeout", defaultDeployTimeout, "wait limit for the rollback to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *rollbackCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Deployments().RollbackDeployment(ctx, &serverv1.RollbackDeploymentRequest{
			App:              args[0],
			TargetRevisionId: c.to,
		})
		if err != nil {
			return err // E_ROLLBACK_NO_TARGET 等信封原样渲染
		}
		final, err := waitDeployment(ctx, env, cl, resp.GetDeploymentId(), c.timeout, c.jsonOut)
		if err != nil {
			return err
		}
		appResp, err := cl.Apps().GetApp(ctx, &serverv1.GetAppRequest{Name: final.GetApp()})
		if err != nil {
			return err
		}
		if err := emitDeployment(env, c.jsonOut, final, appResp.GetDerivedState(), nil); err != nil {
			return err
		}
		if final.GetStatus() != "succeeded" {
			if final.GetErrorCode() != "" {
				return fmt.Errorf("rollback %s failed: %s", final.GetId(), final.GetErrorCode())
			}
			return fmt.Errorf("rollback %s ended as %s", final.GetId(), final.GetStatus())
		}
		return nil
	})
}

// ── fleetly revisions ───────────────────────────────────────────────────────

// revisionsCmd 是外层动词 `revisions`：分发 list。
type revisionsCmd struct {
	sub *commands.App
}

func newRevisionsCmd() *revisionsCmd {
	sub := commands.New()
	sub.Register(&revisionsListCmd{})
	sub.VerbTitle = "revisions subcommands:"
	return &revisionsCmd{sub: sub}
}

func (c *revisionsCmd) Name() string { return "revisions" }
func (c *revisionsCmd) Synopsis() string {
	return "version snapshots in the rollback retention window (list = options)"
}
func (c *revisionsCmd) Usage() string { return "revisions <list> [flags] ..." }

func (c *revisionsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *revisionsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// revisionsListCmd 实现 `fleetly revisions list <app>`。
type revisionsListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *revisionsListCmd) Name() string { return "list" }
func (c *revisionsListCmd) Synopsis() string {
	return "list rollback targets of an app (newest first, retention window = 5)"
}
func (c *revisionsListCmd) Usage() string {
	return "revisions list [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *revisionsListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *revisionsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Revisions().ListRevisions(ctx, &serverv1.ListRevisionsRequest{App: args[0]})
		if err != nil {
			return err
		}
		type revisionJSON struct {
			ID          string `json:"id"`
			Seq         int64  `json:"seq"`
			DesiredHash string `json:"desired_hash,omitempty"`
			Status      string `json:"status"`
			Verified    bool   `json:"verified"`
			CreatedAt   string `json:"created_at,omitempty"`
		}
		out := struct {
			App       string         `json:"app"`
			Keep      int            `json:"keep_versions"`
			Revisions []revisionJSON `json:"revisions"`
		}{App: args[0], Keep: revisionKeepVersions, Revisions: make([]revisionJSON, 0, len(resp.GetRevisions()))}
		for _, r := range resp.GetRevisions() {
			createdAt := ""
			if t := r.GetCreatedAt(); t != nil {
				createdAt = t.AsTime().Format(time.RFC3339)
			}
			out.Revisions = append(out.Revisions, revisionJSON{
				ID: r.GetId(), Seq: r.GetSeq(), DesiredHash: r.GetDesiredHash(),
				Status: r.GetStatus(), Verified: r.GetVerified(), CreatedAt: createdAt,
			})
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, out)
		}
		if len(out.Revisions) == 0 {
			_, err := fmt.Fprintf(env.Stdout, "%s: no revisions（尚无成功部署；可回滚目标 = 最近 %d 次成功部署）\n",
				args[0], revisionKeepVersions)
			return err
		}
		if _, err := fmt.Fprintf(env.Stdout, "app %s: %d revision(s) in the retention window (keep=%d)\n",
			args[0], len(out.Revisions), revisionKeepVersions); err != nil {
			return err
		}
		for _, r := range out.Revisions {
			if _, err := fmt.Fprintf(env.Stdout, "#%-3d %s  %s\n", r.Seq, r.ID, shortHash(r.DesiredHash)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── fleetly drift ───────────────────────────────────────────────────────────

// driftCmd 是外层动词 `drift`：分发 show/converge/enable/disable。
type driftCmd struct {
	sub *commands.App
}

func newDriftCmd() *driftCmd {
	sub := commands.New()
	sub.Register(&driftShowCmd{}, &driftConvergeCmd{}, &driftEnableCmd{}, &driftDisableCmd{})
	sub.VerbTitle = "drift subcommands:"
	return &driftCmd{sub: sub}
}

func (c *driftCmd) Name() string { return "drift" }
func (c *driftCmd) Synopsis() string {
	return "runtime drift detection (desired vs actual; convergence is opt-in per app)"
}
func (c *driftCmd) Usage() string { return "drift <show|converge|enable|disable> [flags] <app>" }

func (c *driftCmd) SetFlags(_ *flag.FlagSet) {}

func (c *driftCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (show|converge|enable|disable)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// driftShowCmd 实现 `fleetly drift show <app>`。
type driftShowCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *driftShowCmd) Name() string { return "show" }
func (c *driftShowCmd) Synopsis() string {
	return "compare desired state vs live services (field-level diff; env as key:hash)"
}
func (c *driftShowCmd) Usage() string {
	return "drift show [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *driftShowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *driftShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Drift().ShowDrift(ctx, &serverv1.ShowDriftRequest{App: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if resp.GetDesiredDeployment() == "" {
			_, err := fmt.Fprintf(env.Stdout, "%s: no succeeded deployment（无期望态可比对）\n", resp.GetApp())
			return err
		}
		if !resp.GetDrifted() {
			_, err := fmt.Fprintf(env.Stdout, "%s: no drift（期望与实况一致，基准 %s）\n",
				resp.GetApp(), resp.GetDesiredDeployment())
			return err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s: DRIFT detected (baseline %s)\n", resp.GetApp(), resp.GetDesiredDeployment())
		for _, s := range resp.GetServices() {
			switch {
			case s.GetMissing():
				fmt.Fprintf(&b, "  %s: MISSING（期望服务不存在）\n", s.GetService())
			case s.GetExtra():
				fmt.Fprintf(&b, "  %s: EXTRA（期望集之外的多余受管服务）\n", s.GetService())
			case s.GetDrifted():
				fmt.Fprintf(&b, "  %s:\n", s.GetService())
				for _, d := range s.GetDiff() {
					fmt.Fprintf(&b, "    %s: %s -> %s\n", d.GetField(), d.GetExpected(), d.GetActual())
				}
			default:
				fmt.Fprintf(&b, "  %s: ok\n", s.GetService())
			}
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// driftConvergeCmd 实现 `fleetly drift converge <app>`（人工一次性收敛，
// 带审计；不依赖 opt-in 位）。
type driftConvergeCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *driftConvergeCmd) Name() string { return "converge" }
func (c *driftConvergeCmd) Synopsis() string {
	return "converge an app to its desired state now (replay primitive; audited)"
}
func (c *driftConvergeCmd) Usage() string {
	return "drift converge [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *driftConvergeCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *driftConvergeCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Drift().ConvergeDrift(ctx, &serverv1.ConvergeDriftRequest{App: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		_, err = fmt.Fprintf(env.Stdout, "converged %s to desired state (deployment %s, desired_hash %.12s)\n",
			resp.GetApp(), resp.GetDeploymentId(), resp.GetDesiredHash())
		return err
	})
}

// driftEnableCmd / driftDisableCmd 实现收敛 opt-in 的人工置位（回滚失败
// 强制关闭后的唯一重置入口；带审计）。
type driftEnableCmd struct {
	jsonOut bool
	conn    connFlags
}
type driftDisableCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *driftEnableCmd) Name() string { return "enable" }
func (c *driftEnableCmd) Synopsis() string {
	return "enable per-app automatic drift convergence (default off)"
}
func (c *driftEnableCmd) Usage() string {
	return "drift enable [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *driftDisableCmd) Name() string     { return "disable" }
func (c *driftDisableCmd) Synopsis() string { return "disable per-app automatic drift convergence" }
func (c *driftDisableCmd) Usage() string {
	return "drift disable [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *driftEnableCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return runDriftToggle(ctx, env, c.Usage(), &c.conn, c.jsonOut, args, true)
}

func (c *driftDisableCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return runDriftToggle(ctx, env, c.Usage(), &c.conn, c.jsonOut, args, false)
}

func (c *driftEnableCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *driftDisableCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

// runDriftToggle 是 enable/disable 的共享执行体（RPC 置位 + 服务端审计）。
func runDriftToggle(ctx context.Context, env *commands.Environment, usage string, conn *connFlags, jsonOut bool, args []string, on bool) error {
	if err := requireArgs(usage, args, 1); err != nil {
		return err
	}
	return conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Drift().SetDriftConverge(ctx, &serverv1.SetDriftConvergeRequest{App: args[0], Enabled: on})
		if err != nil {
			return err
		}
		if jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		state := "disabled"
		if resp.GetEnabled() {
			state = "enabled"
		}
		_, err = fmt.Fprintf(env.Stdout, "drift convergence %s for %s\n", state, resp.GetApp())
		return err
	})
}

// shortHash 是哈希的展示截断。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// 编译期断言：回滚/版本/漂移命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &rollbackCmd{}
	_ commands.Flagged = &rollbackCmd{}
	_ commands.Command = &revisionsCmd{}
	_ commands.Flagged = &revisionsCmd{}
	_ commands.Command = &revisionsListCmd{}
	_ commands.Flagged = &revisionsListCmd{}
	_ commands.Command = &driftCmd{}
	_ commands.Flagged = &driftCmd{}
	_ commands.Command = &driftShowCmd{}
	_ commands.Flagged = &driftShowCmd{}
	_ commands.Command = &driftConvergeCmd{}
	_ commands.Flagged = &driftConvergeCmd{}
	_ commands.Command = &driftEnableCmd{}
	_ commands.Flagged = &driftEnableCmd{}
	_ commands.Command = &driftDisableCmd{}
	_ commands.Flagged = &driftDisableCmd{}
)
