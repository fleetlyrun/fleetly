package main

// fleetly tokens 命令（T2.18 新增动词——RPC 面自 T2.17 起已有，CLI 补齐；
// admin scope 专用）：create / list / revoke。明文 token 仅 create 响应一
// 次可见（服务端只存 sha256 哈希，丢失只能 revoke 后重建）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// tokensCmd 是外层动词 `tokens`：分发 create/list/revoke。
type tokensCmd struct {
	sub *commands.App
}

func newTokensCmd() *tokensCmd {
	sub := commands.New()
	sub.Register(&tokensCreateCmd{}, &tokensListCmd{}, &tokensRevokeCmd{})
	sub.VerbTitle = "tokens subcommands:"
	return &tokensCmd{sub: sub}
}

func (c *tokensCmd) Name() string     { return "tokens" }
func (c *tokensCmd) Synopsis() string { return "API token management (admin scope)" }
func (c *tokensCmd) Usage() string    { return "tokens <create|list|revoke> [flags] ..." }

func (c *tokensCmd) SetFlags(_ *flag.FlagSet) {}

func (c *tokensCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (create|list|revoke)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// tokensCreateCmd 实现 `fleetly tokens create --scopes <s1,s2> [--note]`。
type tokensCreateCmd struct {
	scopes  string
	note    string
	jsonOut bool
	conn    connFlags
}

func (c *tokensCreateCmd) Name() string { return "create" }
func (c *tokensCreateCmd) Synopsis() string {
	return "create an API token (plaintext shown once)"
}
func (c *tokensCreateCmd) Usage() string {
	return "tokens create [--addr <host:port>] [--token <tok>] --scopes <read|deploy|admin[,…]> [--note <text>] [--json]"
}

func (c *tokensCreateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.scopes, "scopes", "read", "comma-separated scopes (read/deploy/admin; admin implies deploy implies read)")
	fs.StringVar(&c.note, "note", "", "human-readable note (e.g. \"CI deploy\")")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *tokensCreateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	scopes := strings.Split(c.scopes, ",")
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Tokens().CreateToken(ctx, &serverv1.CreateTokenRequest{Scopes: scopes, Note: c.note})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "token created: %s（仅此一次显示，请妥善保存——服务端只存哈希）\n", resp.GetToken())
		fmt.Fprintf(&b, "  id: %s\n  scopes: %s\n", resp.GetId(), strings.Join(resp.GetScopes(), ","))
		if resp.GetNote() != "" {
			fmt.Fprintf(&b, "  note: %s\n", resp.GetNote())
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// tokensListCmd 实现 `fleetly tokens list`（无敏感投影：备注/scope/哈希
// 前缀——明文与完整哈希永不回读）。
type tokensListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *tokensListCmd) Name() string { return "list" }
func (c *tokensListCmd) Synopsis() string {
	return "list API tokens (no sensitive projection)"
}
func (c *tokensListCmd) Usage() string {
	return "tokens list [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *tokensListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *tokensListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Tokens().ListTokens(ctx, &serverv1.ListTokensRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if len(resp.GetTokens()) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no tokens（fleetly tokens create 签发）")
			return err
		}
		var b strings.Builder
		for _, t := range resp.GetTokens() {
			line := fmt.Sprintf("%s  %-12s scopes=%s", t.GetId(), t.GetHashPrefix(), strings.Join(t.GetScopes(), ","))
			if t.GetNote() != "" {
				line += "  note=" + t.GetNote()
			}
			if t.GetRevokedAt() != nil {
				line += "  REVOKED"
			}
			b.WriteString(line + "\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// tokensRevokeCmd 实现 `fleetly tokens revoke <id>`（幂等；不存在 404）。
type tokensRevokeCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *tokensRevokeCmd) Name() string { return "revoke" }
func (c *tokensRevokeCmd) Synopsis() string {
	return "revoke an API token (idempotent)"
}
func (c *tokensRevokeCmd) Usage() string {
	return "tokens revoke [--addr <host:port>] [--token <tok>] [--json] <id>"
}

func (c *tokensRevokeCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *tokensRevokeCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Tokens().RevokeToken(ctx, &serverv1.RevokeTokenRequest{Id: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"id": resp.GetId(), "revoked": true})
		}
		_, err = fmt.Fprintf(env.Stdout, "token %s revoked\n", resp.GetId())
		return err
	})
}

// 编译期断言：tokens 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &tokensCmd{}
	_ commands.Flagged = &tokensCmd{}
	_ commands.Command = &tokensCreateCmd{}
	_ commands.Flagged = &tokensCreateCmd{}
	_ commands.Command = &tokensListCmd{}
	_ commands.Flagged = &tokensListCmd{}
	_ commands.Command = &tokensRevokeCmd{}
	_ commands.Flagged = &tokensRevokeCmd{}
)
