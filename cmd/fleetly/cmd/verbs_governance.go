package cmd

// Governance 动词组（F1.9，ADR-0017 附录 A.3）：Change Freeze 生命周期。
// `freeze set --reason`（--team 限定 / --all 全局）→ 变更型动词拒带原因 →
// `freeze lift` 恢复；`freeze list` 含历史。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
)

func newFreezeSetVerb() commands.Command {
	const name = "set"
	var team, reason string
	var all bool
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Set a change freeze (change verbs are refused with the reason until lifted)",
		usage:    "freeze set --reason TEXT [--team TEAM_ID | --all]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "", "limit the freeze to one team (default with neither flag)")
			fs.BoolVar(&all, "all", false, "freeze every team (global row)")
			fs.StringVar(&reason, "reason", "", "why changes are frozen (required; refused calls see it)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr(name, "unexpected argument(s)")
			}
			if reason == "" {
				return usageErr(name, "--reason is required (refused calls see it)")
			}
			if all && team != "" {
				return usageErr(name, "--all and --team are mutually exclusive")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Governance.SetChangeFreeze(ctx, &systemv1.SetChangeFreezeRequest{TeamId: team, Reason: reason})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetFreeze(), func() {
				scope := "all teams"
				if resp.GetFreeze().GetTeamId() != "" {
					scope = "team " + resp.GetFreeze().GetTeamId()
				}
				_, _ = fmt.Fprintf(env.Stdout, "froze changes for %s (id %s): %s\n",
					scope, resp.GetFreeze().GetId(), resp.GetFreeze().GetReason())
			})
		},
	}
}

func newFreezeLiftVerb() commands.Command {
	const name = "lift"
	return &flaggedVerb{
		name:     name,
		synopsis: "Lift a change freeze (change verbs are accepted again)",
		usage:    "freeze lift FREEZE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one FREEZE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Governance.LiftChangeFreeze(ctx, &systemv1.LiftChangeFreezeRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetFreeze(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "lifted freeze %s (reason was: %s)\n",
					resp.GetFreeze().GetId(), resp.GetFreeze().GetReason())
			})
		},
	}
}

func newFreezeListVerb() commands.Command {
	var after string
	var limit int
	return &flaggedVerb{
		name:     "list",
		synopsis: "List change freezes (active and historical)",
		usage:    "freeze list [--after FREEZE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&after, "after", "", "pagination cursor: the last freeze id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr("list", "unexpected argument(s)")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Governance.ListChangeFreezes(ctx, &systemv1.ListChangeFreezesRequest{
				AfterFreezeId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSCOPE\tSTATE\tREASON")
				for _, f := range resp.GetFreezes() {
					scope := "all teams"
					if f.GetTeamId() != "" {
						scope = f.GetTeamId()
					}
					state := "lifted"
					if f.GetLiftedAt() == "" {
						state = "active"
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\n", f.GetId(), scope, state, f.GetReason())
				}
			})
		},
	}
}
