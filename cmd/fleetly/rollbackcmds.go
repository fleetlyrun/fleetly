package main

// 回滚与版本/漂移命令（T2-5b）：`fleetly rollback <app>`（快照重放回滚，
// 入队 kind=rollback 部署并等待终态）、`fleetly revisions list <app>`
// （保留窗内可回滚目标——列表即选项）、`fleetly drift show|converge|
// enable|disable`（运行域漂移检测面：show 即时判定、converge 人工一次性
// 收敛、enable/disable 收敛 opt-in 的人工重置入口）。与 deploy 同形态：
// CLI 直连本地状态库；rollback 入队经 engine.EnqueueRollback（目标解析 +
// 审计 + rollback_started 事件），执行由 fleetlyd 引擎扫描推进。

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// ── fleetly rollback ────────────────────────────────────────────────────────

// rollbackCmd 实现 `fleetly rollback <app> [--to <revision>]`。
type rollbackCmd struct {
	jsonOut bool
	dbPath  string
	keyPath string
	timeout time.Duration
	to      string
}

func (c *rollbackCmd) Name() string { return "rollback" }
func (c *rollbackCmd) Synopsis() string {
	return "roll an app back to a revision (snapshot replay; default = latest revision)"
}
func (c *rollbackCmd) Usage() string {
	return "rollback [--db <state-db>] [--key <path>] [--to <revision>] [--timeout <duration>] [--json] <app>"
}

func (c *rollbackCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.StringVar(&c.keyPath, "key", defaultKeyPath, "master key file path")
	fs.StringVar(&c.to, "to", "", "target revision id (default: latest revision in the retention window)")
	fs.DurationVar(&c.timeout, "timeout", defaultDeployTimeout, "wait limit for the rollback to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *rollbackCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	rec, err := engine.EnqueueRollback(ctx, st, engine.RollbackInput{
		AppName:          args[0],
		TargetRevisionID: c.to,
		Actor:            "human",
	})
	if err != nil {
		return err
	}

	final, err := waitDeployment(ctx, env, st, rec.ID, c.timeout, c.jsonOut)
	if err != nil {
		return err
	}
	appState, err := appDerivedState(ctx, st, rec.AppID)
	if err != nil {
		return err
	}
	if c.jsonOut {
		return writeJSON(env, deployResultJSON{
			Deployment: toDeployRecordJSON(final),
			AppState:   appState,
		})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "rollback %s\n  app: %s (%s)\n  status: %s\n", rec.ID, rec.AppName, appState, final.Status)
	if final.ErrorCode != "" {
		fmt.Fprintf(&b, "  error: %s\n", final.ErrorCode)
	}
	if final.Verdict != "" {
		fmt.Fprintf(&b, "  verdict: %s\n", final.Verdict)
	}
	if final.RevisionID != "" {
		fmt.Fprintf(&b, "  revision: %s（回退版本已固化为新版本）\n", final.RevisionID)
	}
	if _, err := fmt.Fprint(env.Stdout, b.String()); err != nil {
		return err
	}
	if final.Status != state.DeploySucceeded {
		if final.ErrorCode != "" {
			return fmt.Errorf("rollback %s failed: %s", final.ID, final.ErrorCode)
		}
		return fmt.Errorf("rollback %s ended as %s", final.ID, final.Status)
	}
	return nil
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
	dbPath  string
}

func (c *revisionsListCmd) Name() string { return "list" }
func (c *revisionsListCmd) Synopsis() string {
	return "list rollback targets of an app (newest first, retention window = 5)"
}
func (c *revisionsListCmd) Usage() string {
	return "revisions list [--db <state-db>] [--json] <app>"
}

func (c *revisionsListCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *revisionsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	rows, err := st.ListRevisions(ctx, app.ID)
	if err != nil {
		return err
	}
	type revisionJSON struct {
		ID          string `json:"id"`
		Seq         int64  `json:"seq"`
		DesiredHash string `json:"desired_hash,omitempty"`
		CreatedAt   string `json:"created_at,omitempty"`
	}
	out := struct {
		App       string         `json:"app"`
		Keep      int            `json:"keep_versions"`
		Revisions []revisionJSON `json:"revisions"`
	}{App: app.Name, Keep: state.RevisionKeepVersions, Revisions: make([]revisionJSON, 0, len(rows))}
	for _, r := range rows {
		out.Revisions = append(out.Revisions, revisionJSON{
			ID: r.ID, Seq: r.Seq, DesiredHash: r.DesiredHash,
			CreatedAt: r.CreatedAt.Format(time.RFC3339),
		})
	}
	if c.jsonOut {
		return writeJSON(env, out)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(env.Stdout, "%s: no revisions（尚无成功部署；可回滚目标 = 最近 %d 次成功部署）\n",
			app.Name, state.RevisionKeepVersions)
		return err
	}
	if _, err := fmt.Fprintf(env.Stdout, "app %s: %d revision(s) in the retention window (keep=%d)\n",
		app.Name, len(rows), state.RevisionKeepVersions); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(env.Stdout, "#%-3d %s  %s\n", r.Seq, r.ID, shortHash(r.DesiredHash)); err != nil {
			return err
		}
	}
	return nil
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

