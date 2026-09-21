package cmd

// fleetly s3 命令（E3 对象存储 §5.1/E3-2 新增动词，admin scope 专用）：
//
//	show —— S3 设置只读展示（secret 只回指纹，读面永无明文）；
//	set  —— 全量保存设置（PUT 语义：请求即新状态；external↔rustfs 互斥
//	        校验 fail-fast；保存落审计 + 事件 s3.updated）；
//	test —— 连接探针（put→get→delete 一枚探针对象并比对——通过 = 能认证/
//	        能读回，不是 TCP 探活；可带候选配置参数未保存也能测，不带则测
//	        已存配置）。
//
// 英文文案（仓库文案纪律）；secret 经 --secret-access-key 明文传入（传输
// 面 TLS 承载机密性——命令行历史暴露面与 tokens create 同级取舍）。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// s3Cmd 是外层动词 `s3`：分发 show/set/test。
type s3Cmd struct {
	sub *commands.App
}

func newS3Cmd() *s3Cmd {
	sub := commands.New()
	sub.Register(&s3ShowCmd{}, &s3SetCmd{}, &s3TestCmd{})
	sub.VerbTitle = "s3 subcommands:"
	return &s3Cmd{sub: sub}
}

func (c *s3Cmd) Name() string     { return "s3" }
func (c *s3Cmd) Synopsis() string { return "object storage settings and connection test (admin scope)" }
func (c *s3Cmd) Usage() string    { return "s3 <show|set|test> [flags] ..." }

func (c *s3Cmd) SetFlags(_ *flag.FlagSet) {}

