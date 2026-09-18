package main

// fleetly git 命令（T2.19）：git push(SSH) 触发入口的公钥管理面（admin
// scope）。动词面 = `git keys add|list|rm`：
//
//	git keys add  <key.pub>   注册平台管理公钥（authorized_keys 单行；
//	                          明文私钥永不经过平台）；
//	git keys list             在册公钥（指纹/类型/备注）；
//	git keys rm   <id>        删除公钥（新 SSH 握手即拒绝）。
//
// push 用法：`git push ssh://git@<host>:8424/<app>.git <branch>`——SSH
// 面默认只绑 127.0.0.1（安全默认基线），VPS 对外暴露见 README。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// gitCmd 是外层动词 `git`：分发 keys。
type gitCmd struct {
	sub *commands.App
}

func newGitCmd() *gitCmd {
	sub := commands.New()
	sub.Register(newGitKeysCmd())
	sub.VerbTitle = "git subcommands:"
	return &gitCmd{sub: sub}
}

func (c *gitCmd) Name() string     { return "git" }
func (c *gitCmd) Synopsis() string { return "git push (SSH) management (deploy keys)" }
func (c *gitCmd) Usage() string    { return "git <keys> [flags] ..." }

func (c *gitCmd) SetFlags(_ *flag.FlagSet) {}

func (c *gitCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (keys)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// gitKeysCmd 是中层动词 `git keys`：分发 add/list/rm。
type gitKeysCmd struct {
	sub *commands.App
}

func newGitKeysCmd() *gitKeysCmd {
	sub := commands.New()
	sub.Register(&gitKeysAddCmd{}, &gitKeysListCmd{}, &gitKeysRmCmd{})
	sub.VerbTitle = "git keys subcommands:"
	return &gitKeysCmd{sub: sub}
}

func (c *gitKeysCmd) Name() string     { return "keys" }
func (c *gitKeysCmd) Synopsis() string { return "manage git deploy keys (admin scope)" }
func (c *gitKeysCmd) Usage() string    { return "git keys <add|list|rm> [flags] ..." }

func (c *gitKeysCmd) SetFlags(_ *flag.FlagSet) {}

func (c *gitKeysCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (add|list|rm)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// gitKeysAddCmd 实现 `fleetly git keys add <path-or-key>`：位置参数是
// authorized_keys 单行文件路径（以 ssh-/ecdsa- 开头则视为公钥本体）。
type gitKeysAddCmd struct {
	note    string
	jsonOut bool
	conn    connFlags
}

func (c *gitKeysAddCmd) Name() string { return "add" }
func (c *gitKeysAddCmd) Synopsis() string {
	return "register a deploy public key (authorized_keys line or file)"
}
func (c *gitKeysAddCmd) Usage() string {
	return "git keys add [--addr <host:port>] [--token <tok>] [--note <text>] [--json] <key.pub | \"ssh-ed25519 AAA…\">"
}

func (c *gitKeysAddCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.note, "note", "", "human-readable note (defaults to key comment)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *gitKeysAddCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	line := args[0]
	if !strings.HasPrefix(line, "ssh-") && !strings.HasPrefix(line, "ecdsa-") && !strings.HasPrefix(line, "sk-") {
		b, err := osReadFile(line)
		if err != nil {
			// 文件不存在且形态不像路径时给出可行动提示。
			return fmt.Errorf("read %s: %w（位置参数应为公钥文件路径，或以 ssh- 开头的公钥本体）", line, err)
		}
		line = strings.TrimSpace(string(b))
	}
	if line == "" {
		return fmt.Errorf("public key is empty")
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.GitKeys().AddGitKey(ctx, &serverv1.AddGitKeyRequest{PublicKey: line, Note: c.note})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "git key added: %s\n", resp.GetId())
		fmt.Fprintf(&b, "  fingerprint: %s\n  type: %s\n", resp.GetFingerprint(), resp.GetKeyType())
		if resp.GetNote() != "" {
			fmt.Fprintf(&b, "  note: %s\n", resp.GetNote())
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// gitKeysListCmd 实现 `fleetly git keys list`（公钥为公开材料可回读；
// 平台从不接触私钥）。
type gitKeysListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *gitKeysListCmd) Name() string { return "list" }
func (c *gitKeysListCmd) Synopsis() string {
	return "list registered deploy keys"
}
func (c *gitKeysListCmd) Usage() string {
	return "git keys list [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *gitKeysListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *gitKeysListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.GitKeys().ListGitKeys(ctx, &serverv1.ListGitKeysRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if len(resp.GetKeys()) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no git keys（fleetly git keys add 注册公钥后即可 git push 部署）")
			return err
		}
		var b strings.Builder
		for _, k := range resp.GetKeys() {
			line := fmt.Sprintf("%s  %s  %s", k.GetId(), k.GetKeyType(), k.GetFingerprint())
			if k.GetNote() != "" {
				line += "  note=" + k.GetNote()
			}
			b.WriteString(line + "\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// gitKeysRmCmd 实现 `fleetly git keys rm <id>`（不存在 404；删除即时生效）。
type gitKeysRmCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *gitKeysRmCmd) Name() string { return "rm" }
func (c *gitKeysRmCmd) Synopsis() string {
	return "remove a deploy key"
}
func (c *gitKeysRmCmd) Usage() string {
	return "git keys rm [--addr <host:port>] [--token <tok>] [--json] <id>"
}

func (c *gitKeysRmCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *gitKeysRmCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.GitKeys().RemoveGitKey(ctx, &serverv1.RemoveGitKeyRequest{Id: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"id": resp.GetId(), "removed": true})
		}
		_, err = fmt.Fprintf(env.Stdout, "git key %s removed\n", resp.GetId())
		return err
	})
}

// 编译期断言：git 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &gitCmd{}
	_ commands.Flagged = &gitCmd{}
	_ commands.Command = &gitKeysCmd{}
	_ commands.Flagged = &gitKeysCmd{}
	_ commands.Command = &gitKeysAddCmd{}
	_ commands.Flagged = &gitKeysAddCmd{}
	_ commands.Command = &gitKeysListCmd{}
	_ commands.Flagged = &gitKeysListCmd{}
	_ commands.Command = &gitKeysRmCmd{}
	_ commands.Flagged = &gitKeysRmCmd{}
)
