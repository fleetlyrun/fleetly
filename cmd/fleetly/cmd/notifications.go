package cmd

// fleetly notifications 命令（E6 观测专项设计 §5 + §8 通道扩展，W5-S4 /
// W4-S3；V2-6 Webhook 首发——POST + 重试 + 事件订阅；§8 增 slack/email 通
// 道与平台级 SMTP 设置面）：
//
//	endpoint list            端点清单（名字/类型/URL/订阅模式/开关/指纹/
//	                         最近投递终态——无敏感投影）；
//	endpoint create          创建端点（--type webhook|slack|email，email
//	                         用 --target 带收件地址；secret 明文一次性返
//	                         回，丢失只能 rotate-secret 重置）；
//	endpoint rm              删除端点（台账行随之清理）；
//	endpoint enable/disable  订阅开关（停用暂停投递不删台账）；
//	endpoint set-patterns    改订阅模式集（整体替换语义）；
//	endpoint rotate-secret   轮换签名密钥（新明文一次性返回）；
//	smtp get/set/test        平台级 SMTP 设置面（email 通道共用一份；
//	                         密码只写不读，读面只出指纹）；
//	deliveries               投递台账（重试路径与终败的诚实可见面）；
//	test                     发送 type=test 载荷（按端点类型真实试发）。
//
// 端点定位：位置参数 <name|id>——先按名精确匹配，未命中按 id 直取（CLI
// 人读形态优先名字；id 是 REST 面 handle）。
//
// 旗标在位置参数前（本仓 CLI 约定）；--json 各处输出机器可读 JSON。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// notificationsCmd 是外层动词 `notifications`：分发 endpoint/deliveries/test。
type notificationsCmd struct {
	sub *commands.App
}

func newNotificationsCmd() *notificationsCmd {
	sub := commands.New()
	sub.Register(newNotificationsEndpointCmd(), &notificationsDeliveriesCmd{},
		&notificationsTestCmd{}, newNotificationsSmtpCmd())
	sub.VerbTitle = "notifications subcommands:"
	return &notificationsCmd{sub: sub}
}

func (c *notificationsCmd) Name() string { return "notifications" }
func (c *notificationsCmd) Synopsis() string {
	return "notification endpoints and deliveries across webhook/slack/email channels (V2-6)"
}
func (c *notificationsCmd) Usage() string {
	return "notifications <endpoint|smtp|deliveries|test> [flags] [args]"
}

func (c *notificationsCmd) SetFlags(_ *flag.FlagSet) {}

