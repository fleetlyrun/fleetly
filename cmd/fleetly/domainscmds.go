package main

// fleetly domains 命令（T2.18 CLI-over-SDK 改造）：域名台账与验证。
//
//	list   —— 域名台账只读列表（domain/service/port + 证书材料登记）；
//	verify —— 服务端本机视角验证：解析域名 → 探测 80/443 → 如实报告
//	          （不算 DNS 传播，架构 §2.6 DNS 契约行的核验材料 = 当前观测；
//	          探测在 daemon 侧执行，CLI 不再直连台账库）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// domainsCmd 是外层动词 `domains`：分发 list/verify。
type domainsCmd struct {
	sub *commands.App
}

func newDomainsCmd() *domainsCmd {
	sub := commands.New()
	sub.Register(&domainsListCmd{}, &domainsVerifyCmd{})
	sub.VerbTitle = "domains subcommands:"
	return &domainsCmd{sub: sub}
}

func (c *domainsCmd) Name() string     { return "domains" }
func (c *domainsCmd) Synopsis() string { return "route domains ledger and reachability verify" }
func (c *domainsCmd) Usage() string    { return "domains <list|verify> [flags] <app>" }

func (c *domainsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *domainsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|verify)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// domainsListCmd 实现 `fleetly domains list <app>`：台账只读列表。
type domainsListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *domainsListCmd) Name() string { return "list" }
func (c *domainsListCmd) Synopsis() string {
	return "list app route domains (ledger: domain/service/port + cert)"
}
func (c *domainsListCmd) Usage() string {
	return "domains list [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *domainsListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *domainsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Domains().ListAppDomains(ctx, &serverv1.ListAppDomainsRequest{App: args[0]})
		if err != nil {
			return err
		}
		type rowView struct {
			Domain       string `json:"domain"`
			Service      string `json:"service"`
			Port         string `json:"port,omitempty"`
			CertSHA256   string `json:"cert_sha256,omitempty"`
			CertNotAfter string `json:"cert_not_after,omitempty"`
		}
		views := make([]rowView, 0, len(resp.GetDomains()))
		for _, r := range resp.GetDomains() {
			v := rowView{Domain: r.GetDomain(), Service: r.GetService(), Port: r.GetPort()}
			if r.GetCertSha256() != "" {
				v.CertSHA256 = r.GetCertSha256()
				v.CertNotAfter = tstampRFC3339(r.GetCertNotAfter())
			}
			views = append(views, v)
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"app": args[0], "domains": views})
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s: %d domains\n", args[0], len(views))
		for _, v := range views {
			fmt.Fprintf(&b, "  %-40s -> %s:%s", v.Domain, v.Service, v.Port)
			if v.CertSHA256 != "" {
				fmt.Fprintf(&b, "  cert sha256:%s… (expires %s)", short8(v.CertSHA256), v.CertNotAfter)
			} else {
				b.WriteString("  cert: none")
			}
			b.WriteString("\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// domainsVerifyCmd 实现 `fleetly domains verify <app>`：解析 + 80/443 探测
// （服务端本机视角；结果如实呈现——解析不到/不可达/证书形态都是输出的一
// 部分，不做传播判定）。
type domainsVerifyCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *domainsVerifyCmd) Name() string { return "verify" }
func (c *domainsVerifyCmd) Synopsis() string {
	return "verify app domains from the daemon host (resolve + probe 80/443; current view only)"
}
func (c *domainsVerifyCmd) Usage() string {
	return "domains verify [--addr <host:port>] [--token <tok>] [--json] <app>"
}

func (c *domainsVerifyCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *domainsVerifyCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Domains().VerifyAppDomains(ctx, &serverv1.VerifyAppDomainsRequest{App: args[0]})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeJSON(env.Stdout, map[string]any{"app": args[0], "checks": resp.GetChecks()})
		}
		var b strings.Builder
		fmt.Fprintf(&b, "app %s: verifying %d domains (daemon-local view; no propagation judgment)\n",
			args[0], len(resp.GetChecks()))
		for _, ck := range resp.GetChecks() {
			fmt.Fprintf(&b, "  %s\n", ck.GetDomain())
			if ck.GetError() != "" {
				fmt.Fprintf(&b, "    error: %s\n", ck.GetError())
				continue
			}
			fmt.Fprintf(&b, "    resolves: %s\n", strings.Join(ck.GetIps(), ", "))
			fmt.Fprintf(&b, "    :80  %s\n", ck.GetHttp_80())
			fmt.Fprintf(&b, "    :443 %s\n", ck.GetHttps_443())
			if ck.GetCertSubject() != "" {
				expires := ""
				if t := ck.GetCertNotAfter(); t != nil {
					expires = t.AsTime().Format("2006-01-02T15:04:05Z07:00")
				}
				fmt.Fprintf(&b, "    cert: %s (SANs: %s; expires %s)\n",
					ck.GetCertSubject(), strings.Join(ck.GetCertDnsNames(), ", "), expires)
			}
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// short8 取 sha256 hex 前 8 位（展示形态）。
func short8(hex string) string {
	if len(hex) > 8 {
		return hex[:8]
	}
	return hex
}

// 编译期断言：domains 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &domainsCmd{}
	_ commands.Flagged = &domainsCmd{}
	_ commands.Command = &domainsListCmd{}
	_ commands.Flagged = &domainsListCmd{}
	_ commands.Command = &domainsVerifyCmd{}
	_ commands.Flagged = &domainsVerifyCmd{}
)
