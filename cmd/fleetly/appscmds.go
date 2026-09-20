package main

// fleetly apps 命令（T2.18 新增动词——RPC 面自 T2.17 起已有，CLI 补齐）：
//
//	list   —— 应用列表（生命周期位 + 派生状态：running/degraded/blocked/
//	          down，读面即时推导）；
//	get    —— 应用详情（placement + 最近部署 + 派生状态）；
//	delete —— 推进 active → deleting（tombstone 第一拍；admin scope，
//	          保留期内名字不释放）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// appsCmd 是外层动词 `apps`：分发 list/get/delete/webhook。
type appsCmd struct {
	sub *commands.App
}

func newAppsCmd() *appsCmd {
	sub := commands.New()
	sub.Register(&appsListCmd{}, &appsGetCmd{}, &appsDeleteCmd{}, newWebhookCmd())
	sub.VerbTitle = "apps subcommands:"
	return &appsCmd{sub: sub}
}

func (c *appsCmd) Name() string     { return "apps" }
func (c *appsCmd) Synopsis() string { return "app resources (list, details, delete, webhook)" }
func (c *appsCmd) Usage() string    { return "apps <list|get|delete|webhook> [flags] ..." }

func (c *appsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *appsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|get|delete|webhook)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// appsListCmd 实现 `fleetly apps list`。
type appsListCmd struct {
	jsonOut bool
	limit   int
	conn    connFlags
}

func (c *appsListCmd) Name() string { return "list" }
func (c *appsListCmd) Synopsis() string {
	return "list apps (lifecycle + derived state)"
}
func (c *appsListCmd) Usage() string {
	return "apps list [--addr <host:port>] [--token <tok>] [--limit N] [--json]"
}

func (c *appsListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.IntVar(&c.limit, "limit", 100, "max rows")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *appsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().ListApps(ctx, &serverv1.ListAppsRequest{Limit: int32Clamp(c.limit)})
		if err != nil {
			return err
		}
		type appRow struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Lifecycle    string `json:"lifecycle"`
			DerivedState string `json:"derived_state"`
			CreatedAt    string `json:"created_at,omitempty"`
			UpdatedAt    string `json:"updated_at,omitempty"`
		}
		rows := make([]appRow, 0, len(resp.GetApps()))
		for _, a := range resp.GetApps() {
			rows = append(rows, appRow{
				ID: a.GetId(), Name: a.GetName(), Lifecycle: a.GetLifecycle(),
				DerivedState: a.GetDerivedState(),
				CreatedAt:    tstampRFC3339(a.GetCreatedAt()), UpdatedAt: tstampRFC3339(a.GetUpdatedAt()),
			})
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"apps": rows})
		}
		if len(rows) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no apps（fleetly deploy <compose> 发起首次部署）")
			return err
		}
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "%s  %-8s %-8s %s\n", r.Name, r.Lifecycle, r.DerivedState, r.ID)
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// appsGetCmd 实现 `fleetly apps get <name>`。
type appsGetCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *appsGetCmd) Name() string { return "get" }
func (c *appsGetCmd) Synopsis() string {
	return "show app details (placement, recent deployments, derived state)"
}
func (c *appsGetCmd) Usage() string {
	return "apps get [--addr <host:port>] [--token <tok>] [--json] <name>"
}

func (c *appsGetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *appsGetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().GetApp(ctx, &serverv1.GetAppRequest{Name: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s\n  lifecycle: %s\n  derived_state: %s\n  id: %s\n",
			resp.GetName(), resp.GetLifecycle(), resp.GetDerivedState(), resp.GetId())
		if p := resp.GetPlacement(); p != nil {
			fmt.Fprintf(&b, "  placement: node %s (state=%s source=%s)\n",
				p.GetPlatformNodeId(), p.GetState(), p.GetSource())
		} else {
			b.WriteString("  placement: unbound\n")
		}
		for _, d := range resp.GetRecentDeployments() {
			fmt.Fprintf(&b, "  deployment %s  %-9s %s\n", d.GetId(), d.GetStatus(), d.GetKind())
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// appsDeleteCmd 实现 `fleetly apps delete <name>`（admin scope；tombstone
// 第一拍——保留期内名字不释放、不复活）。
type appsDeleteCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *appsDeleteCmd) Name() string { return "delete" }
func (c *appsDeleteCmd) Synopsis() string {
	return "delete an app (tombstone: active → deleting; names stay reserved during retention)"
}
func (c *appsDeleteCmd) Usage() string {
	return "apps delete [--addr <host:port>] [--token <tok>] [--json] <name>"
}

func (c *appsDeleteCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *appsDeleteCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().DeleteApp(ctx, &serverv1.DeleteAppRequest{Name: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"app": resp.GetName(), "lifecycle": resp.GetLifecycle()})
		}
		_, err = fmt.Fprintf(env.Stdout, "%s → %s（保留期内名字不释放；恢复 = 重新部署同名应用）\n", resp.GetName(), resp.GetLifecycle())
		return err
	})
}

// 编译期断言：apps 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &appsCmd{}
	_ commands.Flagged = &appsCmd{}
	_ commands.Command = &appsListCmd{}
	_ commands.Flagged = &appsListCmd{}
	_ commands.Command = &appsGetCmd{}
	_ commands.Flagged = &appsGetCmd{}
	_ commands.Command = &appsDeleteCmd{}
	_ commands.Flagged = &appsDeleteCmd{}
)
