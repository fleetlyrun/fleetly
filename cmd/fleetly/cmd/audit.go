package cmd

// fleetly audit 命令（v0.3 W3-S1，rbac-teams 设计 §5/§6 裁决 D-W0-6 的
// CLI 读面）：list（人读表 / --json）与 export --csv（RFC 4180 流式导出，
// 零第三方依赖）。**平台管理员面**——用户 principal 须 is_platform_admin；
// admin scope 的机具令牌等价放行（平台级凭据的设计语义，§2.3）。过滤集
// 两动词同源（list/export 与服务端 AuditQuery 语义一致）：actor/target
// 子串包含、action 前缀匹配、result 精确、since/until 闭区间。

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/lynx-go/commands"
	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

// auditCmd 是外层动词 `audit`：分发 list/export。
type auditCmd struct {
	sub *commands.App
}

func newAuditCmd() *auditCmd {
	sub := commands.New()
	sub.Register(&auditListCmd{}, &auditExportCmd{})
	sub.VerbTitle = "audit subcommands:"
	return &auditCmd{sub: sub}
}

func (c *auditCmd) Name() string     { return "audit" }
func (c *auditCmd) Synopsis() string { return "query the audit ledger (platform administrators)" }
func (c *auditCmd) Usage() string    { return "audit <list|export> [flags] ..." }

func (c *auditCmd) SetFlags(_ *flag.FlagSet) {}

func (c *auditCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) == 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("missing subcommand (list|export)")}
	}
	return subDispatchUsage(c, c.sub, ctx, env, args)
}

// auditFilterFlags 是 list/export 共用的过滤旗标集（两动词与 API 面语义
// 一致——单点装配，不漂移）。
type auditFilterFlags struct {
	actor  string
	action string
	result string
	target string
	since  string
	until  string
}

// register 把过滤旗标挂进动词 flag 集（帮助面写明匹配语义与格式）。
func (f *auditFilterFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.actor, "actor", "", "substring match on the actor column (human / system / user:<id>)")
	fs.StringVar(&f.action, "action", "", "prefix match on the action column (e.g. auth. matches auth.login_failed)")
	fs.StringVar(&f.result, "result", "", "exact match: ok or error")
	fs.StringVar(&f.target, "target", "", "substring match on the target column (e.g. app:<id>)")
	fs.StringVar(&f.since, "since", "", "only rows at or after this time (RFC 3339, e.g. 2026-09-01T00:00:00Z)")
	fs.StringVar(&f.until, "until", "", "only rows at or before this time (RFC 3339)")
}

// toProto 把过滤旗标映射为 ListAuditRequest 的过滤段（since/until 解析在
// 此单点——两动词共用；RFC 3339 之外的格式显式拒绝，不静默当「不过滤」）。
func (f *auditFilterFlags) toProto() (actor, action, result, target string, since, until *timestampProto, err error) {
	actor, action, result, target = f.actor, f.action, f.result, f.target
	if f.since != "" {
		t, perr := time.Parse(time.RFC3339, f.since)
		if perr != nil {
			return "", "", "", "", nil, nil, fmt.Errorf("--since: invalid RFC 3339 timestamp %q", f.since)
		}
		since = timestamppb.New(t)
	}
	if f.until != "" {
		t, perr := time.Parse(time.RFC3339, f.until)
		if perr != nil {
			return "", "", "", "", nil, nil, fmt.Errorf("--until: invalid RFC 3339 timestamp %q", f.until)
		}
		until = timestamppb.New(t)
	}
	return actor, action, result, target, since, until, nil
}

// auditListCmd 实现 `fleetly audit list`：过滤 + 分页的台账读面（人读表
// 或 --json）。
type auditListCmd struct {
	auditFilterFlags
	limit   int
	offset  int
	jsonOut bool
	conn    connFlags
}

func (c *auditListCmd) Name() string { return "list" }
func (c *auditListCmd) Synopsis() string {
	return "list audit entries (platform administrators; machine tokens with the admin scope pass alike)"
}
func (c *auditListCmd) Usage() string {
	return "audit list [--addr <host:port>] [--token <tok>] [--actor <sub>] [--action <prefix>] [--result ok|error] [--target <sub>] [--since <rfc3339>] [--until <rfc3339>] [--limit <n>] [--offset <n>] [--json]"
}

func (c *auditListCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	c.register(fs)
	fs.IntVar(&c.limit, "limit", 0, "page size (0 = server default 100, capped at 1000)")
	fs.IntVar(&c.offset, "offset", 0, "rows to skip before the first returned row")
	fs.BoolVar(&c.jsonOut, "json", false, "output machine-readable JSON")
}