func (c *notificationsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (endpoint|smtp|deliveries|test)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// notificationsEndpointCmd 是中层动词 `notifications endpoint`：分发
// list/create/rm/enable/disable/set-patterns/rotate-secret。
type notificationsEndpointCmd struct {
	sub *commands.App
}

func newNotificationsEndpointCmd() *notificationsEndpointCmd {
	sub := commands.New()
	sub.Register(
		&webhookEndpointListCmd{},
		&webhookEndpointCreateCmd{},
		&webhookEndpointRmCmd{},
		newWebhookEndpointEnabledCmd(true),
		newWebhookEndpointEnabledCmd(false),
		&webhookEndpointSetPatternsCmd{},
		&webhookEndpointRotateCmd{},
	)
	sub.VerbTitle = "endpoint subcommands:"
	return &notificationsEndpointCmd{sub: sub}
}

func (c *notificationsEndpointCmd) Name() string { return "endpoint" }
func (c *notificationsEndpointCmd) Synopsis() string {
	return "manage webhook endpoints (list/create/rm/enable/disable/set-patterns/rotate-secret)"
}
func (c *notificationsEndpointCmd) Usage() string {
	return "notifications endpoint <list|create|rm|enable|disable|set-patterns|rotate-secret> [flags] [args]"
}

func (c *notificationsEndpointCmd) SetFlags(_ *flag.FlagSet) {}

func (c *notificationsEndpointCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|create|rm|enable|disable|set-patterns|rotate-secret)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// webhookEndpointListCmd 实现 `notifications endpoint list`。
type webhookEndpointListCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *webhookEndpointListCmd) Name() string { return "list" }
func (c *webhookEndpointListCmd) Synopsis() string {
	return "list notification endpoints (no sensitive projection: secret appears as a fingerprint)"
}
func (c *webhookEndpointListCmd) Usage() string {
	return "notifications endpoint list [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *webhookEndpointListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointListCmd) Run(ctx context.Context, env *commands.Environment, _ []string) error {
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Notifications().ListWebhookEndpoints(ctx, &serverv1.ListWebhookEndpointsRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		if len(resp.GetEndpoints()) == 0 {
			b.WriteString("no notification endpoints (create one with 'notifications endpoint create')\n")
		}
		for _, e := range resp.GetEndpoints() {
			b.WriteString(endpointListLine(e))
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// endpointListLine 渲染端点清单行（通道语义列：webhook/slack 显 URL；
// email 显 to:<target>——表驱动测试钉住该口径）。
func endpointListLine(e *serverv1.WebhookEndpointView) string {
	enabled := "disabled"
	if e.GetEnabled() {
		enabled = "enabled"
	}
	where := e.GetUrl()
	if e.GetType() == "email" {
		where = "to:" + e.GetTarget()
	}
	return fmt.Sprintf("%s  [%s]  %s  secret=%s…  [%s]  %s\n",
		e.GetName(), e.GetType(), where, e.GetSecretFingerprint(), enabled,
		strings.Join(e.GetEventPatterns(), ","))
}

// webhookEndpointCreateCmd 实现 `notifications endpoint create <name> <url>
// --patterns <p[,…]> [--type webhook|slack|email] [--target <to>]
// [--disabled]`：secret 明文仅本次输出。email 端点无 URL 位置参数——
// 第二位置参数可省略，收件地址走 --target。
type webhookEndpointCreateCmd struct {
	patterns string
	typ      string
	target   string
	disabled bool
	jsonOut  bool
	conn     connFlags
}

func (c *webhookEndpointCreateCmd) Name() string { return "create" }
func (c *webhookEndpointCreateCmd) Synopsis() string {
	return "create a notification endpoint (type webhook|slack|email; email uses --target for the mailbox and takes no URL; secret shown once)"
}
func (c *webhookEndpointCreateCmd) Usage() string {
	return "notifications endpoint create [--addr <host:port>] [--token <tok>] --patterns <glob[,…]> [--type webhook|slack|email] [--target <mailbox>] [--disabled] [--json] <name> [<url>]"
}

func (c *webhookEndpointCreateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.patterns, "patterns", "", "comma-separated event-name glob patterns (e.g. deployment.*,cron.failed or * for everything)")
	fs.StringVar(&c.typ, "type", "webhook", "channel type: webhook (signed JSON POST), slack (Incoming Webhook), email (SMTP via the platform settings)")
	fs.StringVar(&c.target, "target", "", "email channel only: recipient mailbox address (required for --type email)")
	fs.BoolVar(&c.disabled, "disabled", false, "create the endpoint disabled (subscription paused)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointCreateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	typ := c.typ
	if typ == "" {
		typ = "webhook"
	}
	// email 端点：1 个位置参数（无 URL）；webhook/slack：2 个（name+url）。
	nArgs, want := 2, "<name> <url>"
	if typ == "email" {
		nArgs, want = 1, "<name>"
	}
	if len(args) < nArgs || len(args) > 2 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("want %s, got %d positional argument(s)", want, len(args))}
	}
	if strings.TrimSpace(c.patterns) == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--patterns is required (e.g. --patterns 'deployment.*' or --patterns '*')")}
	}
	enabled := !c.disabled
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Notifications().CreateWebhookEndpoint(ctx, &serverv1.CreateWebhookEndpointRequest{
			Name:          args[0],
			Url:           urlArg(args),
			Type:          typ,
			Target:        c.target,
			EventPatterns: splitCommaList(c.patterns),
			Enabled:       &enabled,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "notification endpoint created: %s (id %s, type %s)\n",
			resp.GetEndpoint().GetName(), resp.GetEndpoint().GetId(), resp.GetEndpoint().GetType())
		if resp.GetEndpoint().GetUrl() != "" {
			fmt.Fprintf(&b, "  url: %s\n", resp.GetEndpoint().GetUrl())
		}
		if resp.GetEndpoint().GetTarget() != "" {
			fmt.Fprintf(&b, "  target: %s\n", resp.GetEndpoint().GetTarget())
		}
		fmt.Fprintf(&b, "  patterns: %s\n  enabled: %v\n",
			strings.Join(resp.GetEndpoint().GetEventPatterns(), ","),
			resp.GetEndpoint().GetEnabled())
		fmt.Fprintf(&b, "  signing secret (shown only once, store it safely; the platform keeps only a fingerprint %s…):\n    %s\n",
			resp.GetEndpoint().GetSecretFingerprint(), resp.GetSecret())
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// urlArg 取第二个位置参数（email 端点无 URL——缺位时空串）。
func urlArg(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	return ""
}

// webhookEndpointRmCmd 实现 `notifications endpoint rm <name|id>`。
type webhookEndpointRmCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *webhookEndpointRmCmd) Name() string { return "rm" }
func (c *webhookEndpointRmCmd) Synopsis() string {
	return "delete a webhook endpoint (its delivery ledger rows are removed with it)"
}
func (c *webhookEndpointRmCmd) Usage() string {
	return "notifications endpoint rm [--addr <host:port>] [--token <tok>] [--json] <name|id>"
}

func (c *webhookEndpointRmCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointRmCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		id, err := resolveWebhookEndpointRef(ctx, cl, args[0])
		if err != nil {
			return err
		}
		resp, err := cl.Notifications().DeleteWebhookEndpoint(ctx, &serverv1.DeleteWebhookEndpointRequest{Id: id})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		_, err = fmt.Fprintf(env.Stdout, "webhook endpoint deleted: %s\n", args[0])
		return err
	})
}

