package main

// 部署命令（T2-5a）：`fleetly deploy <compose>` 入队并等待终态（轮询
// deployments 行）→ 输出部署结果与 app 派生状态；`fleetly deployments
// list <app>` / `deployments cancel <id>`。与 build/env 同形态：CLI 直连
// 本地状态库（gRPC API 面随 T2.17 统一接线）。
//
// 执行拓扑：入队（deployments queued 行）与执行（fleetlyd engine.release
// 服务扫描推进）跨进程解耦——daemon 不在运行时部署停留 queued（--timeout
// 超时给出可行动提示）。同 app 互斥由引擎承载（第二个入队 queued 等待）。
// cancel 是受限语义：未切流可取消（引擎先归位再落 cancelled）；曾健康
// 409（apperr 信封，建议改用 rollback）。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// deployPollInterval 是 CLI 轮询 deployments 行的周期。
const deployPollInterval = time.Second

// defaultDeployTimeout 是 deploy 等待缺省上限（看门狗 300s + 观察窗 60s 的
// 数倍；构建产物缺失会在准备/核对阶段快速失败，不会耗尽预算）。
const defaultDeployTimeout = 15 * time.Minute

// deployCmd 实现 `fleetly deploy <compose-file>`。
type deployCmd struct {
	jsonOut bool
	dbPath  string
	keyPath string
	timeout time.Duration
}

func (c *deployCmd) Name() string { return "deploy" }
func (c *deployCmd) Synopsis() string {
	return "deploy a compose file (queued into fleetlyd, waits and prints the deployment result)"
}
func (c *deployCmd) Usage() string {
	return "deploy [--db <state-db>] [--key <path>] [--timeout <duration>] [--json] <compose-file>"
}

func (c *deployCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.StringVar(&c.keyPath, "key", defaultKeyPath, "master key file path (env decrypt/snapshot seal)")
	fs.DurationVar(&c.timeout, "timeout", defaultDeployTimeout, "wait limit for the deployment to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *deployCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	abs, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve compose path: %w", err)
	}
	// 入队前受控子集校验（场景 1：compose 违约不动底座、不入队）。
	spec, warnings, err := compose.Load(ctx, abs)
	if err != nil {
		return err
	}

	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	app, err := ensureApp(ctx, st, spec.Name)
	if err != nil {
		return err
	}

	// 入队：spec_hash/compose_path 随行落库（daemon preparing 重载复核）。
	rec, err := st.CreateDeployment(ctx, state.DeployRecord{
		AppID:       app.ID,
		AppName:     spec.Name,
		Kind:        "deploy",
		SpecHash:    spec.SpecHash,
		ComposePath: abs,
	})
	if err != nil {
		return err
	}
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		if _, err := tx.AppendEvent(ctx, state.Event{
			Name:    "deployment.queued",
			Subject: "deployment:" + rec.ID,
			Payload: `{"deployment":"` + rec.ID + `","app":"` + spec.Name + `"}`,
		}); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "human",
			Action:      "deployment.create",
			Target:      "deployment:" + rec.ID,
			Result:      "ok",
			DiffSummary: `{"app":"` + spec.Name + `","spec_hash":"` + spec.SpecHash + `"}`,
		})
	}); err != nil {
		return err
	}

	// 等待终态（daemon 不在运行 → queued 超时，给可行动提示）。
	final, err := waitDeployment(ctx, env, st, rec.ID, c.timeout, c.jsonOut)
	if err != nil {
		return err
	}

	// app 派生状态（读面即时推导）。
	appState, err := appDerivedState(ctx, st, app.ID)
	if err != nil {
		return err
	}
	if err := c.emit(env, final, appState, warnings); err != nil {
		return err
	}
	if final.Status != state.DeploySucceeded {
		if final.ErrorCode != "" {
			return fmt.Errorf("deployment %s failed: %s（%s）", final.ID, final.ErrorCode, final.Phase)
		}
		return fmt.Errorf("deployment %s ended as %s", final.ID, final.Status)
	}
	return nil
}