// driftEngine 组装漂移命令的引擎依赖（store + box + 底座只读/写面）。
// images/placement 依赖传 nil——drift 面不消费（只触 DriftShow/Converge/
// SetDriftConverge；引擎其余方法不经此 CLI 路径）。
func driftEngine(ctx context.Context, dbPath, keyPath, dockerHost string) (*engine.Engine, func(), error) {
	st, cleanup, err := openStore(dbPath)
	if err != nil {
		return nil, nil, err
	}
	box, _, err := loadBox(keyPath, false)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	sub, err := substrate.NewClient(dockerHost)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	eng := engine.NewEngine(engine.Config{}, st, sub, nil, nil, box,
		slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	return eng, cleanup, nil
}

// discardWriter 适配 slog 到 stderr 之外的空输出（CLI 漂移读面的日志静音）。
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// driftShowCmd 实现 `fleetly drift show <app>`。
type driftShowCmd struct {
	jsonOut    bool
	dbPath     string
	keyPath    string
	dockerHost string
}

func (c *driftShowCmd) Name() string { return "show" }
func (c *driftShowCmd) Synopsis() string {
	return "compare desired state vs live services (field-level diff; env as key:hash)"
}
func (c *driftShowCmd) Usage() string {
	return "drift show [--db <state-db>] [--key <path>] [--docker-host <uri>] [--json] <app>"
}

func (c *driftShowCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.StringVar(&c.keyPath, "key", defaultKeyPath, "master key file path")
	fs.StringVar(&c.dockerHost, "docker-host", "", "docker daemon endpoint (default: DOCKER_HOST or local socket)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *driftShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	eng, cleanup, err := driftEngine(ctx, c.dbPath, c.keyPath, c.dockerHost)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	report, err := eng.DriftShow(ctx, args[0])
	if err != nil {
		return err
	}
	if c.jsonOut {
		return writeJSON(env, report)
	}
	if report.DesiredDeployment == "" {
		_, err := fmt.Fprintf(env.Stdout, "%s: no succeeded deployment（无期望态可比对）\n", report.App)
		return err
	}
	if !report.Drifted {
		_, err := fmt.Fprintf(env.Stdout, "%s: no drift（期望与实况一致，基准 %s）\n",
			report.App, report.DesiredDeployment)
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: DRIFT detected (baseline %s)\n", report.App, report.DesiredDeployment)
	for _, s := range report.Services {
		switch {
		case s.Missing:
			fmt.Fprintf(&b, "  %s: MISSING（期望服务不存在）\n", s.Service)
		case s.Extra:
			fmt.Fprintf(&b, "  %s: EXTRA（期望集之外的多余受管服务）\n", s.Service)
		case s.Drifted:
			fmt.Fprintf(&b, "  %s:\n", s.Service)
			for _, d := range s.Diff {
				fmt.Fprintf(&b, "    %s: %s -> %s\n", d.Field, d.Expected, d.Actual)
			}
		default:
			fmt.Fprintf(&b, "  %s: ok\n", s.Service)
		}
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
}

// driftConvergeCmd 实现 `fleetly drift converge <app>`（人工一次性收敛，
// 带审计；不依赖 opt-in 位）。
type driftConvergeCmd struct {
	dbPath     string
	keyPath    string
	dockerHost string
}

func (c *driftConvergeCmd) Name() string { return "converge" }
func (c *driftConvergeCmd) Synopsis() string {
	return "converge an app to its desired state now (replay primitive; audited)"
}
func (c *driftConvergeCmd) Usage() string {
	return "drift converge [--db <state-db>] [--key <path>] [--docker-host <uri>] <app>"
}

func (c *driftConvergeCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.StringVar(&c.keyPath, "key", defaultKeyPath, "master key file path")
	fs.StringVar(&c.dockerHost, "docker-host", "", "docker daemon endpoint (default: DOCKER_HOST or local socket)")
}

func (c *driftConvergeCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	eng, cleanup, err := driftEngine(ctx, c.dbPath, c.keyPath, c.dockerHost)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	source, err := eng.ConvergeApp(ctx, args[0], "human")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(env.Stdout, "converged %s to desired state (deployment %s, desired_hash %.12s)\n",
		args[0], source.ID, source.DesiredHash)
	return err
}

// driftEnableCmd / driftDisableCmd 实现收敛 opt-in 的人工置位（回滚失败
// 强制关闭后的唯一重置入口；带审计）。
type driftEnableCmd struct {
	dbPath string
}
type driftDisableCmd struct {
	dbPath string
}

func (c *driftEnableCmd) Name() string { return "enable" }
func (c *driftEnableCmd) Synopsis() string {
	return "enable per-app automatic drift convergence (default off)"
}
func (c *driftEnableCmd) Usage() string { return "drift enable [--db <state-db>] <app>" }

func (c *driftDisableCmd) Name() string     { return "disable" }
func (c *driftDisableCmd) Synopsis() string { return "disable per-app automatic drift convergence" }
func (c *driftDisableCmd) Usage() string    { return "drift disable [--db <state-db>] <app>" }

func (c *driftEnableCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return runDriftToggle(ctx, c.Usage(), c.dbPath, args, true)
}

func (c *driftDisableCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	return runDriftToggle(ctx, c.Usage(), c.dbPath, args, false)
}

func (c *driftEnableCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
}

func (c *driftDisableCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
}

// runDriftToggle 是 enable/disable 的共享执行体（置位 + 审计同事务）。
func runDriftToggle(ctx context.Context, usage, dbPath string, args []string, on bool) error {
	if err := requireArgs(usage, args, 1); err != nil {
		return err
	}
	st, cleanup, err := openStore(dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.SetAppDriftConverge(ctx, app.ID, on); err != nil {
			return err
		}
		action := "reconcile.drift_converge_enabled"
		if !on {
			action = "reconcile.drift_converge_disabled"
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "human",
			Action:      action,
			Target:      "app:" + app.Name,
			Result:      "ok",
			DiffSummary: `{"enabled":` + boolLiteral(on) + `}`,
		})
	}); err != nil {
		return err
	}
	return nil
}

// boolLiteral 是布尔的 JSON 字面量。
func boolLiteral(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
