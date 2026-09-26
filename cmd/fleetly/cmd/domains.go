package cmd

// fleetly domains 命令（T2.18 CLI-over-SDK 改造；IMPL-T1-1 写面升级）：
//
//	list   —— 域名资源列表（domain/service/port/protocol/cert_mode +
//	          证书材料登记）；
//	verify —— 服务端本机视角验证：解析域名 → 探测 80/443 → 如实报告
//	          （不算 DNS 传播，架构 §2.6 DNS 契约行的核验材料 = 当前观测；
//	          探测在 daemon 侧执行，CLI 不再直连台账库）；
//	add    —— 新建域名资源（host 全局独占；写入即触发入口收敛）；
//	set    —— 局部更新（空 flag = 保持现值；host 是身份不改名）；
//	rm     —— 删除（即触发路由撤销收敛）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// domainsCmd 是外层动词 `domains`：分发 list/verify/add/set/rm。
type domainsCmd struct {
	sub *commands.App
}

func newDomainsCmd() *domainsCmd {
	sub := commands.New()
	sub.Register(&domainsListCmd{}, &domainsVerifyCmd{}, &domainsAddCmd{}, &domainsSetCmd{}, &domainsRemoveCmd{})
	sub.VerbTitle = "domains subcommands:"
	return &domainsCmd{sub: sub}
}

func (c *domainsCmd) Name() string     { return "domains" }
func (c *domainsCmd) Synopsis() string { return "route domain resources (CRUD), ledger and reachability verify" }
func (c *domainsCmd) Usage() string    { return "domains <list|verify|add|set|rm> [flags] <app>" }

func (c *domainsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *domainsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|verify|add|set|rm)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// domainsListCmd 实现 `fleetly domains list <app>`：域名资源列表。
type domainsListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *domainsListCmd) Name() string { return "list" }
func (c *domainsListCmd) Synopsis() string {
	return "list app domain resources (domain/service/port/protocol/cert_mode + cert)"
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
		resp, err := cl.Domains().ListAppDomains(ctx, &serverv1.ListAppDomainsRequest{App: c.conn.ref(args[0])})
		if err != nil {
			return err
		}
		type rowView struct {
			Domain       string `json:"domain"`
			Service      string `json:"service"`
			Port         string `json:"port,omitempty"`
			Protocol     string `json:"protocol"`
			CertMode     string `json:"cert_mode"`
			CertSHA256   string `json:"cert_sha256,omitempty"`
			CertNotAfter string `json:"cert_not_after,omitempty"`
		}
		views := make([]rowView, 0, len(resp.GetDomains()))
		for _, r := range resp.GetDomains() {
			v := rowView{
				Domain: r.GetDomain(), Service: r.GetService(), Port: r.GetPort(),
				Protocol: r.GetProtocol(), CertMode: r.GetCertMode(),
			}
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
			fmt.Fprintf(&b, "  %-40s -> %s:%s %s cert=%s", v.Domain, v.Service, v.Port, v.Protocol, v.CertMode)
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

// domainsAddCmd 实现 `fleetly domains add <app> <host>`：新建域名资源。
type domainsAddCmd struct {
	service  string
	port     string
	protocol string
	certMode string
	conn     connFlags
}

func (c *domainsAddCmd) Name() string { return "add" }
func (c *domainsAddCmd) Synopsis() string {
	return "add a domain resource (host is globally unique; the ingress converges right away)"
}
func (c *domainsAddCmd) Usage() string {
	return "domains add --service <svc> --port <n> [--protocol http|h2c] [--cert-mode http01|wildcard] [--addr <host:port>] [--token <tok>] <app> <host>"
}

func (c *domainsAddCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.service, "service", "", "compose service name the host routes to")
	fs.StringVar(&c.port, "port", "", "backend port the host routes to (required)")
	fs.StringVar(&c.protocol, "protocol", "", "backend protocol: http (default) | h2c")
	fs.StringVar(&c.certMode, "cert-mode", "", "certificate mode: http01 (default) | wildcard (DNS-01 issuance lands with W5)")
}

func (c *domainsAddCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	if c.service == "" || c.port == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing --service/--port (the route target is explicit per domain)")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Domains().CreateAppDomain(ctx, &serverv1.CreateAppDomainRequest{
			App: c.conn.ref(args[0]), Domain: args[1], Service: c.service, Port: c.port,
			Protocol: c.protocol, CertMode: c.certMode,
		})
		if err != nil {
			return err
		}
		d := resp.GetDomain()
		_, err = fmt.Fprintf(env.Stdout, "domain %s/%s added -> %s:%s %s\n",
			args[0], d.GetDomain(), d.GetService(), d.GetPort(), d.GetProtocol())
		return err
	})
}

