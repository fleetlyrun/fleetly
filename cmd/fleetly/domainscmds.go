package main

// fleetly domains 命令（T2.15/T2.16）：域名台账与验证。
//
//	list   —— 域名台账只读列表（domain/service/port + 证书材料登记）；
//	verify —— 本机视角验证：解析域名 → 探测 80/443 → 如实报告（不算
//	          DNS 传播，架构 §2.6 DNS 契约行的核验材料 = 当前观测）。
//
// 状态库直连（与 env/placement/nodes 同拓扑：gRPC API 面随 T2.17 统一
// 接线，本期 CLI 打开本地 SQLite 直读）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/ingress"
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

func (c *domainsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|verify)")}
	}
	return c.sub.SubDispatch(ctx, env, args)
}

// domainsListCmd 实现 `fleetly domains list <app>`：台账只读列表。
type domainsListCmd struct {
	db      string
	jsonOut bool
}

func (c *domainsListCmd) Name() string { return "list" }
func (c *domainsListCmd) Synopsis() string {
	return "list app route domains (ledger: domain/service/port + cert)"
}
func (c *domainsListCmd) Usage() string {
	return "domains list [--db <path>] [--json] <app>"
}

func (c *domainsListCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *domainsListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	rows, err := st.ListAppDomains(ctx, app.ID)
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
	views := make([]rowView, 0, len(rows))
	for _, r := range rows {
		v := rowView{Domain: r.Domain, Service: r.Service, Port: r.Port}
		if r.CertSHA256 != "" {
			v.CertSHA256 = r.CertSHA256
			v.CertNotAfter = r.CertNotAfter.Format("2006-01-02T15:04:05Z07:00")
		}
		views = append(views, v)
	}
	if c.jsonOut {
		return writeJSON(env, map[string]any{"app": args[0], "domains": views})
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
}

// domainsVerifyCmd 实现 `fleetly domains verify <app>`：解析 + 80/443 探测
// （本机视角；结果如实呈现——解析不到/不可达/证书形态都是输出的一部分，
// 不做传播判定）。
type domainsVerifyCmd struct {
	db      string
	jsonOut bool
}

func (c *domainsVerifyCmd) Name() string { return "verify" }
func (c *domainsVerifyCmd) Synopsis() string {
	return "verify app domains from this host (resolve + probe 80/443; current view only)"
}
func (c *domainsVerifyCmd) Usage() string {
	return "domains verify [--db <path>] [--json] <app>"
}

func (c *domainsVerifyCmd) SetFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.db, "db", defaultDBPath, "state db path")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *domainsVerifyCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	st, done, err := openStore(c.db)
	if err != nil {
		return err
	}
	defer done()
	app, err := lookupApp(ctx, st, args[0])
	if err != nil {
		return err
	}
	rows, err := st.ListAppDomains(ctx, app.ID)
	if err != nil {
		return err
	}
	domains := make([]string, 0, len(rows))
	for _, r := range rows {
		domains = append(domains, r.Domain)
	}
	checks := ingress.VerifyDomains(ctx, domains)
	if c.jsonOut {
		return writeJSON(env, map[string]any{"app": args[0], "checks": checks})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "app %s: verifying %d domains (local view; no propagation judgment)\n", args[0], len(checks))
	for _, ck := range checks {
		fmt.Fprintf(&b, "  %s\n", ck.Domain)
		if ck.Err != "" {
			fmt.Fprintf(&b, "    error: %s\n", ck.Err)
			continue
		}
		fmt.Fprintf(&b, "    resolves: %s\n", strings.Join(ck.IPs, ", "))
		fmt.Fprintf(&b, "    :80  %s\n", ck.HTTP80)
		fmt.Fprintf(&b, "    :443 %s\n", ck.HTTPS443)
		if ck.CertSubject != "" {
			fmt.Fprintf(&b, "    cert: %s (SANs: %s; expires %s)\n",
				ck.CertSubject, strings.Join(ck.CertDNSNames, ", "), ck.CertNotAfter)
		}
	}
	_, err = fmt.Fprint(env.Stdout, b.String())
	return err
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
	_ commands.Command = &domainsListCmd{}
	_ commands.Flagged = &domainsListCmd{}
	_ commands.Command = &domainsVerifyCmd{}
	_ commands.Flagged = &domainsVerifyCmd{}
)