// webhookEndpointEnabledCmd 实现 `notifications endpoint enable|disable
// <name|id>`（同一命令结构两个动词面——构造期注入目标开关）。
type webhookEndpointEnabledCmd struct {
	target  bool
	name    string
	jsonOut bool
	conn    connFlags
}

func newWebhookEndpointEnabledCmd(target bool) *webhookEndpointEnabledCmd {
	e := &webhookEndpointEnabledCmd{target: target}
	if target {
		e.name = "enable"
	} else {
		e.name = "disable"
	}
	return e
}

func (c *webhookEndpointEnabledCmd) Name() string { return c.name }
func (c *webhookEndpointEnabledCmd) Synopsis() string {
	if c.target {
		return "enable deliveries for an endpoint (pending rows resume)"
	}
	return "disable deliveries for an endpoint (subscription paused; ledger rows are kept)"
}
func (c *webhookEndpointEnabledCmd) Usage() string {
	return "notifications endpoint " + c.name + " [--addr <host:port>] [--token <tok>] [--json] <name|id>"
}

func (c *webhookEndpointEnabledCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointEnabledCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	target := c.target
	return c.conn.withClient(func(cl *fleetlyClient) error {
		id, err := resolveWebhookEndpointRef(ctx, cl, args[0])
		if err != nil {
			return err
		}
		resp, err := cl.Notifications().UpdateWebhookEndpoint(ctx, &serverv1.UpdateWebhookEndpointRequest{
			Id:      id,
			Enabled: &target,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		verb := "enabled"
		if !target {
			verb = "disabled"
		}
		_, err = fmt.Fprintf(env.Stdout, "webhook endpoint %s: %s\n", verb, resp.GetEndpoint().GetName())
		return err
	})
}

// webhookEndpointSetPatternsCmd 实现 `notifications endpoint set-patterns
// <name|id> --patterns <p[,…]>`（整体替换语义）。
type webhookEndpointSetPatternsCmd struct {
	patterns string
	jsonOut  bool
	conn     connFlags
}

func (c *webhookEndpointSetPatternsCmd) Name() string { return "set-patterns" }
func (c *webhookEndpointSetPatternsCmd) Synopsis() string {
	return "replace an endpoint's subscription patterns (whole-list semantics)"
}
func (c *webhookEndpointSetPatternsCmd) Usage() string {
	return "notifications endpoint set-patterns [--addr <host:port>] [--token <tok>] --patterns <glob[,…]> [--json] <name|id>"
}

func (c *webhookEndpointSetPatternsCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.patterns, "patterns", "", "comma-separated event-name glob patterns (whole list replaces the old set)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointSetPatternsCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	if strings.TrimSpace(c.patterns) == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--patterns is required (whole-list replace; e.g. --patterns 'deployment.*')")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		id, err := resolveWebhookEndpointRef(ctx, cl, args[0])
		if err != nil {
			return err
		}
		resp, err := cl.Notifications().UpdateWebhookEndpoint(ctx, &serverv1.UpdateWebhookEndpointRequest{
			Id:            id,
			EventPatterns: splitCommaList(c.patterns),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		_, err = fmt.Fprintf(env.Stdout, "patterns set for %s: %s\n",
			resp.GetEndpoint().GetName(), strings.Join(resp.GetEndpoint().GetEventPatterns(), ","))
		return err
	})
}

// webhookEndpointRotateCmd 实现 `notifications endpoint rotate-secret
// <name|id>`：新明文仅本次输出。
type webhookEndpointRotateCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *webhookEndpointRotateCmd) Name() string { return "rotate-secret" }
func (c *webhookEndpointRotateCmd) Synopsis() string {
	return "rotate the signing secret (new plaintext shown once; update the receiver to verify with it)"
}
func (c *webhookEndpointRotateCmd) Usage() string {
	return "notifications endpoint rotate-secret [--addr <host:port>] [--token <tok>] [--json] <name|id>"
}