func (c *s3Cmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (show|set|test)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// s3ShowCmd 实现 `fleetly s3 show`：设置只读展示（脱敏）。
type s3ShowCmd struct {
	jsonOut bool
	conn    connFlags
}

func (c *s3ShowCmd) Name() string { return "show" }
func (c *s3ShowCmd) Synopsis() string {
	return "show object storage settings (secret shown as fingerprint only)"
}
func (c *s3ShowCmd) Usage() string {
	return "s3 show [--addr <host:port>] [--token <tok>] [--json]"
}

func (c *s3ShowCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *s3ShowCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().GetS3Settings(ctx, &serverv1.GetS3SettingsRequest{})
		if err != nil {
			return err
		}
		st := resp.GetSettings()
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "mode: %s\n", st.GetMode())
		if st.GetEndpointUrl() != "" {
			fmt.Fprintf(&b, "endpoint: %s\n", st.GetEndpointUrl())
		}
		if st.GetRegion() != "" {
			fmt.Fprintf(&b, "region: %s\n", st.GetRegion())
		}
		if st.GetBucket() != "" {
			fmt.Fprintf(&b, "bucket: %s\n", st.GetBucket())
		}
		if st.GetAccessKeyId() != "" {
			fmt.Fprintf(&b, "access key id: %s\n", st.GetAccessKeyId())
		}
		fmt.Fprintf(&b, "secret: ")
		if st.GetSecretFingerprint() != "" {
			fmt.Fprintf(&b, "set (fingerprint %s)\n", st.GetSecretFingerprint())
		} else {
			b.WriteString("not set\n")
		}
		fmt.Fprintf(&b, "path style: %t\n", st.GetPathStyle())
		fmt.Fprintf(&b, "public exposed: %t\n", st.GetPublicExposed())
		if t := st.GetUpdatedAt(); t != nil {
			fmt.Fprintf(&b, "updated at: %s\n", tstampRFC3339(t))
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// s3SetCmd 实现 `fleetly s3 set --mode <external|rustfs|unset> ...`：全量
// 保存（PUT 语义）。external 需 endpoint/bucket/两钥齐备；rustfs 时它们
// 必须为空；--public-exposed 仅 rustfs 且要求平台已配 base_domain。
type s3SetCmd struct {
	mode       string
	endpoint   string
	region     string
	bucket     string
	accessKey  string
	secretKey  string
	pathStyle  bool
	publicExpo bool
	jsonOut    bool
	conn       connFlags
}

func (c *s3SetCmd) Name() string { return "set" }
func (c *s3SetCmd) Synopsis() string {
	return "save object storage settings (full replace; external and rustfs are mutually exclusive)"
}
func (c *s3SetCmd) Usage() string {
	return "s3 set [--addr <host:port>] [--token <tok>] --mode <unset|external|rustfs>" +
		" [--endpoint-url <url>] [--region <r>] [--bucket <b>] [--access-key-id <ak>]" +
		" [--secret-access-key <sk>] [--path-style] [--public-exposed] [--json]"
}

func (c *s3SetCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.mode, "mode", "", "storage mode: unset | external | rustfs")
	fs.StringVar(&c.endpoint, "endpoint-url", "", "S3 endpoint URL including scheme (external mode)")
	fs.StringVar(&c.region, "region", "", "S3 region (optional)")
	fs.StringVar(&c.bucket, "bucket", "", "S3 bucket (external mode)")
	fs.StringVar(&c.accessKey, "access-key-id", "", "S3 access key id (external mode)")
	fs.StringVar(&c.secretKey, "secret-access-key", "", "S3 secret access key (external mode; stored encrypted, never readable back)")
	fs.BoolVar(&c.pathStyle, "path-style", false, "use path-style addressing (self-hosted endpoints like MinIO/RustFS)")
	fs.BoolVar(&c.publicExpo, "public-exposed", false, "expose managed RustFS on the public subdomain s3.<base> (rustfs mode only; requires base_domain)")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *s3SetCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if c.mode == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--mode is required (unset|external|rustfs)")}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().UpdateS3Settings(ctx, &serverv1.UpdateS3SettingsRequest{
			Mode:            c.mode,
			EndpointUrl:     c.endpoint,
			Region:          c.region,
			Bucket:          c.bucket,
			AccessKeyId:     c.accessKey,
			SecretAccessKey: c.secretKey,
			PathStyle:       c.pathStyle,
			PublicExposed:   c.publicExpo,
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		st := resp.GetSettings()
		var b strings.Builder
		fmt.Fprintf(&b, "s3 settings saved: mode=%s", st.GetMode())
		if st.GetEndpointUrl() != "" {
			fmt.Fprintf(&b, " endpoint=%s bucket=%s", st.GetEndpointUrl(), st.GetBucket())
		}
		if st.GetSecretFingerprint() != "" {
			fmt.Fprintf(&b, " secret fingerprint=%s", st.GetSecretFingerprint())
		}
		if st.GetPublicExposed() {
			b.WriteString(" public_exposed=true (s3.<base> reachable; auth = storage credentials)")
		}
		b.WriteString("\n")
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// s3TestCmd 实现 `fleetly s3 test`：连接探针。带任一候选参数 = 测候选
// 配置（未保存也能测）；全不带 = 测已存配置。
type s3TestCmd struct {
	endpoint  string
	region    string
	bucket    string
	accessKey string
	secretKey string
	pathStyle bool
	jsonOut   bool
	conn      connFlags
}

func (c *s3TestCmd) Name() string { return "test" }
func (c *s3TestCmd) Synopsis() string {
	return "test an S3 connection (probe: put -> get -> delete one probe object; not a TCP check)"
}
func (c *s3TestCmd) Usage() string {
	return "s3 test [--addr <host:port>] [--token <tok>] [--endpoint-url <url>] [--region <r>]" +
		" [--bucket <b>] [--access-key-id <ak>] [--secret-access-key <sk>] [--path-style] [--json]"
}

func (c *s3TestCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	fs.StringVar(&c.endpoint, "endpoint-url", "", "candidate S3 endpoint URL (omit to test the saved settings)")
	fs.StringVar(&c.region, "region", "", "candidate S3 region")
	fs.StringVar(&c.bucket, "bucket", "", "candidate bucket")
	fs.StringVar(&c.accessKey, "access-key-id", "", "candidate access key id")
	fs.StringVar(&c.secretKey, "secret-access-key", "", "candidate secret access key")
	fs.BoolVar(&c.pathStyle, "path-style", false, "candidate uses path-style addressing")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *s3TestCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.System().TestS3Connection(ctx, &serverv1.TestS3ConnectionRequest{
			EndpointUrl:     c.endpoint,
			Region:          c.region,
			Bucket:          c.bucket,
			AccessKeyId:     c.accessKey,
			SecretAccessKey: c.secretKey,
			PathStyle:       c.pathStyle,
		})
		if err != nil {
			return err
		}
		res := resp.GetResult()
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		var b strings.Builder
		verdict := "ok"
		if !res.GetOk() {
			verdict = "FAILED"
		}
		fmt.Fprintf(&b, "s3 connection test %s (probe: put -> get -> delete; pass = can authenticate, can write, can read back)\n", verdict)
		fmt.Fprintf(&b, "  endpoint: %s  bucket: %s  path-style: %t\n", res.GetEndpointUrl(), res.GetBucket(), res.GetPathStyle())
		for _, st := range res.GetSteps() {
			if st.GetOk() {
				fmt.Fprintf(&b, "  %-6s ok   (%dms)\n", st.GetStep(), st.GetDurationMs())
			} else {
				fmt.Fprintf(&b, "  %-6s FAIL (%dms): %s\n", st.GetStep(), st.GetDurationMs(), st.GetError())
			}
		}
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// 编译期断言：s3 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &s3Cmd{}
	_ commands.Flagged = &s3Cmd{}
	_ commands.Command = &s3ShowCmd{}
	_ commands.Flagged = &s3ShowCmd{}
	_ commands.Command = &s3SetCmd{}
	_ commands.Flagged = &s3SetCmd{}
	_ commands.Command = &s3TestCmd{}
	_ commands.Flagged = &s3TestCmd{}
)
