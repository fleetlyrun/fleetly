package cmd

// fleetly registry 命令（IMPL-T1-2/DT-2 新增动词，admin scope 专用）：
//
//	show  —— registry 凭证设置只读展示（密码只回指纹，读面永无明文）；
//	set   —— 保存设置（host + 用户名 + 密码；密码留空 = 保留已存密码）；
//	clear —— 清除全部设置（host 留空 = 四键齐清；解析腿回落匿名 + 本机
//	         inspect）。
//
// 英文文案（仓库文案纪律）；密码经 --password 明文传入（传输面 TLS 承载
// 机密性——命令行历史暴露面与 s3 --secret-access-key 同级取舍）。设置效果：
// tag 引用部署时经 registry API 解析为 digest（命中 host 携凭证），service
// create/update 隨 X-Registry-Auth 把凭证分发到拉取节点。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// registryCmd 是外层动词 `registry`：分发 show/set/clear。
type registryCmd struct {
	sub *commands.App
}

func newRegistryCmd() *registryCmd {
	sub := commands.New()
	sub.Register(&registryShowCmd{}, &registrySetCmd{}, &registryClearCmd{})
	sub.VerbTitle = "registry subcommands:"
	return &registryCmd{sub: sub}
}

func (c *registryCmd) Name() string     { return "registry" }
func (c *registryCmd) Synopsis() string { return "platform registry credentials (admin scope)" }
func (c *registryCmd) Usage() string    { return "registry <show|set|clear> [flags] ..." }

func (c *registryCmd) SetFlags(_ *flag.FlagSet) {}

func (c *registryCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (show|set|clear)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// registryShowCmd 实现 `fleetly registry show`：设置只读展示（脱敏）。
type registryShowCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *registryShowCmd) Name() string { return "show" }
func (c *registryShowCmd) Synopsis() string {
	return "show platform registry credentials (password shown as fingerprint only)"
}
func (c *registryShowCmd) Usage() string {
	return "registry show [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *registryShowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *registryShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().GetRegistrySettings(ctx, &serverv1.GetRegistrySettingsRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		st := resp.GetSettings()
		var b strings.Builder
		if st.GetHost() == "" {
			b.WriteString("registry credentials not configured (registry.host empty; tag resolution is anonymous and deploys fall back to local images)\n")
			_, werr := fmt.Fprint(env.Stdout, b.String())
			return werr
		}
		fmt.Fprintf(&b, "host: %s\n", st.GetHost())
		if st.GetUsername() != "" {
			fmt.Fprintf(&b, "username: %s\n", st.GetUsername())
		}
		fmt.Fprintf(&b, "password: ")
		if st.GetPasswordFingerprint() != "" {
			fmt.Fprintf(&b, "set (fingerprint %s)\n", st.GetPasswordFingerprint())
		} else {
			b.WriteString("not set\n")
		}
		if t := st.GetUpdatedAt(); t != nil {
			fmt.Fprintf(&b, "updated at: %s\n", tstampRFC3339(t))
		}
		_, werr := fmt.Fprint(env.Stdout, b.String())
		return werr
	})
}

// registrySetCmd 实现 `fleetly registry set --host <host> [--username <u>]
// [--password <pw>]`：保存设置（password 留空 = 保留已存密码；host 归一
// 由服务端执行——scheme 会被剥离）。
type registrySetCmd struct {
	host     string
	username string
	password string
	jsonOut  bool
	conn     connFlags
}

func (c *registrySetCmd) Name() string { return "set" }
func (c *registrySetCmd) Synopsis() string {
	return "save platform registry credentials (empty --password keeps the stored password)"
}
func (c *registrySetCmd) Usage() string {
	return "registry set [--addr <host:port>] [--token <tok>] --host <host[:port]>" +
		" [--username <u>] [--password <pw>] [--json]"
}

func (c *registrySetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.host, "host", "", "registry host, e.g. ghcr.io (scheme is stripped; docker.io normalizes to registry-1.docker.io)")
	fs.StringVar(&c.username, "username", "", "registry username (Basic Auth)")
	fs.StringVar(&c.password, "password", "", "registry password (stored encrypted, never readable back; empty keeps the stored password)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *registrySetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if strings.TrimSpace(c.host) == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--host is required (use 'registry clear' to remove the settings)")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().UpdateRegistrySettings(ctx, &serverv1.UpdateRegistrySettingsRequest{
			Host:     c.host,
			Username: c.username,
			Password: c.password,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		st := resp.GetSettings()
		var b strings.Builder
		fmt.Fprintf(&b, "registry settings saved: host=%s", st.GetHost())
		if st.GetUsername() != "" {
			fmt.Fprintf(&b, " username=%s", st.GetUsername())
		}
		if st.GetPasswordFingerprint() != "" {
			fmt.Fprintf(&b, " password fingerprint=%s", st.GetPasswordFingerprint())
		} else {
			b.WriteString(" (no password: anonymous pulls only)")
		}
		b.WriteString("\n")
		_, werr := fmt.Fprint(env.Stdout, b.String())
		return werr
	})
}

// registryClearCmd 实现 `fleetly registry clear`：清除全部设置（host 留空
// = 四键齐清）。
type registryClearCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *registryClearCmd) Name() string { return "clear" }
func (c *registryClearCmd) Synopsis() string {
	return "clear platform registry credentials (resolution falls back to anonymous)"
}
func (c *registryClearCmd) Usage() string {
	return "registry clear [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *registryClearCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *registryClearCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().UpdateRegistrySettings(ctx, &serverv1.UpdateRegistrySettingsRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if host := resp.GetSettings().GetHost(); host != "" {
			return fmt.Errorf("registry settings still configured after clear (host=%s)", host)
		}
		_, werr := fmt.Fprintln(env.Stdout, "registry settings cleared (registry.host empty; tag resolution is anonymous and deploys fall back to local images)")
		return werr
	})
}

// 编译期断言：registry 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &registryCmd{}
	_ commands.Flagged = &registryCmd{}
	_ commands.Command = &registryShowCmd{}
	_ commands.Flagged = &registryShowCmd{}
	_ commands.Command = &registrySetCmd{}
	_ commands.Flagged = &registrySetCmd{}
	_ commands.Command = &registryClearCmd{}
	_ commands.Flagged = &registryClearCmd{}
)
