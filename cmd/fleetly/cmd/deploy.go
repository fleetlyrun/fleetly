package cmd

// 部署命令（T2.18 CLI-over-SDK 改造）：`fleetly deploy <compose>` 经 RPC
// 入队（DeploymentsService.Deploy：compose 内容字节上行，服务端受控子集
// 校验 + 引擎入队）并等待终态（轮询 GetDeployment）→ 输出部署结果与 app
// 派生状态（GetApp）；`fleetly deployments list <app>` / `deployments
// cancel <id>` 同经 RPC。
//
// 等待终态的形态取舍（T2.18 设计裁决）：选 GetDeployment 轮询而非
// WatchEvents 事件流——轮询是幂等单请求、无需游标管理，断线重连语义与
// 既有 CLI 等待循环同构；事件流适合观察面（events watch 动词），不适合
// 作为部署等待的控制面原语。
//
// cancel 是受限语义：未切流可取消（引擎先归位再落 cancelled）；曾健康
// 409（E_STATE_VERSION_CONFLICT 信封，建议改用 rollback）——预检在服务端
// （API 面权威），CLI 只消费信封渲染。

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/compose"
)

// deployPollInterval 是 CLI 轮询部署行的周期。
const deployPollInterval = time.Second

// defaultDeployTimeout 是 deploy 等待缺省上限（看门狗 300s + 观察窗 60s 的
// 数倍；构建产物缺失会在准备/核对阶段快速失败，不会耗尽预算）。
const defaultDeployTimeout = 15 * time.Minute

// deployCmd 实现 `fleetly deploy <compose-file>`。
type deployCmd struct {
	jsonOut            bool
	confirmDestructive bool
	timeout            time.Duration
	conn               connFlags
}

func (c *deployCmd) Name() string { return "deploy" }
func (c *deployCmd) Synopsis() string {
	return "deploy a compose file (queued via fleetlyd, waits and prints the deployment result)"
}
func (c *deployCmd) Usage() string {
	return "deploy [--addr <host:port>] [--token <tok>] [--timeout <duration>] [--json] [--confirm-destructive] <compose-file>"
}

func (c *deployCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.DurationVar(&c.timeout, "timeout", defaultDeployTimeout, "wait limit for the deployment to finish")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
	// 破坏性变更门控（MG-C3，架构 §2.4 plan/apply 语义）：服务端以最新
	// revision 为基线判定（与 plan 的 requires_confirm_destructive 同口径）；
	// 删除服务/解绑卷的部署须显式携带本标志，否则 E_DEPLOY_CONFIRM_REQUIRED。
	fs.BoolVar(&c.confirmDestructive, "confirm-destructive", false,
		"confirm destructive changes (service removal / volume unbind) required to queue the deploy")
}

func (c *deployCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	content, err := osReadFile(args[0])
	if err != nil {
		return err
	}
	// 本地解析只为取应用名（Deploy 契约的 app 字段；权威校验在服务端——
	// compose 违约由 Deploy RPC 以 E_COMPOSE_* 信封拒绝）。
	spec, _, err := compose.Load(ctx, args[0])
	if err != nil {
		return err
	}
	// v0.3 W2-S3 归属管道：--project（或 FLEETLY_PROJECT/config 上下文）
	// 随请求上行——裸名或 team/prj 限定形；用户 PAT 缺省 = 个人队默认项目，
	// 机具令牌必须显式（否则服务端 400 带指引）。上下文解析单点 resolveContext
	//（flag > env > config）。
	rc, err := resolveContext(c.conn.team, c.conn.project)
	if err != nil {
		return err
	}
	err = c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Deployments().Deploy(ctx, &serverv1.DeployRequest{
			App:                spec.Name,
			Compose:            content,
			ConfirmDestructive: c.confirmDestructive,
			Project:            rc.Project,
		})
		if err != nil {
			return err
		}
		// 等待终态（daemon 不在运行 → queued 超时，给可行动提示）。
		final, err := waitDeployment(ctx, env, cl, resp.GetDeploymentId(), c.timeout, c.jsonOut)
		if err != nil {
			return err
		}
		// app 派生状态（读面即时推导）。同名跨项目（D-W0-4 二修）下裸名
		// 歧义：项目上下文为限定形时用 team/prj/app 取派生态——staging 真机
		// 演练（SV-11b）抓出「部署成功后尾查 GetApp 裸名 409」的错位。
		appRef := final.GetApp()
		if rc.Project != "" && strings.Contains(rc.Project, "/") {
			appRef = rc.Project + "/" + final.GetApp()
		}
		appResp, err := cl.Apps().GetApp(ctx, &serverv1.GetAppRequest{Name: appRef})
		if err != nil {
			return err
		}
		warnings := fromComposeWarnings(resp.GetWarnings())
		if err := emitDeployment(env, c.jsonOut, final, appResp.GetDerivedState(), warnings); err != nil {
			return err
		}
		if final.GetStatus() != "succeeded" {
			if code := final.GetErrorCode(); code != "" {
				return fmt.Errorf("deployment %s failed: %s (%s)", final.GetId(), code, final.GetPhase())
			}
			return fmt.Errorf("deployment %s ended as %s", final.GetId(), final.GetStatus())
		}
		return nil
	})
	if isCleanCancel(ctx, err) {
		// Ctrl-C/SIGTERM：等待循环取消干净退出（在途部署继续执行，
		// fleetly deployments list 可查）——exit 0（S17-D3）。
		return nil
	}
	return err
}

