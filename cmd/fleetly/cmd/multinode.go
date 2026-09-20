package cmd

// 多节点动词（E1-7/E1-8，multi-node §2.3/§2.6/§2.8，CLI-over-SDK）：
//
//	nodes join-guide     join 向导（join 命令 + 精确防火墙规则 + DNS 步骤
//	                     + 完成判据 + HA 边界诚实口径尾部）；base_domain
//	                     缺失 → 服务端 409 信封渲染（D-MN-13）。
//	nodes rotate-token   swarm join token 轮换（D-MN-1；旧 token 即刻失效）。
//	placement rebind     显式换点（破坏性确认：强制 --confirm-destructive +
//	                     回显；有卷应用 --data-restored / --discard 二选一
//	                     声明数据处置）。
//	placement migrate    restic 迁移 runbook（服务端生成步骤文档，只读）。
//	volumes list         跨 app 卷清单（孤儿 + residual 残留指引）。
//
// 全动词支持 --json；连接参数与退出码语义与既有动词一致。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// ── fleetly nodes join-guide / rotate-token ─────────────────────────────────

// nodesJoinGuideCmd 实现 `fleetly nodes join-guide`。
type nodesJoinGuideCmd struct {
	jsonOut     bool
	conn        connFlags
	workerIP    string
	managerAddr string
}

func (c *nodesJoinGuideCmd) Name() string { return "join-guide" }
func (c *nodesJoinGuideCmd) Synopsis() string {
	return "generate the worker join guide (join command, firewall rules, DNS steps, completion checks)"
}
func (c *nodesJoinGuideCmd) Usage() string {
	return "nodes join-guide [--addr <host:port>] [--token <tok>] [--worker-ip <ip>] [--manager-addr <host:port>] [--json]"
}

func (c *nodesJoinGuideCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.workerIP, "worker-ip", "", "worker public IP for precise firewall rule text")
	fs.StringVar(&c.managerAddr, "manager-addr", "", "manager reachable address override (advertise is private, worker is cross-WAN)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *nodesJoinGuideCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().GetJoinGuide(ctx, &serverv1.GetJoinGuideRequest{
			WorkerIp:    c.workerIP,
			ManagerAddr: c.managerAddr,
		})
		if err != nil {
			return err
		}
		g := resp.GetGuide()
		if c.jsonOut {
			type ruleView struct {
				Direction string `json:"direction"`
				Port      string `json:"port"`
				Purpose   string `json:"purpose"`
				Side      string `json:"side"`
				Rule      string `json:"rule"`
			}
			conv := func(rs []*serverv1.FirewallRule) []ruleView {
				out := make([]ruleView, 0, len(rs))
				for _, r := range rs {
					out = append(out, ruleView{r.GetDirection(), r.GetPort(), r.GetPurpose(), r.GetSide(), r.GetRule()})
				}
				return out
			}
			return writeJSON(env.Stdout, map[string]any{
				"join_command":  g.GetJoinCommand(),
				"manager_addr":  g.GetManagerAddr(),
				"worker_token":  g.GetWorkerToken(),
				"base_domain":   g.GetBaseDomain(),
				"manager_rules": conv(g.GetManagerFirewallRules()),
				"worker_rules":  conv(g.GetWorkerFirewallRules()),
				"preflight":     g.GetWorkerPreflightCommands(),
				"dns_steps":     g.GetDnsSteps(),
				"completion":    g.GetCompletionChecks(),
			})
		}
		var b strings.Builder
		fmt.Fprintf(&b, "join guide (base domain: %s)\n", g.GetBaseDomain())
		fmt.Fprintf(&b, "\n  1. run on the worker (preflight):\n")
		for _, s := range g.GetWorkerPreflightCommands() {
			fmt.Fprintf(&b, "       %s\n", s)
		}
		fmt.Fprintf(&b, "\n  2. join command (run on the worker):\n       %s\n", g.GetJoinCommand())
		b.WriteString("\n  firewall rules — generated text only, the platform never applies them:\n")
		fmt.Fprintf(&b, "    manager side:\n")
		for _, r := range g.GetManagerFirewallRules() {
			fmt.Fprintf(&b, "      %-12s %-10s %s\n", r.GetDirection(), r.GetPort(), r.GetRule())
		}
		fmt.Fprintf(&b, "    worker side:\n")
		for _, r := range g.GetWorkerFirewallRules() {
			fmt.Fprintf(&b, "      %-12s %-10s %s\n", r.GetDirection(), r.GetPort(), r.GetRule())
		}
		fmt.Fprintf(&b, "\n  3. DNS steps:\n")
		for _, s := range g.GetDnsSteps() {
			fmt.Fprintf(&b, "       - %s\n", s)
		}
		fmt.Fprintf(&b, "\n  4. completion checks (the wizard advances automatically):\n")
		for _, s := range g.GetCompletionChecks() {
			fmt.Fprintf(&b, "       - %s\n", s)
		}
		b.WriteString(haBoundaryText)
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// haBoundaryText 是 HA 边界诚实口径（multi-node §2.9：join 向导输出尾部，
// 架构 §2.6 口径的文本化——「2 台 ≠ 全面 HA」必须显式呈现）。
const haBoundaryText = `
  HA boundary (2 nodes — what you get and what you do NOT):
    get: stateless process-level HA (loss-of-contact ~13s, reschedule ~19s; the app is briefly unavailable during the window)
    get: nodes can be drained for maintenance with zero failed new connections (connection-level retry; in-flight connections may break once — remove DNS records first)
    get: control-plane failure does not affect running apps
    not: management-plane HA (1 manager; quorum=2 means any node loss takes management down — 3 managers required, not offered on 2 nodes)
    not: stateful HA (local volumes do not follow rescheduling; a DB on a lost node is unavailable until backup-restore + rebind)
    not: image-distribution HA (zot is pinned to the manager; new pulls fail while it is down — running apps unaffected)
    not: config-channel HA (each Traefik freezes its last good config while the manager is unreachable)
    not: health-driven failover or VIP redundancy at the ingress
`