func (c *webhookEndpointRotateCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *webhookEndpointRotateCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		id, err := resolveWebhookEndpointRef(ctx, cl, args[0])
		if err != nil {
			return err
		}
		resp, err := cl.Notifications().RotateWebhookSecret(ctx, &serverv1.RotateWebhookSecretRequest{Id: id})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		_, err = fmt.Fprintf(env.Stdout, "signing secret rotated (new fingerprint %s…; shown only once, store it safely):\n  %s\n",
			resp.GetSecretFingerprint(), resp.GetSecret())
		return err
	})
}

// notificationsDeliveriesCmd 实现 `notifications deliveries [--endpoint]
// [--status] [--limit]`：投递台账读面（重试路径与终败的诚实可见面）。
type notificationsDeliveriesCmd struct {
	endpoint string
	status   string
	limit    int
	jsonOut  bool
	conn     connFlags
}

func (c *notificationsDeliveriesCmd) Name() string { return "deliveries" }
func (c *notificationsDeliveriesCmd) Synopsis() string {
	return "show the webhook delivery ledger (status/attempts/response code/next retry — the honest failure face)"
}
func (c *notificationsDeliveriesCmd) Usage() string {
	return "notifications deliveries [--addr <host:port>] [--token <tok>] [--endpoint <name|id>] [--status pending|ok|failed] [--limit 50] [--json]"
}

