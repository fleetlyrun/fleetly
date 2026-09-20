package cmd

// fleetly ingress status 命令（T2.18 CLI-over-SDK 改造）：入口链三面状态
// ——① Traefik 服务实况（Swarm service fleetly-ingress，服务端只读
// inspect）；② 控制面配置端点健康（/healthz 无鉴权 + /configs 带 token
// 鉴权核验；服务端环回执行）；③ 证书台账与证书存储目录对照。
//
// 全部经 SystemService.GetIngressStatus 消费——CLI 不再直连 docker /
// 读本地 token 文件 / 自行探测端点。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// ingressCmd 是外层动词 `ingress`：分发 status。
type ingressCmd struct {
	sub *commands.App
}

func newIngressCmd() *ingressCmd {
	sub := commands.New()
	sub.Register(&ingressStatusCmd{})
	sub.VerbTitle = "ingress subcommands:"
	return &ingressCmd{sub: sub}
}

func (c *ingressCmd) Name() string { return "ingress" }
func (c *ingressCmd) Synopsis() string {
	return "ingress chain (traefik deployment, config endpoint, certificates)"
}
func (c *ingressCmd) Usage() string { return "ingress <status> [flags]" }

func (c *ingressCmd) SetFlags(_ *flag.FlagSet) {}

func (c *ingressCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (status)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// ingressStatusCmd 实现 `fleetly ingress status [--json]`。
type ingressStatusCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *ingressStatusCmd) Name() string { return "status" }
func (c *ingressStatusCmd) Synopsis() string {
	return "ingress chain status (traefik service / config endpoint / certificates)"
}
func (c *ingressStatusCmd) Usage() string {
	return "ingress status [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *ingressStatusCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *ingressStatusCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().GetIngressStatus(ctx, &serverv1.GetIngressStatusRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		b.WriteString("ingress status\n")
		tv := resp.GetTraefik()
		if tv.GetExists() {
			fmt.Fprintf(&b, "  traefik: %s (static args=%d)\n", tv.GetImage(), tv.GetStaticArgs())
		} else {
			msg := tv.GetError()
			if msg == "" {
				msg = "not deployed"
			}
			fmt.Fprintf(&b, "  traefik: %s\n", msg)
		}
		fmt.Fprintf(&b, "  config endpoint: healthz=%s auth=%s\n", resp.GetHealthz(), resp.GetAuth())
		certs := resp.GetCertificates()
		fmt.Fprintf(&b, "  certificates (ledger): %d\n", len(certs))
		for _, ct := range certs {
			fmt.Fprintf(&b, "    %s: sha256:%s… expires %s\n",
				ct.GetDomain(), short8(ct.GetCertSha256()), tstampRFC3339(ct.GetCertNotAfter()))
		}
		if resp.GetCertDirError() != "" {
			fmt.Fprintf(&b, "  cert dir %s: %s\n", resp.GetCertDir(), resp.GetCertDirError())
		} else {
			fmt.Fprintf(&b, "  cert dir apps: %s\n", strings.Join(resp.GetCertDirApps(), ", "))
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// 编译期断言：ingress 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &ingressCmd{}
	_ commands.Flagged = &ingressCmd{}
	_ commands.Command = &ingressStatusCmd{}
	_ commands.Flagged = &ingressStatusCmd{}
)