// nodesRotateTokenCmd 实现 `fleetly nodes rotate-token`。
type nodesRotateTokenCmd struct {
	jsonOut bool
	conn    connFlags
	role    string
}

func (c *nodesRotateTokenCmd) Name() string { return "rotate-token" }
func (c *nodesRotateTokenCmd) Synopsis() string {
	return "rotate the swarm join token (old token stops working immediately)"
}
func (c *nodesRotateTokenCmd) Usage() string {
	return "nodes rotate-token [--addr <host:port>] [--token <tok>] [--role worker|manager] [--json]"
}

func (c *nodesRotateTokenCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.role, "role", "worker", "token role to rotate (worker|manager)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *nodesRotateTokenCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if c.role != "worker" && c.role != "manager" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--role must be worker or manager, got %q", c.role)}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().RotateJoinToken(ctx, &serverv1.RotateJoinTokenRequest{Role: c.role})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{
				"role": resp.GetRole(), "token": resp.GetToken(),
				"note": "the previous join token no longer works",
			})
		}
		_, err = fmt.Fprintf(env.Stdout, "%s join token rotated (the previous token no longer works):\n  %s\n",
			resp.GetRole(), resp.GetToken())
		return err
	})
}

// ── fleetly placement rebind / migrate ──────────────────────────────────────

// placementRebindCmd 实现 `fleetly placement rebind <app>`：显式换点
// （破坏性确认路径——强制 --confirm-destructive；有卷应用必须以
// --data-restored 或 --discard 声明数据处置，二者互斥）。
type placementRebindCmd struct {
	jsonOut    bool
	conn       connFlags
	node       string
	restored   bool
	discard    bool
	confirmDmg bool
}

func (c *placementRebindCmd) Name() string { return "rebind" }
func (c *placementRebindCmd) Synopsis() string {
	return "rebind an app to another node (destructive: requires --confirm-destructive and a data disposition)"
}
func (c *placementRebindCmd) Usage() string {
	return "placement rebind [--addr <host:port>] [--token <tok>] --node <name|platform-id> [--data-restored|--discard] --confirm-destructive [--json] <app>"
}