// waitDeployment 轮询至部署到终态或超时。超时区分「仍 queued」（daemon 未
// 运行）与「推进中超时」（在途部署继续执行，deployments list 可查）。
func waitDeployment(ctx context.Context, env *commands.Environment, cl *fleetlyClient, id string, timeout time.Duration, quiet bool) (*serverv1.DeploymentView, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		resp, err := cl.Deployments().GetDeployment(ctx, &serverv1.GetDeploymentRequest{Id: id})
		if err != nil {
			return nil, err
		}
		rec := resp.GetDeployment()
		if isTerminalStatus(rec.GetStatus()) {
			if !quiet {
				line := fmt.Sprintf("deployment %s %s", rec.GetId(), rec.GetStatus())
				if rec.GetErrorCode() != "" {
					line += "  error=" + rec.GetErrorCode()
				}
				if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
					return rec, err
				}
			}
			return rec, nil
		}
		select {
		case <-deadline.C:
			if rec.GetStatus() == "queued" {
				return rec, fmt.Errorf("deployment stayed queued for %s — is fleetlyd running? deployments are executed by fleetlyd's engine.release service (this command only enqueues and waits)", timeout)
			}
			return rec, fmt.Errorf("deployment did not finish within %s (in-flight deployments keep running; check 'fleetly deployments list')", timeout)
		case <-ctx.Done():
			return rec, ctx.Err()
		case <-time.After(deployPollInterval):
		}
	}
}