func (c *notificationsDeliveriesCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.endpoint, "endpoint", "", "filter by endpoint (name or id)")
	fs.StringVar(&c.status, "status", "", "filter by status (pending|ok|failed)")
	fs.IntVar(&c.limit, "limit", 50, "max rows (server ceiling 500)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *notificationsDeliveriesCmd) Run(ctx context.Context, env *commands.Environment, _ []string) error {
	return c.conn.withClient(func(cl *fleetlyClient) error {
		var endpointID string
		if c.endpoint != "" {
			var err error
			endpointID, err = resolveWebhookEndpointRef(ctx, cl, c.endpoint)
			if err != nil {
				return err
			}
		}
		resp, err := cl.Notifications().ListWebhookDeliveries(ctx, &serverv1.ListWebhookDeliveriesRequest{
			EndpointId: endpointID,
			Status:     c.status,
			Limit:      int32Clamp(c.limit),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		if len(resp.GetDeliveries()) == 0 {
			b.WriteString("no deliveries recorded\n")
		}
		for _, d := range resp.GetDeliveries() {
			line := fmt.Sprintf("event_seq=%d endpoint=%s status=%s attempts=%d", d.GetEventSeq(), d.GetEndpointId(), d.GetStatus(), d.GetAttempts())
			if d.GetResponseCode() != 0 {
				line += fmt.Sprintf(" response_code=%d", d.GetResponseCode())
			}
			if d.GetNextRetryAt() != nil {
				line += fmt.Sprintf(" next_retry_at=%s", d.GetNextRetryAt().AsTime().Format("2006-01-02T15:04:05Z"))
			}
			if d.GetLastError() != "" {
				line += " last_error=" + d.GetLastError()
			}
			b.WriteString(line + "\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// notificationsTestCmd 实现 `notifications test <name|id>`：发送 type=test
// 载荷（按端点类型真实试发——webhook 签名 POST / slack {"text"} / email
// SMTP 投递到端点 target；同步返回单次投递结论）。
type notificationsTestCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *notificationsTestCmd) Name() string { return "test" }
func (c *notificationsTestCmd) Synopsis() string {
	return "send a type=test payload to an endpoint (delivered over the endpoint's channel; single synchronous attempt)"
}
func (c *notificationsTestCmd) Usage() string {
	return "notifications test [--addr <host:port>] [--token <tok>] [--json] <name|id>"
}

func (c *notificationsTestCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *notificationsTestCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if err := requireArgs(c.Usage(), args, 1); err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		id, err := resolveWebhookEndpointRef(ctx, cl, args[0])
		if err != nil {
			return err
		}
		resp, err := cl.Notifications().TestWebhook(ctx, &serverv1.TestWebhookRequest{Id: id})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if resp.GetOk() {
			_, err = fmt.Fprintf(env.Stdout, "test payload delivered: %s answered %d\n", args[0], resp.GetStatusCode())
			return err
		}
		return fmt.Errorf("test payload failed (status_code=%d): %s", resp.GetStatusCode(), resp.GetError())
	})
}

// ── notifications smtp（W4-S3 平台级 SMTP 设置面，设计 §8.3）──────────────

// notificationsSmtpCmd 是中层动词 `notifications smtp`：分发 get/set/test。
type notificationsSmtpCmd struct {
	sub *commands.App
}

func newNotificationsSmtpCmd() *notificationsSmtpCmd {
	sub := commands.New()
	sub.Register(&smtpGetCmd{}, &smtpSetCmd{}, &smtpTestCmd{})
	sub.VerbTitle = "smtp subcommands:"
	return &notificationsSmtpCmd{sub: sub}
}

func (c *notificationsSmtpCmd) Name() string { return "smtp" }
func (c *notificationsSmtpCmd) Synopsis() string {
	return "manage the platform SMTP settings shared by all email endpoints (get/set/test)"
}
func (c *notificationsSmtpCmd) Usage() string {
	return "notifications smtp <get|set|test> [flags] [args]"
}

func (c *notificationsSmtpCmd) SetFlags(_ *flag.FlagSet) {}

func (c *notificationsSmtpCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (get|set|test)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// smtpGetCmd 实现 `notifications smtp get`：设置只读展示（密码只出指纹，
// 读面永无明文——s3 show 同口径）。
type smtpGetCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *smtpGetCmd) Name() string { return "get" }
func (c *smtpGetCmd) Synopsis() string {
	return "show the platform SMTP settings (password shown as a fingerprint only)"
}
func (c *smtpGetCmd) Usage() string {
	return "notifications smtp get [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *smtpGetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *smtpGetCmd) Run(ctx context.Context, env *commands.Environment, _ []string) error {
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Notifications().GetSmtpSettings(ctx, &serverv1.GetSmtpSettingsRequest{})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		st := resp.GetSettings()
		var b strings.Builder
		if st.GetHost() == "" {
			b.WriteString("no SMTP settings saved (email endpoints cannot deliver until configured)\n")
		} else {
			fmt.Fprintf(&b, "host: %s\nport: %d\nusername: %s\nfrom: %s\n",
				st.GetHost(), st.GetPort(), st.GetUsername(), st.GetFrom())
			b.WriteString("password: ")
			if st.GetPasswordFingerprint() == "" {
				b.WriteString("(not set)")
			} else {
				fmt.Fprintf(&b, "fingerprint %s…", st.GetPasswordFingerprint())
			}
			b.WriteString("\n")
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// smtpSetCmd 实现 `notifications smtp set --host --port --from
// [--username] [--password]`：PUT 全量语义。密码经 --password 旗标或
// FLEETLY_SMTP_PASSWORD 环境变量（env 形态不进 shell 历史）；两者皆空 =
// 清除已存密码（与 PUT 全量语义一致）。
type smtpSetCmd struct {
	host     string
	port     int
	username string
	password string
	from     string
	jsonOut  bool
	conn     connFlags
}

func (c *smtpSetCmd) Name() string { return "set" }
func (c *smtpSetCmd) Synopsis() string {
	return "save the platform SMTP settings (PUT: the request is the new state; the password is write-only and shown as a fingerprint afterwards)"
}
func (c *smtpSetCmd) Usage() string {
	return "notifications smtp set [--addr <host:port>] [--token <tok>] --host <relay> --port <n> --from <addr> [--username <u>] [--password <pw>|FLEETLY_SMTP_PASSWORD env] [--json]"
}

func (c *smtpSetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.host, "host", "", "SMTP relay host (required)")
	fs.IntVar(&c.port, "port", 0, "SMTP relay port 1..65535 (required, e.g. 587)")
	fs.StringVar(&c.username, "username", "", "auth username (optional; empty = relay without authentication)")
	fs.StringVar(&c.password, "password", "", "auth password (write-only; prefer the FLEETLY_SMTP_PASSWORD env var to keep it out of shell history; empty = clear)")
	fs.StringVar(&c.from, "from", "", "envelope-from mailbox address (required)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *smtpSetCmd) Run(ctx context.Context, env *commands.Environment, _ []string) error {
	if strings.TrimSpace(c.host) == "" || c.port == 0 || strings.TrimSpace(c.from) == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--host, --port and --from are required (PUT semantics: the request is the whole new state)")}
	}
	// 密码旗标缺省回退环境变量（env 不进 shell 历史——优先形态）。
	password := c.password
	if password == "" {
		password = os.Getenv("FLEETLY_SMTP_PASSWORD")
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Notifications().UpdateSmtpSettings(ctx, &serverv1.UpdateSmtpSettingsRequest{
			Host:     c.host,
			Port:     int32(c.port), //nolint:gosec // G115：端口量级极小
			Username: c.username,
			Password: password,
			From:     c.from,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		st := resp.GetSettings()
		fp := st.GetPasswordFingerprint()
		detail := "(not set)"
		if fp != "" {
			detail = "fingerprint " + fp + "…"
		}
		_, err = fmt.Fprintf(env.Stdout, "SMTP settings saved: %s:%d from %s password %s\n",
			st.GetHost(), st.GetPort(), st.GetFrom(), detail)
		return err
	})
}

// smtpTestCmd 实现 `notifications smtp test --to <addr>
// [--host --port --username --password --from]`：对候选（未保存也能测）
// 或已存配置发测试邮件——真实 SMTP 往返。
type smtpTestCmd struct {
	to       string
	host     string
	port     int
	username string
	password string
	from     string
	jsonOut  bool
	conn     connFlags
}

func (c *smtpTestCmd) Name() string { return "test" }
func (c *smtpTestCmd) Synopsis() string {
	return "send a test email through the SMTP settings (candidate flags or the saved settings; real SMTP round trip)"
}
func (c *smtpTestCmd) Usage() string {
	return "notifications smtp test [--addr <host:port>] [--token <tok>] --to <addr> [--host <relay>] [--port <n>] [--username <u>] [--password <pw>] [--from <addr>] [--json]"
}

func (c *smtpTestCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.to, "to", "", "test-mail recipient mailbox address (required)")
	fs.StringVar(&c.host, "host", "", "candidate relay host (empty = test the saved settings)")
	fs.IntVar(&c.port, "port", 0, "candidate relay port")
	fs.StringVar(&c.username, "username", "", "candidate auth username")
	fs.StringVar(&c.password, "password", "", "candidate auth password (used for this probe only, never stored)")
	fs.StringVar(&c.from, "from", "", "candidate envelope-from address")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *smtpTestCmd) Run(ctx context.Context, env *commands.Environment, _ []string) error {
	if strings.TrimSpace(c.to) == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--to is required (the mailbox the test mail is delivered to)")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		// 候选字段 optional 语义：只带用户显式提供的字段（全缺省 = 测已存
		// 设置——空字符串不参与「是否给了候选」的判定）。
		req := &serverv1.TestSmtpRequest{To: c.to}
		if c.host != "" {
			req.Host = &c.host
		}
		if c.port != 0 {
			port := int32(c.port) //nolint:gosec // G115：端口量级极小
			req.Port = &port
		}
		if c.username != "" {
			req.Username = &c.username
		}
		if c.password != "" {
			req.Password = &c.password
		}
		if c.from != "" {
			req.From = &c.from
		}
		resp, err := cl.Notifications().TestSmtp(ctx, req)
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if resp.GetOk() {
			_, err = fmt.Fprintf(env.Stdout, "test mail accepted by the relay (DATA answered %d); check the %s mailbox\n", resp.GetStatusCode(), c.to)
			return err
		}
		return fmt.Errorf("smtp test failed (status_code=%d): %s", resp.GetStatusCode(), resp.GetError())
	})
}

// resolveWebhookEndpointRef 把 <name|id> 位置参数解析为端点 id：先按名
// 精确匹配（CLI 人读形态），未命中按 id 直取（Get 404 即端点不存在）。
func resolveWebhookEndpointRef(ctx context.Context, cl *fleetlyClient, ref string) (string, error) {
	list, err := cl.Notifications().ListWebhookEndpoints(ctx, &serverv1.ListWebhookEndpointsRequest{})
	if err == nil {
		for _, e := range list.GetEndpoints() {
			if e.GetName() == ref {
				return e.GetId(), nil
			}
		}
	}
	// 名单不可得/未命中：按 id 直取（Get 同时承担存在性校验）。
	if _, err := cl.Notifications().GetWebhookEndpoint(ctx, &serverv1.GetWebhookEndpointRequest{Id: ref}); err != nil {
		return "", err
	}
	return ref, nil
}

// splitCommaList 把逗号分隔旗标值切为去空白列表（--patterns 消费形态）。
func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