func (c *auditListCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	actor, action, result, target, since, until, err := c.toProto()
	if err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		resp, err := cl.Audit().ListAudit(ctx, &serverv1.ListAuditRequest{
			Actor:  actor,
			Action: action,
			Result: result,
			Target: target,
			Since:  since,
			Until:  until,
			Limit:  int32Clamp(c.limit),
			Offset: int32Clamp(c.offset),
		})
		if err != nil {
			return err
		}
		if c.jsonOut {
			return writeProtoJSON(env.Stdout, resp)
		}
		if len(resp.GetAudits()) == 0 {
			_, err := fmt.Fprintln(env.Stdout, "no audit entries match the given filters")
			return err
		}
		var b strings.Builder
		for _, a := range resp.GetAudits() {
			at := ""
			if a.GetAt() != nil {
				at = a.GetAt().AsTime().UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(&b, "%s  %s  %-20s result=%-5s %s\n", at, a.GetId(), a.GetAction(), a.GetResult(), a.GetActor())
			if a.GetTarget() != "" {
				fmt.Fprintf(&b, "    target: %s\n", a.GetTarget())
			}
			if a.GetErrorCode() != "" {
				fmt.Fprintf(&b, "    error: %s\n", a.GetErrorCode())
			}
			if a.GetRequestId() != "" {
				fmt.Fprintf(&b, "    request: %s\n", a.GetRequestId())
			}
			if a.GetDiffSummary() != "" {
				fmt.Fprintf(&b, "    diff: %s\n", a.GetDiffSummary())
			}
		}
		fmt.Fprintf(&b, "showing %d of %d matching entries (offset %d)\n", len(resp.GetAudits()), resp.GetTotal(), c.offset)
		_, err = fmt.Fprint(env.Stdout, b.String())
		return err
	})
}

// auditExportCmd 实现 `fleetly audit export --csv <path>`：同一过滤集的
// 全量导出——分页循环流式写 CSV（每页落盘后即弃，不整体物化），表头 + 转
// 义按 RFC 4180 简明实现（零第三方依赖）。CRLF 行尾为 RFC 4180 原文形态。
type auditExportCmd struct {
	auditFilterFlags
	csvPath string
	conn    connFlags
}

func (c *auditExportCmd) Name() string { return "export" }
func (c *auditExportCmd) Synopsis() string {
	return "export audit entries to a CSV file (platform administrators; same filters as 'audit list')"
}
func (c *auditExportCmd) Usage() string {
	return "audit export [--addr <host:port>] [--token <tok>] [--actor <sub>] [--action <prefix>] [--result ok|error] [--target <sub>] [--since <rfc3339>] [--until <rfc3339>] --csv <path>"
}

func (c *auditExportCmd) SetFlags(fs *flag.FlagSet) {
	c.conn.register(fs)
	c.register(fs)
	fs.StringVar(&c.csvPath, "csv", "", "output CSV file path (required)")
}

// exportPageSize 是导出循环的单页行数（服务端天花板 1000 内取大页——页数
// 少、每页物化行数有常数上界）。
const exportPageSize = 500

// auditCSVHeader 是导出文件的首行表头（列序 = AuditRecord 字段序）。
const auditCSVHeader = "id,at,actor,action,target,result,error_code,request_id,diff_summary"

func (c *auditExportCmd) Run(ctx context.Context, env *commands.Environment, args []string) error {
	if len(args) != 0 {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("expected 0 arguments, got %d", len(args))}
	}
	if c.csvPath == "" {
		return &commands.UsageError{Usage: c.Usage(), Err: fmt.Errorf("--csv <path> is required (refusing to stream CSV into stdout implicitly — use 'audit list --json' for machine-readable output)")}
	}
	actor, action, result, target, since, until, err := c.toProto()
	if err != nil {
		return err
	}
	return c.conn.withClient(func(cl *fleetlyClient) error {
		f, err := os.Create(c.csvPath) //nolint:gosec // G304：路径为操作者显式 flag（导出目标），CLI 的本职就是写指定文件
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		w := bufio.NewWriter(f)
		if _, err := io.WriteString(w, auditCSVHeader+"\r\n"); err != nil {
			return err
		}
		rows := 0
		for offset := 0; ; offset += exportPageSize {
			resp, err := cl.Audit().ListAudit(ctx, &serverv1.ListAuditRequest{
				Actor:  actor,
				Action: action,
				Result: result,
				Target: target,
				Since:  since,
				Until:  until,
				Limit:  exportPageSize,
				Offset: int32(offset),
			})
			if err != nil {
				return err
			}
			for _, a := range resp.GetAudits() {
				at := ""
				if a.GetAt() != nil {
					at = a.GetAt().AsTime().UTC().Format(time.RFC3339)
				}
				fields := []string{
					a.GetId(), at, a.GetActor(), a.GetAction(), a.GetTarget(),
					a.GetResult(), a.GetErrorCode(), a.GetRequestId(), a.GetDiffSummary(),
				}
				for i, v := range fields {
					if i > 0 {
						if _, err := io.WriteString(w, ","); err != nil {
							return err
						}
					}
					if _, err := io.WriteString(w, csvField(v)); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(w, "\r\n"); err != nil {
					return err
				}
				rows++
			}
			if len(resp.GetAudits()) < exportPageSize || rows >= int(resp.GetTotal()) {
				break
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		_, err = fmt.Fprintf(env.Stdout, "exported %d audit entries to %s\n", rows, c.csvPath)
		return err
	})
}

// csvField 按 RFC 4180 转义单个字段：含逗号/引号/CR/LF 时整体加双引号，
// 字段内双引号翻倍（简明实现——无第三方依赖）。
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// 编译期断言：audit 命令实现 commands.Command/Flagged 契约。
var (
	_ commands.Command = &auditCmd{}
	_ commands.Flagged = &auditCmd{}
	_ commands.Command = &auditListCmd{}
	_ commands.Flagged = &auditListCmd{}
	_ commands.Command = &auditExportCmd{}
	_ commands.Flagged = &auditExportCmd{}
)
