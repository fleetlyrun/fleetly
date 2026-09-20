package main

// env / placement / nodes 命令（T2.18 CLI-over-SDK 改造）：经 SDK 消费平
// 台（EnvService/PlacementService/SystemService.ListNodes），不再直开状态
// 库与主密钥——env 明文的加密边界在 daemon（EnvService.GetEnv 为 admin
// scope 显式路径）；nodes 是观测缓存的只读列表（state-model §2.2：禁止
// 用于决策，展示/诊断专用）。
//
// env 生效语义（architecture §2.4 变量合并行）：set 创建 pending 变更、
// 随下次部署生效（发布引擎消费）；list 输出键名与状态位、值恒脱敏，读值
// 走 env get 显式路径。
//
// 动词形态（lynx-go/commands 嵌套子命令）：env set/get/list/rm、
// placement show、nodes list——外层动词经 subDispatchUsage 收口复用内层
// App 的分发机器（README「嵌套子动词」模式；嵌套 miss 保 64，见 app.go）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/placement"
)

// ── fleetly env ─────────────────────────────────────────────────────────────

// envCmd 是外层动词 `env`：分发 set/get/list/rm。
type envCmd struct {
	sub *commands.App
}

func newEnvCmd() *envCmd {
	sub := commands.New()
	sub.Register(&envSetCmd{}, &envGetCmd{}, &envListCmd{}, &envRmCmd{})
	sub.VerbTitle = "env subcommands:"
	return &envCmd{sub: sub}
}

func (c *envCmd) Name() string     { return "env" }
func (c *envCmd) Synopsis() string { return "platform env vars (three-layer merge: platform layer)" }
func (c *envCmd) Usage() string    { return "env <set|get|list|rm> [flags] <app> ..." }

func (c *envCmd) SetFlags(_ *flag.FlagSet) {}

func (c *envCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (set|get|list|rm)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// envSetCmd 实现 `fleetly env set <app> <key> <value>`：明文经 RPC 上行
// （TLS 前提 = v0.1 明文回环），服务端 envelope 加密落库（value 列只存
// 密文），状态位 = pending（随下次部署生效）。
type envSetCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *envSetCmd) Name() string { return "set" }
func (c *envSetCmd) Synopsis() string {
	return "set a platform env var (pending until next deploy)"
}
func (c *envSetCmd) Usage() string {
	return "env set [--addr <host:port>] [--token <tok>] [--json] <app> <key> <value>"
}

func (c *envSetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envSetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 3); err != nil {
		return err
	}
	appName, key, value := args[0], args[1], args[2]
	return c.conn.withClient(func(cl *fleetlyClient) error {
		if _, err := cl.Env().SetEnv(ctx, &serverv1.SetEnvRequest{App: appName, Key: key, Value: value}); err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{
				"app": appName, "key": key, "status": "pending", "effective_on": "next deploy",
			})
		}
		_, err := fmt.Fprintf(env.Stdout, "%s %s = *** (pending, effective on next deploy)\n", appName, key)
		return err
	})
}

// envGetCmd 实现 `fleetly env get <app> <key>`：显式读回明文（admin scope
// 的显式路径；列表输出不落值）。
type envGetCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *envGetCmd) Name() string { return "get" }
func (c *envGetCmd) Synopsis() string {
	return "read a platform env var (plaintext; admin scope)"
}
func (c *envGetCmd) Usage() string {
	return "env get [--addr <host:port>] [--token <tok>] [--json] <app> <key>"
}

func (c *envGetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envGetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Env().GetEnv(ctx, &serverv1.GetEnvRequest{App: args[0], Key: args[1]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{
				"app": args[0], "key": resp.GetKey(), "value": resp.GetValue(), "status": resp.GetStatus(),
			})
		}
		_, err = fmt.Fprintf(env.Stdout, "%s\n", resp.GetValue())
		return err
	})
}

// envListCmd 实现 `fleetly env list <app>`：键名 + 来源 + 状态位；值恒
// 脱敏（读值走 env get 显式路径）。
type envListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *envListCmd) Name() string { return "list" }
func (c *envListCmd) Synopsis() string {
	return "list platform env vars (keys only; values masked)"
}
func (c *envListCmd) Usage() string {
	return "env list [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *envListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Env().ListEnv(ctx, &serverv1.ListEnvRequest{App: args[0]})
		if err != nil {
			return err
		}
		type item struct {
			Key    string `json:"key"`
			Source string `json:"source"`
			Status string `json:"status"`
			Value  string `json:"value"`
		}
		items := make([]item, 0, len(resp.GetEnvVars()))
		for _, r := range resp.GetEnvVars() {
			items = append(items, item{Key: r.GetKey(), Source: r.GetSource(), Status: r.GetStatus(), Value: "********"})
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"app": args[0], "vars": items})
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s: %d platform env vars\n", args[0], len(items))
		for _, it := range items {
			fmt.Fprintf(&b, "  %s = ******** (%s, %s)\n", it.Key, it.Source, it.Status)
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// envRmCmd 实现 `fleetly env rm <app> <key>`（生效语义：当前运行 env 不变，
// 下次部署不再注入——「省略 = 删除」的期望态治理规则）。
type envRmCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *envRmCmd) Name() string { return "rm" }
func (c *envRmCmd) Synopsis() string {
	return "remove a platform env var (takes effect on next deploy)"
}
func (c *envRmCmd) Usage() string {
	return "env rm [--addr <host:port>] [--token <tok>] [--json] <app> <key>"
}