// isTerminalStatus 报告部署状态词是否终态（词表与 state 层一致）。
func isTerminalStatus(s string) bool {
	switch s {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// emitDeployment 输出部署/回滚结果（--json 机器形态 / 人读摘要）。
func emitDeployment(env *commands.Environment, jsonOut bool, rec *serverv1.DeploymentView, appState string, warnings []compose.Warning) error {
	if jsonOut {
		return writeJSON(env.Stdout, deploymentResultJSON{
			Deployment: toDeploymentJSON(rec),
			AppState:   appState,
			Warnings:   warnings,
		})
	}
	var b strings.Builder
	writeWarnings(&b, warnings)
	fmt.Fprintf(&b, "deployment %s\n  app: %s (%s)\n  status: %s", rec.GetId(), rec.GetApp(), appState, rec.GetStatus())
	if rec.GetPhase() != "" {
		fmt.Fprintf(&b, " (%s)", rec.GetPhase())
	}
	b.WriteString("\n")
	if rec.GetErrorCode() != "" {
		fmt.Fprintf(&b, "  error: %s\n", rec.GetErrorCode())
	}
	if rec.GetVerdict() != "" {
		fmt.Fprintf(&b, "  verdict: %s\n", rec.GetVerdict())
	}
	if rec.GetRecovery() != "" {
		fmt.Fprintf(&b, "  recovery: %s\n", rec.GetRecovery())
	}
	if rec.GetDowntimeMs() > 0 {
		fmt.Fprintf(&b, "  downtime: %dms\n", rec.GetDowntimeMs())
	}
	if rec.GetSubstrateHalted() {
		b.WriteString("  substrate_halted: true (failed first deploy; kept at scale=0 for inspection)\n")
	}
	if rec.GetRevisionId() != "" {
		fmt.Fprintf(&b, "  revision: %s\n", rec.GetRevisionId())
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
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// deploymentsListCmd 实现 `fleetly deployments list <app>`。
type deploymentsListCmd struct {
	jsonOut bool
	limit   int
	conn    connFlags
}

func (c *deploymentsListCmd) Name() string { return "list" }
func (c *deploymentsListCmd) Synopsis() string {
	return "list deployments of an app (newest first)"
}
func (c *deploymentsListCmd) Usage() string {
	return "deployments list [--addr <host:port>] [--token <tok>] [--limit N] [--json] <app>"
}

func (c *deploymentsListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.IntVar(&c.limit, "limit", 20, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *deploymentsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		app, rows, err := listDeployments(ctx, cl, args[0], c.limit)
		if err != nil {
			return err
		}
		if c.jsonOut {
			out := struct {
				App      string           `json:"app"`
				AppState string           `json:"app_state"`
				Rows     []deploymentJSON `json:"deployments"`
			}{App: app.appName, AppState: app.derivedState, Rows: make([]deploymentJSON, 0, len(rows))}
			for _, rec := range rows {
				out.Rows = append(out.Rows, toDeploymentJSON(rec))
			}
			return writeJSON(env.Stdout, out)
		}
		if len(rows) == 0 {
			_, err := fmt.Fprintf(env.Stdout, "%s: no deployments (run 'fleetly deploy <compose>' to deploy)\n", app.appName)
			return err
		}
		if _, err := fmt.Fprintf(env.Stdout, "app %s: %s\n", app.appName, app.derivedState); err != nil {
			return err
		}
		for _, rec := range rows {
			line := fmt.Sprintf("%s  %-9s %-10s", rec.GetId(), rec.GetStatus(), rec.GetKind())
			if rec.GetPhase() != "" {
				line += "/" + rec.GetPhase()
			}
			if rec.GetErrorCode() != "" {
				line += "  error=" + rec.GetErrorCode()
			}
			if rec.GetVerdict() != "" {
				line += "  verdict=" + rec.GetVerdict()
			}
			if rec.GetRecovery() != "" {
				line += "  recovery=" + rec.GetRecovery()
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
		return nil
	})
}

// appView 是命令内传递的 app 名 + 派生状态（GetApp 即时推导）。
type appView struct {
	appName      string
	derivedState string
}

// listDeployments 取 app 派生状态与部署列表（list 面公共读取）。
func listDeployments(ctx context.Context, cl *fleetlyClient, name string, limit int) (appView, []*serverv1.DeploymentView, error) {
	appResp, err := cl.Apps().GetApp(ctx, &serverv1.GetAppRequest{Name: name})
	if err != nil {
		return appView{}, nil, err
	}
	listResp, err := cl.Deployments().ListDeployments(ctx, &serverv1.ListDeploymentsRequest{App: name, Limit: int32Clamp(limit)})
	if err != nil {
		return appView{}, nil, err
	}
	return appView{appName: appResp.GetName(), derivedState: appResp.GetDerivedState()}, listResp.GetDeployments(), nil
}

// deploymentsCancelCmd 实现 `fleetly deployments cancel <id>`。
type deploymentsCancelCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *deploymentsCancelCmd) Name() string { return "cancel" }
func (c *deploymentsCancelCmd) Synopsis() string {
	return "cancel a not-yet-healthy deployment (restore first; 409 after switch)"
}
func (c *deploymentsCancelCmd) Usage() string {
	return "deployments cancel [--addr <host:port>] [--token <tok>] [--json] <deployment-id>"
}

func (c *deploymentsCancelCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *deploymentsCancelCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	err := c.conn.withClient(func(cl *fleetlyClient) error {
		id := args[0]
		if _, err := cl.Deployments().CancelDeployment(ctx, &serverv1.CancelDeploymentRequest{Id: id}); err != nil {
			return err // 曾健康/终态 409 信封（建议 rollback）由 renderCLIError 渲染
		}
		// 等待引擎消费（归位 + cancelled 终态）；竞态切流时引擎继续推进到
		// succeeded——两者都是终态，按实际终态报告。
		deadline := time.NewTimer(60 * time.Second)
		defer deadline.Stop()
		for {
			resp, err := cl.Deployments().GetDeployment(ctx, &serverv1.GetDeploymentRequest{Id: id})
			if err != nil {
				return err
			}
			row := resp.GetDeployment()
			if isTerminalStatus(row.GetStatus()) {
				if c.jsonOut {
					return writeJSON(env.Stdout, toDeploymentJSON(row))
				}
				line := fmt.Sprintf("deployment %s %s", row.GetId(), row.GetStatus())
				if row.GetRecovery() != "" {
					line += " (recovery=" + row.GetRecovery() + ")"
				}
				_, err := fmt.Fprintln(env.Stdout, line)
				return err
			}
			select {
			case <-deadline.C:
				return fmt.Errorf("cancel not consumed within 60s — is fleetlyd running? (cancel_requested is set; it is executed once the engine starts)")
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(deployPollInterval):
			}
		}
	})
	if isCleanCancel(ctx, err) {
		return nil // Ctrl-C/SIGTERM：等待循环取消干净退出，exit 0（S17-D3）
	}
	return err
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
