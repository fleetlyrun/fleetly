package cmd

// fleetly apps scaling 命令（W5-S1，B 线设计 §1，D-V3W5-2）：per app ×
// per compose service 的自动扩缩策略面。动词面：
//
//	apps scaling set  <app> <service> --min --max --cpu --mem --cooldown
//	apps scaling show <app> <service>
//	apps scaling rm   <app> <service>
//
// 生效前置（帮助文案同口径披露）：metrics.mode=on（`fleetly metrics mode
// set on`）且服务 running——metrics off 时策略休眠（scaling.dormant 事件
// 一次性披露）；有卷（stateful）服务只扩不缩（数据安全纪律）；无限额的
// 维度不可评估（诚实缺省——臆造分母即伪造利用率）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// scalingCmd 是 `apps` 下的中层动词 `scaling`：分发 set/show/rm。
type scalingCmd struct {
	sub *commands.App
}

func newScalingCmd() *scalingCmd {
	sub := commands.New()
	sub.Register(&scalingSetCmd{}, &scalingShowCmd{}, &scalingRemoveCmd{})
	sub.VerbTitle = "scaling subcommands:"
	return &scalingCmd{sub: sub}
}

func (c *scalingCmd) Name() string { return "scaling" }
func (c *scalingCmd) Synopsis() string {
	return "per-service autoscaling policies (requires metrics.mode=on to act)"
}
func (c *scalingCmd) Usage() string {
	return "apps scaling <set|show|rm> [flags] ..."
}

func (c *scalingCmd) SetFlags(_ *flag.FlagSet) {}

func (c *scalingCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (set|show|rm)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// scalingSetCmd 实现 `fleetly apps scaling set <app> <service>`。
type scalingSetCmd struct {
	min      int
	max      int
	cpu      int
	mem      int
	cooldown int
	jsonOut  bool
	conn     connFlags
}

func (c *scalingSetCmd) Name() string { return "set" }
func (c *scalingSetCmd) Synopsis() string {
	return "set the autoscaling policy of a service (whole-replacement upsert)"
}
func (c *scalingSetCmd) Usage() string {
	return "apps scaling set [--addr <host:port>] [--token <tok>] [--json] <app> <service> " +
		"--min 1 --max 4 --cpu 60 --mem 70 --cooldown 180"
}

func (c *scalingSetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.IntVar(&c.min, "min", 1, "minimum replicas (>=1)")
	fs.IntVar(&c.max, "max", 4, "maximum replicas (<=16)")
	fs.IntVar(&c.cpu, "cpu", 60, "CPU target utilization percent (20-90; 0 disables the CPU dimension)")
	fs.IntVar(&c.mem, "mem", 70, "memory target utilization percent (20-90; 0 disables the memory dimension)")
	fs.IntVar(&c.cooldown, "cooldown", 180, "cooldown window seconds between adjustments (60-3600)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *scalingSetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	if c.min < 1 {
		return fmt.Errorf("--min must be >= 1")
	}
	if c.max > 16 {
		return fmt.Errorf("--max must be <= 16 (swarm per-service ceiling)")
	}
	if c.max < c.min {
		return fmt.Errorf("--max must be >= --min")
	}
	if (c.cpu != 0 && (c.cpu < 20 || c.cpu > 90)) || (c.mem != 0 && (c.mem < 20 || c.mem > 90)) {
		return fmt.Errorf("--cpu/--mem targets must be within [20,90] (0 disables the dimension)")
	}
	if c.cpu == 0 && c.mem == 0 {
		return fmt.Errorf("at least one target is required: set --cpu or --mem (0 disables a dimension)")
	}
	if c.cooldown != 0 && (c.cooldown < 60 || c.cooldown > 3600) {
		return fmt.Errorf("--cooldown must be within [60,3600] seconds (0 = default 180)")
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().SetScalingPolicy(ctx, &serverv1.SetScalingPolicyRequest{
			Name:            c.conn.ref(args[0]),
			Service:         args[1],
			MinReplicas:     int32Clamp(c.min),
			MaxReplicas:     int32Clamp(c.max),
			TargetCpuPct:    int32Clamp(c.cpu),
			TargetMemPct:    int32Clamp(c.mem),
			CooldownSeconds: int32Clamp(c.cooldown),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		p := resp.GetPolicy()
		_, err = fmt.Fprintf(env.Stdout, `autoscaling policy set for %s / %s
  min/max replicas: %d/%d
  targets: cpu %d%%, mem %d%%
  cooldown: %ds
acts only while metrics.mode=on and the service is running (dormant otherwise);
services with volumes scale up only (never shrink)
`, p.GetName(), p.GetService(), p.GetMinReplicas(), p.GetMaxReplicas(),
			p.GetTargetCpuPct(), p.GetTargetMemPct(), p.GetCooldownSeconds())
		return err
	})
}

// scalingShowCmd 实现 `fleetly apps scaling show <app> <service>`。
type scalingShowCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *scalingShowCmd) Name() string { return "show" }
func (c *scalingShowCmd) Synopsis() string {
	return "show the autoscaling policy of a service"
}
func (c *scalingShowCmd) Usage() string {
	return "apps scaling show [--addr <host:port>] [--token <tok>] [--json] <app> <service>"
}

func (c *scalingShowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *scalingShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().GetScalingPolicy(ctx, &serverv1.GetScalingPolicyRequest{
			Name: c.conn.ref(args[0]), Service: args[1],
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app: %s\n", resp.GetName())
		fmt.Fprintf(&b, "  service: %s\n", resp.GetService())
		fmt.Fprintf(&b, "  min/max replicas: %d/%d\n", resp.GetMinReplicas(), resp.GetMaxReplicas())
		fmt.Fprintf(&b, "  targets: cpu %d%%, mem %d%%\n", resp.GetTargetCpuPct(), resp.GetTargetMemPct())
		fmt.Fprintf(&b, "  cooldown: %ds\n", resp.GetCooldownSeconds())
		fmt.Fprintf(&b, "  updated: %s\n", tstampRFC3339(resp.GetUpdatedAt()))
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// scalingRemoveCmd 实现 `fleetly apps scaling rm <app> <service>`。
type scalingRemoveCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *scalingRemoveCmd) Name() string { return "rm" }
func (c *scalingRemoveCmd) Synopsis() string {
	return "remove the autoscaling policy of a service"
}
func (c *scalingRemoveCmd) Usage() string {
	return "apps scaling rm [--addr <host:port>] [--token <tok>] [--json] <app> <service>"
}

func (c *scalingRemoveCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *scalingRemoveCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Apps().RemoveScalingPolicy(ctx, &serverv1.RemoveScalingPolicyRequest{
			Name: c.conn.ref(args[0]), Service: args[1],
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		_, err = fmt.Fprintf(env.Stdout, "autoscaling policy removed for %s / %s\n", resp.GetName(), resp.GetService())
		return err
	})
}

// 编译期断言：scaling 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &scalingCmd{}
	_ commands.Flagged = &scalingCmd{}
	_ commands.Command = &scalingSetCmd{}
	_ commands.Flagged = &scalingSetCmd{}
	_ commands.Command = &scalingShowCmd{}
	_ commands.Flagged = &scalingShowCmd{}
	_ commands.Command = &scalingRemoveCmd{}
	_ commands.Flagged = &scalingRemoveCmd{}
)