func (c *envRmCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *envRmCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		if _, err := cl.Env().RemoveEnv(ctx, &serverv1.RemoveEnvRequest{App: args[0], Key: args[1]}); err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{
				"app": args[0], "key": args[1], "removed": true, "effective_on": "next deploy",
			})
		}
		_, err := fmt.Fprintf(env.Stdout, "%s %s removed (current running env unchanged; next deploy no longer injects it)\n", args[0], args[1])
		return err
	})
}

// ── fleetly placement ───────────────────────────────────────────────────────

// placementCmd 是外层动词 `placement`：分发 show（rebind 属 v0.2，不加）。
type placementCmd struct {
	sub *commands.App
}

func newPlacementCmd() *placementCmd {
	sub := commands.New()
	sub.Register(&placementShowCmd{})
	sub.VerbTitle = "placement subcommands:"
	return &placementCmd{sub: sub}
}

func (c *placementCmd) Name() string     { return "placement" }
func (c *placementCmd) Synopsis() string { return "app placement bindings (single-node view)" }
func (c *placementCmd) Usage() string    { return "placement <show> [flags] <app>" }

func (c *placementCmd) SetFlags(_ *flag.FlagSet) {}

func (c *placementCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (show)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// placementShowCmd 实现 `fleetly placement show <app>`：绑定记录 + 卷注册
// 表 + 有卷约束字符串（运行位置卡片的数据面，stateful-placement §2.3）。
type placementShowCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *placementShowCmd) Name() string { return "show" }
func (c *placementShowCmd) Synopsis() string {
	return "show app placement binding, volumes and constraint"
}
func (c *placementShowCmd) Usage() string {
	return "placement show [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *placementShowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *placementShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Placement().ShowPlacement(ctx, &serverv1.ShowPlacementRequest{App: args[0]})
		if err != nil {
			return err
		}
		p := resp.GetPlacement()
		type volumeView struct {
			Key        string `json:"key"`
			Name       string `json:"name"`
			Kind       string `json:"kind"`
			PlatformID string `json:"platform_node_id,omitempty"`
			MountPath  string `json:"mount_path,omitempty"`
			Status     string `json:"status"`
		}
		out := struct {
			App            string       `json:"app"`
			Bound          bool         `json:"bound"`
			PlatformNodeID string       `json:"platform_node_id,omitempty"`
			State          string       `json:"state,omitempty"`
			Source         string       `json:"source,omitempty"`
			LabelRef       string       `json:"label_ref,omitempty"`
			Reason         string       `json:"reason,omitempty"`
			PinnedAt       string       `json:"pinned_at,omitempty"`
			Constraint     string       `json:"constraint,omitempty"`
			Volumes        []volumeView `json:"volumes"`
		}{App: args[0], Volumes: []volumeView{}}
		if p != nil {
			out.Bound = p.GetState() == "bound"
			out.PlatformNodeID = p.GetPlatformNodeId()
			out.State = p.GetState()
			out.Source = p.GetSource()
			out.LabelRef = p.GetLabelRef()
			out.Reason = p.GetReason()
			if t := p.GetPinnedAt(); t != nil {
				out.PinnedAt = t.AsTime().Format("2006-01-02T15:04:05Z07:00")
			}
			// 有卷 + 已绑定 → 约束编译结果（执行层随部署下发）。
			if len(resp.GetVolumes()) > 0 && out.PlatformNodeID != "" {
				out.Constraint = placement.ConstraintFor(out.PlatformNodeID)
			}
		}
		for _, v := range resp.GetVolumes() {
			out.Volumes = append(out.Volumes, volumeView{
				Key: v.GetKey(), Name: v.GetName(), Kind: v.GetKind(),
				PlatformID: v.GetPlatformNodeId(), MountPath: v.GetMountPath(), Status: v.GetStatus(),
			})
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, out)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s\n", args[0])
		if p != nil {
			fmt.Fprintf(&b, "  placement: node %s (state=%s source=%s", out.PlatformNodeID, out.State, out.Source)
			if out.LabelRef != "" {
				fmt.Fprintf(&b, " label=%s", out.LabelRef)
			}
			if out.PinnedAt != "" {
				fmt.Fprintf(&b, " pinned_at=%s", out.PinnedAt)
			}
			b.WriteString(")\n")
		} else {
			b.WriteString("  placement: unbound (stateless apps are free to schedule)\n")
		}
		if out.Constraint != "" {
			fmt.Fprintf(&b, "  constraint: %s\n", out.Constraint)
		}
		if len(out.Volumes) > 0 {
			b.WriteString("  volumes:\n")
			for _, v := range out.Volumes {
				fmt.Fprintf(&b, "    %s %s (%s, %s", v.Key, v.Name, v.Kind, v.Status)
				if v.PlatformID != "" {
					fmt.Fprintf(&b, " @%s", v.PlatformID)
				}
				if v.MountPath != "" {
					fmt.Fprintf(&b, " -> %s", v.MountPath)
				}
				b.WriteString(")\n")
			}
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// ── fleetly nodes ───────────────────────────────────────────────────────────

// nodesCmd 是外层动词 `nodes`：分发 list（只读观测面）。
type nodesCmd struct {
	sub *commands.App
}

func newNodesCmd() *nodesCmd {
	sub := commands.New()
	sub.Register(&nodesListCmd{})
	sub.VerbTitle = "nodes subcommands:"
	return &nodesCmd{sub: sub}
}

func (c *nodesCmd) Name() string     { return "nodes" }
func (c *nodesCmd) Synopsis() string { return "cluster nodes (read-only observation cache)" }
func (c *nodesCmd) Usage() string    { return "nodes <list> [flags]" }

func (c *nodesCmd) SetFlags(_ *flag.FlagSet) {}

func (c *nodesCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// nodesListCmd 实现 `fleetly nodes list`：观测缓存只读列表（展示/诊断专用，
// state-model §2.2 禁止用于决策；节点变更用 docker node 原生命令）。
type nodesListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *nodesListCmd) Name() string { return "list" }
func (c *nodesListCmd) Synopsis() string {
	return "list cluster nodes (observation cache; not for decisions)"
}
func (c *nodesListCmd) Usage() string {
	return "nodes list [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *nodesListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *nodesListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().ListNodes(ctx, &serverv1.ListNodesRequest{})
		if err != nil {
			return err
		}
		type nodeView struct {
			SwarmNodeID  string            `json:"swarm_node_id"`
			PlatformID   string            `json:"platform_id,omitempty"`
			Hostname     string            `json:"hostname"`
			State        string            `json:"state"`
			Availability string            `json:"availability"`
			IsManager    bool              `json:"is_manager"`
			ObservedAt   string            `json:"observed_at"`
			Stale        bool              `json:"stale"`
			Labels       map[string]string `json:"labels,omitempty"`
		}
		views := make([]nodeView, 0, len(resp.GetNodes()))
		for _, n := range resp.GetNodes() {
			v := nodeView{
				SwarmNodeID:  n.GetSwarmNodeId(),
				PlatformID:   n.GetPlatformId(),
				Hostname:     n.GetHostname(),
				State:        n.GetState(),
				Availability: n.GetAvailability(),
				IsManager:    n.GetIsManager(),
				ObservedAt:   tstampRFC3339(n.GetObservedAt()),
				Stale:        n.GetStale(),
				Labels:       n.GetLabels(),
			}
			views = append(views, v)
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"nodes": views, "cache": true})
		}
		var b strings.Builder
		b.WriteString("cluster nodes (observation cache; read-only — manage nodes with `docker node`)\n")
		if len(views) == 0 {
			b.WriteString("  (cache empty — fleetlyd observer has not synced yet)\n")
		}
		for _, v := range views {
			id := v.PlatformID
			if id == "" {
				id = "-"
			}
			fmt.Fprintf(&b, "  %s %s state=%s availability=%s manager=%t observed_at=%s",
				id, v.Hostname, v.State, v.Availability, v.IsManager, v.ObservedAt)
			if v.Stale {
				b.WriteString(" STALE")
			}
			b.WriteString("\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// tstampRFC3339 是 proto Timestamp → RFC3339 文本（nil = 未发生，出空串）。
func tstampRFC3339(t *timestampProto) string {
	if t == nil {
		return ""
	}
	return t.AsTime().Format("2006-01-02T15:04:05Z07:00")
}

// 编译期断言：嵌套动词与子命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &envCmd{}
	_ commands.Flagged = &envCmd{}
	_ commands.Command = &placementCmd{}
	_ commands.Flagged = &placementCmd{}
	_ commands.Command = &nodesCmd{}
	_ commands.Flagged = &nodesCmd{}
	_ commands.Command = &envSetCmd{}
	_ commands.Flagged = &envSetCmd{}
	_ commands.Command = &envGetCmd{}
	_ commands.Flagged = &envGetCmd{}
	_ commands.Command = &envListCmd{}
	_ commands.Flagged = &envListCmd{}
	_ commands.Command = &envRmCmd{}
	_ commands.Flagged = &envRmCmd{}
	_ commands.Command = &placementShowCmd{}
	_ commands.Flagged = &placementShowCmd{}
	_ commands.Command = &nodesListCmd{}
	_ commands.Flagged = &nodesListCmd{}
)