// waitDeployment 轮询至部署到终态或超时。超时区分「仍 queued」（daemon 未
// 运行）与「推进中超时」（在途部署继续执行，deployments list 可查）。
func waitDeployment(ctx context.Context, env *commands.Environment, st *state.Store, id string, timeout time.Duration, quiet bool) (state.DeployRecord, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		rec, err := st.GetDeployment(ctx, id)
		if err != nil {
			return state.DeployRecord{}, err
		}
		if rec.Status.Terminal() {
			if !quiet {
				line := fmt.Sprintf("deployment %s %s", rec.ID, rec.Status)
				if rec.ErrorCode != "" {
					line += "  error=" + rec.ErrorCode
				}
				if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
					return rec, err
				}
			}
			return rec, nil
		}
		select {
		case <-deadline.C:
			if rec.Status == state.DeployQueued {
				return rec, fmt.Errorf("deployment stayed queued for %s——fleetlyd 未运行？部署由 fleetlyd 的 engine.release 服务执行（本命令只入队与等待）", timeout)
			}
			return rec, fmt.Errorf("deployment did not finish within %s（在途部署继续执行，fleetly deployments list 可查）", timeout)
		case <-ctx.Done():
			return rec, ctx.Err()
		case <-time.After(deployPollInterval):
		}
	}
}

// appDerivedState 读面即时推导 app 派生状态（state-model §2.10 纯函数，
// 与引擎共用同一实现）。
func appDerivedState(ctx context.Context, st *state.Store, appID string) (string, error) {
	facts := engine.AppFacts{}
	if p, err := st.GetPlacement(ctx, appID); err == nil {
		facts.PlacementState = string(p.State)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return "", err
	}
	if rows, err := st.ListAppDeployments(ctx, appID, 1); err == nil && len(rows) > 0 {
		facts.Latest = rows[0]
	} else if err != nil {
		return "", err
	}
	rows, err := st.ListAppDeployments(ctx, appID, 25)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Status == state.DeploySucceeded {
			facts.LatestSucceeded = r
			break
		}
	}
	return engine.DeriveAppState(facts), nil
}

// ensureApp 取应用行；不存在则创建（部署常是应用的第一个平台动作，同
// build 模式——应用随首次部署自动创建）。
func ensureApp(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, state.ErrAppNotFound) {
		return state.App{}, err
	}
	created, err := st.CreateApp(ctx, "", name)
	if err != nil {
		return state.App{}, fmt.Errorf("create app %s: %w", name, err)
	}
	return created, nil
}

