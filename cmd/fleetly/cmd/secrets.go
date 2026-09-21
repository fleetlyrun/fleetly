package cmd

// 平台密钥库命令（E4 managed-databases §2.7，W4-S4）：`fleetly secrets
// <set|list|rm>`。全部经 RPC（CLI-over-SDK 纪律）；无值读回面（D-DB-7：
// 忘记即轮换——list 只出名称/指纹/时间锚，set/rm 的回执只有 hash8 指纹）。
// 值经 --value 传入（env set 无 stdin 先例——口径一致取 flag-only；进程
// 参数会进 shell 历史，runbook 提示优先用一次性会话）。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// secretsCmd 是外层动词 `secrets`：分发密钥库子命令。
type secretsCmd struct {
	sub *commands.App
}

func newSecretsCmd() *secretsCmd {
	sub := commands.New()
	sub.Register(&secretSetCmd{}, &secretListCmd{}, &secretRemoveCmd{})
	sub.VerbTitle = "secrets subcommands:"
	return &secretsCmd{sub: sub}
}

func (c *secretsCmd) Name() string { return "secrets" }
func (c *secretsCmd) Synopsis() string {
	return "manage app secrets in the platform secret store (source for compose external secrets; values are write-only — never read back)"
}
func (c *secretsCmd) Usage() string {
	return "secrets <set|list|rm> [flags] ..."
}

func (c *secretsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *secretsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (set|list|rm)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// secretSetCmd 实现 `fleetly secrets set <app> <name> --value <v>`。
type secretSetCmd struct {
	value string
	conn  connFlags
}

func (c *secretSetCmd) Name() string { return "set" }
func (c *secretSetCmd) Synopsis() string {
	return "set (or rotate) an app secret: the value is encrypted server-side and never returned (rotation = set again; referencing apps pick it up on their next deploy)"
}
func (c *secretSetCmd) Usage() string {
	return "secrets set --value <value> [--addr <host:port>] [--token <tok>] <app> <name>"
}

func (c *secretSetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.value, "value", "", "secret value (write-only; never echoed back)")
}

func (c *secretSetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	if c.value == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing --value (secret values are passed via flag; avoid interactive shells for sensitive values)")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Secrets().SetSecret(ctx, &serverv1.SetSecretRequest{
			App:   args[0],
			Name:  args[1],
			Value: c.value,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "secret %s/%s set (fingerprint %s); referencing apps pick up the value on their next deploy\n",
			resp.GetApp(), resp.GetName(), resp.GetHash8())
		return err
	})
}

// secretListCmd 实现 `fleetly secrets list <app>`。
type secretListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *secretListCmd) Name() string { return "list" }
func (c *secretListCmd) Synopsis() string {
	return "list an app's secrets (names and fingerprints only — values are never readable; forgot one? set it again)"
}
func (c *secretListCmd) Usage() string {
	return "secrets list [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *secretListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *secretListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Secrets().ListSecrets(ctx, &serverv1.ListSecretsRequest{App: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, resp)
		}
		if len(resp.GetSecrets()) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no secrets (set one with: fleetly secrets set <app> <name> --value <value>)")
			return err
		}
		for _, s := range resp.GetSecrets() {
			line := fmt.Sprintf("%s  %s", s.GetName(), s.GetHash8())
			if ts := s.GetUpdatedAt(); ts != nil {
				line += "  " + tstampRFC3339(ts)
			}
			if _, err := fmt.Fprintln(env.Stdout, line); err != nil {
				return err
			}
		}
		return nil
	})
}

// secretRemoveCmd 实现 `fleetly secrets rm <app> <name>`。
type secretRemoveCmd struct {
	conn connFlags
}

func (c *secretRemoveCmd) Name() string { return "rm" }
func (c *secretRemoveCmd) Synopsis() string {
	return "remove an app secret (services still declaring it fail their next deploy with E_SECRET_NOT_FOUND — remove the declaration too)"
}
func (c *secretRemoveCmd) Usage() string {
	return "secrets rm [--addr <host:port>] [--token <tok>] <app> <name>"
}

func (c *secretRemoveCmd) SetFlags(fs *flag.FlagSet) { c.conn.register(fs) }

func (c *secretRemoveCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Secrets().RemoveSecret(ctx, &serverv1.RemoveSecretRequest{
			App:  args[0],
			Name: args[1],
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "secret %s/%s removed\n", resp.GetApp(), resp.GetName())
		return err
	})
}

// 编译期断言：secrets 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &secretsCmd{}
	_ commands.Flagged = &secretsCmd{}
)