func (c *placementRebindCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.node, "node", "", "target node (display name or platform node ID)")
	fs.BoolVar(&c.restored, "data-restored", false, "declare the volume data restored on the target (restore flow completed)")
	fs.BoolVar(&c.discard, "discard", false, "declare the volume data on the source node discarded (becomes residual)")
	fs.BoolVar(&c.confirmDmg, "confirm-destructive", false, "confirm the destructive rebind (required)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *placementRebindCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	app := args[0]
	if c.node == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--node is required (display name or platform node ID)")}
	}
	if c.restored && c.discard {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--data-restored and --discard are mutually exclusive")}
	}
	if !c.confirmDmg {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf(
			"rebinding is a destructive, explicitly confirmed path: re-run with --confirm-destructive after reviewing the data disposition (--data-restored / --discard)")}
	}
	dataAck := ""
	if c.restored {
		dataAck = "restored"
	}
	if c.discard {
		dataAck = "discarded"
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Placement().UpdatePlacement(ctx, &serverv1.UpdatePlacementRequest{
			App: app, Node: c.node, DataAck: dataAck, Confirm: c.confirmDmg,
		})
		if err != nil {
			return err
		}
		// 回显：换点结果与卷残留指引（「显式换点」的操作回执面）。
		if c.jsonOut {
			type volView struct {
				Key      string `json:"key"`
				Name     string `json:"name"`
				Node     string `json:"platform_node_id,omitempty"`
				PrevNode string `json:"prev_platform_node_id,omitempty"`
				Residual bool   `json:"residual"`
				Status   string `json:"status"`
			}
			vols := make([]volView, 0, len(resp.GetVolumes()))
			for _, v := range resp.GetVolumes() {
				vols = append(vols, volView{v.GetKey(), v.GetName(), v.GetPlatformNodeId(),
					v.GetPrevPlatformNodeId(), v.GetResidual(), v.GetStatus()})
			}
			return writeJSON(env.Stdout, map[string]any{
				"app": app, "node": c.node, "data_ack": dataAck,
				"placement": resp.GetPlacement(), "volumes": vols,
				"next": "run fleetly deploy to converge; the rebind does not deploy",
			})
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s rebound to %s (data_ack=%s)\n", app, c.node, orUnspecified(dataAck))
		if p := resp.GetPlacement(); p != nil {
			fmt.Fprintf(&b, "  placement: node %s (state=%s)\n", p.GetPlatformNodeId(), p.GetState())
		}
		for _, v := range resp.GetVolumes() {
			line := fmt.Sprintf("  volume %s -> %s", v.GetName(), v.GetPlatformNodeId())
			if v.GetPrevPlatformNodeId() != "" {
				line += fmt.Sprintf(" (prev %s)", v.GetPrevPlatformNodeId())
			}
			if v.GetResidual() {
				line += " [residual on prev node: clean up with docker volume rm after verification]"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("  next: run fleetly deploy to converge (the rebind does not deploy)\n")
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// placementMigrateCmd 实现 `fleetly placement migrate <app> --to <node>`：
// restic 迁移 runbook（服务端生成步骤文档，D-MN-10 平台半）。
type placementMigrateCmd struct {
	jsonOut bool
	conn    connFlags
	to      string
}

func (c *placementMigrateCmd) Name() string { return "migrate" }
func (c *placementMigrateCmd) Synopsis() string {
	return "generate the restic migration runbook for moving an app to another node (read-only)"
}
func (c *placementMigrateCmd) Usage() string {
	return "placement migrate [--addr <host:port>] [--token <tok>] --to <name|platform-id> [--json] <app>"
}

func (c *placementMigrateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.to, "to", "", "target node (display name or platform node ID)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *placementMigrateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	if c.to == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--to is required (display name or platform node ID)")}
	}
	app := args[0]
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Placement().GetPlacementMigrationPlan(ctx, &serverv1.GetPlacementMigrationPlanRequest{
			App: app, To: c.to,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "migration runbook for app %s: %s -> %s\n", app,
			orUnspecified(resp.GetFromNode()), resp.GetToNode())
		for i, s := range resp.GetSteps() {
			fmt.Fprintf(&b, "\n  %d. %s\n", i+1, s.GetTitle())
			fmt.Fprintf(&b, "       %s\n", s.GetDetail())
		}
		for _, w := range resp.GetWarnings() {
			fmt.Fprintf(&b, "\n  WARNING: %s\n", w)
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// ── fleetly volumes ─────────────────────────────────────────────────────────

// volumesCmd 是外层动词 `volumes`：跨 app 卷清单（multi-node §2.8）。
type volumesCmd struct {
	sub *commands.App
}

func newVolumesCmd() *volumesCmd {
	sub := commands.New()
	sub.Register(&volumesListCmd{})
	sub.VerbTitle = "volumes subcommands:"
	return &volumesCmd{sub: sub}
}

func (c *volumesCmd) Name() string { return "volumes" }
func (c *volumesCmd) Synopsis() string {
	return "volume registry across apps (read-only; orphans and residual cleanup guidance)"
}
func (c *volumesCmd) Usage() string { return "volumes <list> [flags]" }

func (c *volumesCmd) SetFlags(_ *flag.FlagSet) {}

func (c *volumesCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// volumesListCmd 实现 `fleetly volumes list [--orphaned] [--residual]`：
// 卷清单 + 残留清理指引（平台只指引不代删——D18）。
type volumesListCmd struct {
	jsonOut  bool
	conn     connFlags
	orphaned bool
	residual bool
}

func (c *volumesListCmd) Name() string { return "list" }
func (c *volumesListCmd) Synopsis() string {
	return "list volumes across apps (orphans kept by default; residual rows point at source-node cleanup)"
}
func (c *volumesListCmd) Usage() string {
	return "volumes list [--addr <host:port>] [--token <tok>] [--orphaned] [--residual] [--json]"
}

func (c *volumesListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.orphaned, "orphaned", false, "only orphaned volumes (kept after app deletion)")
	fs.BoolVar(&c.residual, "residual", false, "only residual rows (data copied/moved off the source node; source copy awaits cleanup)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *volumesListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if c.orphaned && c.residual {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--orphaned and --residual are mutually exclusive filters")}
	}
	status := ""
	if c.orphaned {
		status = "orphaned"
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Placement().ListVolumes(ctx, &serverv1.ListVolumesRequest{
			Status: status, Residual: c.residual,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "volumes (%d rows)\n", len(resp.GetVolumes()))
		for _, v := range resp.GetVolumes() {
			fmt.Fprintf(&b, "  %s %s (%s, %s @%s", v.GetKey(), v.GetName(), v.GetKind(), v.GetStatus(), v.GetPlatformNodeId())
			if v.GetResidual() {
				fmt.Fprintf(&b, ")\n    residual: old copy on %s awaits cleanup (docker volume rm %s after verification; the platform never deletes data)",
					v.GetPrevPlatformNodeId(), v.GetName())
			}
			b.WriteString(")\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// orUnspecified 是空串的展示兜底（回执面可读性）。
func orUnspecified(s string) string {
	if s == "" {
		return "(unspecified)"
	}
	return s
}

// 编译期断言：新动词实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &nodesJoinGuideCmd{}
	_ commands.Flagged = &nodesJoinGuideCmd{}
	_ commands.Command = &nodesRotateTokenCmd{}
	_ commands.Flagged = &nodesRotateTokenCmd{}
	_ commands.Command = &placementRebindCmd{}
	_ commands.Flagged = &placementRebindCmd{}
	_ commands.Command = &placementMigrateCmd{}
	_ commands.Flagged = &placementMigrateCmd{}
	_ commands.Command = &volumesCmd{}
	_ commands.Flagged = &volumesCmd{}
	_ commands.Command = &volumesListCmd{}
	_ commands.Flagged = &volumesListCmd{}
)
