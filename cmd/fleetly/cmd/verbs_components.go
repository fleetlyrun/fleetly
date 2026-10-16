package cmd

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// components 组（IA v3 二期④）：受管组件载体的运维动词（排障动线
// "日志断了 → 就地重启"；Managed Providers 页同链路）。
func newComponentsRestartVerb() commands.Command {
	const name = "restart"
	return &flaggedVerb{
		name:     name,
		synopsis: "Restart a managed component carrier (force-reschedule; e.g. traefik / zot / victorialogs / victoriametrics)",
		usage:    "components restart NAME",
		setFlags: func(fs *flag.FlagSet) {},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.System.RestartComponent(ctx, &systemv1.RestartComponentRequest{Name: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout, "restarted %s (%s carriers rescheduled)\n", args[0], strconv.Itoa(int(resp.GetRestarted())))
				})
			})
		},
	}
}
