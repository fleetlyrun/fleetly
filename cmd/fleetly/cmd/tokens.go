package cmd

// fleetly tokens 命令（T2.18 新增动词——RPC 面自 T2.17 起已有，CLI 补齐；
// v0.3 W2 语义迁移：用户自服务面——登录用户管自己的 PAT，声明 scopes 不
// 得超出角色可达集；平台管理员经 --machine 显式创建平台级机具令牌）：
// create / list / revoke。明文 token 仅 create 响应一次可见（服务端只存
// sha256 哈希，丢失只能 revoke 后重建）。

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
func (c *tokensCmd) Synopsis() string { return "manage personal access tokens (PATs; platform admins can also mint machine tokens)" }
func (c *tokensCmd) Usage() string    { return "tokens <create|list|revoke> [flags] ..." }

func (c *tokensCmd) SetFlags(_ *flag.FlagSet) {}

func (c *tokensCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (create|list|revoke)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// tokensCreateCmd 实现 `fleetly tokens create --scopes <s1,s2> [--note]
// [--project <id>] [--machine]`：登录凭据（用户 PAT）造自己的 PAT——声明
// scopes ⊆ 用户可达集（viewer 角色造 admin PAT → 400 带指引）；平台管理
// 员（或 admin 机具令牌）经 --machine 创建平台级机具令牌（CI/CD 形态）。
type tokensCreateCmd struct {
	scopes  string
	note    string
	project string
	machine bool
	jsonOut bool
	conn    connFlags
}

func (c *tokensCreateCmd) Name() string { return "create" }
func (c *tokensCreateCmd) Synopsis() string {
	return "create a token (plaintext shown once; personal PAT by default, --machine for a platform machine token)"
}
func (c *tokensCreateCmd) Usage() string {
	return "tokens create [--addr <host:port>] [--token <tok>] --scopes <read|deploy|terminal|admin[,…]> [--note <text>] [--project <id>] [--machine] [--json]"
}

func (c *tokensCreateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	// E7 W5-S6：词表增补 terminal（Web 终端独立 scope——默认仅 admin，
	// read/deploy 不蕴含；admin 蕴含一切）。W2：声明受用户可达集约束
	//（服务端校验——超集 400 带指引）。
	fs.StringVar(&c.scopes, "scopes", "read", "comma-separated scopes (read/deploy/terminal/admin; admin implies deploy, read and terminal; terminal is the web-terminal scope, not implied by read/deploy; declared scopes must not exceed your role-implied capabilities)")
	fs.StringVar(&c.note, "note", "", "human-readable note (e.g. \"laptop\" or \"CI deploy\")")
	fs.StringVar(&c.project, "bind-project", "", "optional project id to bind the token to (narrowing dimension; the role gate still applies; distinct from the --project context flag)")
	fs.BoolVar(&c.machine, "machine", false, "create a platform machine token instead of a personal PAT (platform admins only; machine tokens carry no user identity)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *tokensCreateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	scopes := strings.Split(c.scopes, ",")
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Tokens().CreateToken(ctx, &serverv1.CreateTokenRequest{
			Scopes:    scopes,
			Note:      c.note,
			ProjectId: c.project,
			Machine:   c.machine,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		kind := "personal access token"
		if c.machine {
			kind = "machine token"
		}
		fmt.Fprintf(&b, "token created (%s): %s (shown only once, store it safely — the server keeps only a hash)\n", kind, resp.GetToken())
		fmt.Fprintf(&b, "  id: %s\n  scopes: %s\n", resp.GetId(), strings.Join(resp.GetScopes(), ","))
		if resp.GetNote() != "" {
			fmt.Fprintf(&b, "  note: %s\n", resp.GetNote())
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// tokensListCmd 实现 `fleetly tokens list`（无敏感投影：备注/scope/哈希
// 前缀/属主——明文与完整哈希永不回读）。用户只见自己的 PAT；平台管理员
// 与机具令牌见全部（machine 注记区分平台级凭据）。
type tokensListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *tokensListCmd) Name() string { return "list" }
func (c *tokensListCmd) Synopsis() string {
	return "list tokens you own (platform admins see all; no sensitive projection)"
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
			_, err := fmt.Fprintln(env.Stdout, "no tokens (issue one with 'fleetly tokens create')")
			return err
		}
		var b strings.Builder
		for _, t := range resp.GetTokens() {
			line := fmt.Sprintf("%s  %-12s scopes=%s", t.GetId(), t.GetHashPrefix(), strings.Join(t.GetScopes(), ","))
			if t.GetUserId() != "" {
				line += "  user=" + t.GetUserId()
			} else {
				line += "  machine"
			}
			if t.GetProjectId() != "" {
				line += "  project=" + t.GetProjectId()
			}
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

// tokensRevokeCmd 实现 `fleetly tokens revoke <id>`（幂等；可见集外或不存在
// 一律 404；吊销最后一枚 admin token 被 E_TOKEN_LAST_ADMIN 守卫拒绝）。
type tokensRevokeCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *tokensRevokeCmd) Name() string { return "revoke" }
func (c *tokensRevokeCmd) Synopsis() string {
	return "revoke a token you own (idempotent; platform admins can revoke any)"
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