// deployRecordJSON 是部署行的机器形态（时间 RFC3339、枚举原样）。
type deployRecordJSON struct {
	ID              string `json:"id"`
	App             string `json:"app"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Phase           string `json:"phase,omitempty"`
	RevisionID      string `json:"revision_id,omitempty"`
	SubstrateHalted bool   `json:"substrate_halted,omitempty"`
	FirstHealthyAt  string `json:"first_healthy_at,omitempty"`
	Recovery        string `json:"recovery,omitempty"`
	Verdict         string `json:"verdict,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	DowntimeMS      int64  `json:"downtime_ms,omitempty"`
	SpecHash        string `json:"spec_hash,omitempty"`
	DesiredHash     string `json:"desired_hash,omitempty"`
	EnvSnapshotHash string `json:"env_snapshot_hash,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

func toDeployRecordJSON(rec state.DeployRecord) deployRecordJSON {
	out := deployRecordJSON{
		ID:              rec.ID,
		App:             rec.AppName,
		Kind:            rec.Kind,
		Status:          string(rec.Status),
		Phase:           rec.Phase,
		RevisionID:      rec.RevisionID,
		SubstrateHalted: rec.SubstrateHalted,
		Recovery:        rec.Recovery,
		Verdict:         rec.Verdict,
		ErrorCode:       rec.ErrorCode,
		DowntimeMS:      rec.DowntimeMS,
		SpecHash:        rec.SpecHash,
		DesiredHash:     rec.DesiredHash,
		EnvSnapshotHash: rec.EnvSnapshotHash,
	}
	if !rec.FirstHealthyAt.IsZero() {
		out.FirstHealthyAt = rec.FirstHealthyAt.Format(time.RFC3339)
	}
	if !rec.CreatedAt.IsZero() {
		out.CreatedAt = rec.CreatedAt.Format(time.RFC3339)
	}
	if !rec.UpdatedAt.IsZero() {
		out.UpdatedAt = rec.UpdatedAt.Format(time.RFC3339)
	}
	return out
}

// deployResultJSON 是 deploy --json 输出形态。
type deployResultJSON struct {
	Deployment deployRecordJSON  `json:"deployment"`
	AppState   string            `json:"app_state"`
	Warnings   []compose.Warning `json:"warnings,omitempty"`
}

func (c *deployCmd) emit(env *commands.Environment, rec state.DeployRecord, appState string, warnings []compose.Warning) error {
	if c.jsonOut {
		return writeJSON(env, deployResultJSON{
			Deployment: toDeployRecordJSON(rec),
			AppState:   appState,
			Warnings:   warnings,
		})
	}
	var b strings.Builder
	writeWarnings(&b, warnings)
	fmt.Fprintf(&b, "deployment %s\n  app: %s (%s)\n  status: %s", rec.ID, rec.AppName, appState, rec.Status)
	if rec.Phase != "" {
		fmt.Fprintf(&b, " (%s)", rec.Phase)
	}
	b.WriteString("\n")
	if rec.ErrorCode != "" {
		fmt.Fprintf(&b, "  error: %s\n", rec.ErrorCode)
	}
	if rec.Verdict != "" {
		fmt.Fprintf(&b, "  verdict: %s\n", rec.Verdict)
	}
	if rec.Recovery != "" {
		fmt.Fprintf(&b, "  recovery: %s\n", rec.Recovery)
	}
	if rec.DowntimeMS > 0 {
		fmt.Fprintf(&b, "  downtime: %dms\n", rec.DowntimeMS)
	}
	if rec.SubstrateHalted {
		b.WriteString("  substrate_halted: true（首发失败 scale=0 保留现场）\n")
	}
	if rec.RevisionID != "" {
		fmt.Fprintf(&b, "  revision: %s\n", rec.RevisionID)
	}
	_, err := fmt.Fprint(env.Stdout, b.String())
	return err
}

// ── fleetly deployments ─────────────────────────────────────────────────────

// deploymentsCmd 是外层动词 `deployments`：分发 list/cancel。
type deploymentsCmd struct {
	sub *commands.App
}

func newDeploymentsCmd() *deploymentsCmd {
	sub := commands.New()
	sub.Register(&deploymentsListCmd{}, &deploymentsCancelCmd{})
	sub.VerbTitle = "deployments subcommands:"
	return &deploymentsCmd{sub: sub}
}

func (c *deploymentsCmd) Name() string { return "deployments" }
func (c *deploymentsCmd) Synopsis() string {
	return "deployment history and cancel (release state machine records)"
}
func (c *deploymentsCmd) Usage() string {
	return "deployments <list|cancel> [flags] ..."
}

func (c *deploymentsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *deploymentsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|cancel)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// deploymentsListCmd 实现 `fleetly deployments list <app>`。
type deploymentsListCmd struct {
	jsonOut bool
	dbPath  string
	limit   int
}

func (c *deploymentsListCmd) Name() string { return "list" }
func (c *deploymentsListCmd) Synopsis() string {
	return "list deployments of an app (newest first)"
}
func (c *deploymentsListCmd) Usage() string {
	return "deployments list [--db <state-db>] [--limit N] [--json] <app>"
}

func (c *deploymentsListCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
	fs.IntVar(&c.limit, "limit", 20, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *deploymentsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
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
	rows, err := st.ListAppDeployments(ctx, app.ID, c.limit)
	if err != nil {
		return err
	}
	appState, err := appDerivedState(ctx, st, app.ID)
	if err != nil {
		return err
	}
	if c.jsonOut {
		out := struct {
			App      string             `json:"app"`
			AppState string             `json:"app_state"`
			Rows     []deployRecordJSON `json:"deployments"`
		}{App: app.Name, AppState: appState, Rows: make([]deployRecordJSON, 0, len(rows))}
		for _, rec := range rows {
			out.Rows = append(out.Rows, toDeployRecordJSON(rec))
		}
		return writeJSON(env, out)
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintf(env.Stdout, "%s: no deployments（fleetly deploy <compose> 发起部署）\n", app.Name)
		return err
	}
	if _, err := fmt.Fprintf(env.Stdout, "app %s: %s\n", app.Name, appState); err != nil {
		return err
	}
	for _, rec := range rows {
		line := fmt.Sprintf("%s  %-9s %-10s", rec.ID, rec.Status, rec.Kind)
		if rec.Phase != "" {
			line += "/" + rec.Phase
		}
		if rec.ErrorCode != "" {
			line += "  error=" + rec.ErrorCode
		}
		if rec.Verdict != "" {
			line += "  verdict=" + rec.Verdict
		}
		if rec.Recovery != "" {
			line += "  recovery=" + rec.Recovery
		}
		if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
			return err
		}
	}
	return nil
}

// deploymentsCancelCmd 实现 `fleetly deployments cancel <id>`。
type deploymentsCancelCmd struct {
	dbPath string
}

func (c *deploymentsCancelCmd) Name() string { return "cancel" }
func (c *deploymentsCancelCmd) Synopsis() string {
	return "cancel a not-yet-healthy deployment (restore first; 409 after switch)"
}
func (c *deploymentsCancelCmd) Usage() string {
	return "deployments cancel [--db <state-db>] <deployment-id>"
}

func (c *deploymentsCancelCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.dbPath, "db", defaultDBPath, "state db path (shared with fleetlyd)")
}

func (c *deploymentsCancelCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, cleanup, err := openStore(c.dbPath)
	if err != nil {
		return err
	}
	defer func() { cleanup() }()

	rec, err := st.GetDeployment(ctx, args[0])
	if err != nil {
		return err
	}
	// 409 语义在 CLI 侧预检（曾健康/终态不可 cancel——apperr 信封渲染）；
	// 引擎侧二次校验（权威）。
	if !rec.FirstHealthyAt.IsZero() || rec.Status == state.DeployObserving || rec.Status.Terminal() {
		return apperr.New("E_STATE_VERSION_CONFLICT",
			"deployment %s 已切流或已是终态（%s），不可 cancel：曾健康请改用 rollback", rec.ID, rec.Status).
			WithContext("deployment", rec.ID).
			WithContext("reason", "already_switched")
	}
	set := true
	if err := st.UpdateDeployment(ctx, rec.ID, state.DeploymentPatch{CancelRequested: &set}); err != nil {
		return err
	}
	// 等待引擎消费（归位 + cancelled 终态）；引擎拒绝（竞态切流）时清位，
	// 此处按超时口径给出指引。
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	for {
		row, err := st.GetDeployment(ctx, rec.ID)
		if err != nil {
			return err
		}
		if row.Status.Terminal() {
			_, err = fmt.Fprintf(env.Stdout, "deployment %s %s", row.ID, row.Status)
			if err == nil && row.Recovery != "" {
				_, err = fmt.Fprintf(env.Stdout, " (recovery=%s)", row.Recovery)
			}
			if err == nil {
				_, err = fmt.Fprintln(env.Stdout)
			}
			return err
		}
		if !row.CancelRequested {
			return apperr.New("E_STATE_VERSION_CONFLICT",
				"deployment %s 在取消前已切流（曾健康），不可 cancel：建议改用 rollback", rec.ID).
				WithContext("deployment", rec.ID).
				WithContext("reason", "already_switched")
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("cancel not consumed within 60s——fleetlyd 未运行？（cancel_requested 已置位，引擎启动后执行）")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(deployPollInterval):
		}
	}
}

// 编译期断言：部署命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &deployCmd{}
	_ commands.Flagged = &deployCmd{}
	_ commands.Command = &deploymentsCmd{}
	_ commands.Flagged = &deploymentsCmd{}
	_ commands.Command = &deploymentsListCmd{}
	_ commands.Flagged = &deploymentsListCmd{}
	_ commands.Command = &deploymentsCancelCmd{}
	_ commands.Flagged = &deploymentsCancelCmd{}
)