// domainsSetCmd 实现 `fleetly domains set <app> <host>`：局部更新（空 flag
// = 保持现值；host 是资源身份不改名）。
type domainsSetCmd struct {
	service  string
	port     string
	protocol string
	certMode string
	conn     connFlags
}

func (c *domainsSetCmd) Name() string { return "set" }
func (c *domainsSetCmd) Synopsis() string {
	return "update a domain resource (omitted flags keep the current value; the host itself is the resource identity)"
}
func (c *domainsSetCmd) Usage() string {
	return "domains set [--service <svc>] [--port <n>] [--protocol http|h2c] [--cert-mode http01|wildcard] [--addr <host:port>] [--token <tok>] <app> <host>"
}

func (c *domainsSetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.service, "service", "", "compose service name the host routes to (omit to keep)")
	fs.StringVar(&c.port, "port", "", "backend port (omit to keep)")
	fs.StringVar(&c.protocol, "protocol", "", "backend protocol: http | h2c (omit to keep)")
	fs.StringVar(&c.certMode, "cert-mode", "", "certificate mode: http01 | wildcard (omit to keep)")
}

func (c *domainsSetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Domains().UpdateAppDomain(ctx, &serverv1.UpdateAppDomainRequest{
			App: c.conn.ref(args[0]), Domain: args[1], Service: c.service, Port: c.port,
			Protocol: c.protocol, CertMode: c.certMode,
		})
		if err != nil {
			return err
		}
		d := resp.GetDomain()
		_, err = fmt.Fprintf(env.Stdout, "domain %s/%s updated -> %s:%s %s cert=%s\n",
			args[0], d.GetDomain(), d.GetService(), d.GetPort(), d.GetProtocol(), d.GetCertMode())
		return err
	})
}

// domainsRemoveCmd 实现 `fleetly domains rm <app> <host>`。
type domainsRemoveCmd struct {
	conn connFlags
}

func (c *domainsRemoveCmd) Name() string { return "rm" }
func (c *domainsRemoveCmd) Synopsis() string {
	return "remove a domain resource (routes are withdrawn right away; the certificate SAN set converges on the next issuance)"
}
func (c *domainsRemoveCmd) Usage() string {
	return "domains rm [--addr <host:port>] [--token <tok>] <app> <host>"
}

func (c *domainsRemoveCmd) SetFlags(fs *flag.FlagSet) { c.conn.register(fs) }

func (c *domainsRemoveCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 2); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Domains().RemoveAppDomain(ctx, &serverv1.RemoveAppDomainRequest{App: c.conn.ref(args[0]), Domain: args[1]})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "domain %s/%s removed\n", resp.GetApp(), resp.GetDomain())
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
		resp, err := cl.Domains().VerifyAppDomains(ctx, &serverv1.VerifyAppDomainsRequest{App: c.conn.ref(args[0])})
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
	_ commands.Command = &domainsAddCmd{}
	_ commands.Flagged = &domainsAddCmd{}
	_ commands.Command = &domainsSetCmd{}
	_ commands.Flagged = &domainsSetCmd{}
	_ commands.Command = &domainsRemoveCmd{}
	_ commands.Flagged = &domainsRemoveCmd{}
)
