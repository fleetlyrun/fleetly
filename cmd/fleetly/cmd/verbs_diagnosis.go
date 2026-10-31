package cmd

// 诊断动词族（B2a W3，zeabur 借鉴设计 §2.5——CLI 动词 = Agent 消费面：skills
// 动线全靠 CLI 动词，无动词则 Agent 消费不到）：apps diagnose 单 App 深诊断
// （逐条目人类形态 + unreadable 诚实面）；findings list 跨 App 聚合（「需要
// 你」页，v1 挂 project 域——挂法沿 project 族动词惯例：顶层组 + --project
// 旗标锚，分页旗标沿 events list 的轴命名形态 --after-app-seq）。全部
// --json 双形态（golden 钉死）；severity 徽章 warning=WARN / advice=ADVICE。

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/lynx-go/commands"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// severityBadge 是 severity 两级的徽章投影（人类形态窄列；未知值原样大写
// ——不猜语义，注册表两级定形是常态面）。
func severityBadge(severity string) string {
	switch severity {
	case "warning":
		return "WARN"
	case "advice":
		return "ADVICE"
	default:
		return strings.ToUpper(severity)
	}
}

// findingTarget 是条目定位列的合成形态：workload id + 进程模板名（进程
// 缺席 = 非 workload 锚定条目，仅 workload id）。
func findingTarget(f *runtimev1.Finding) string {
	if p := f.GetProcess(); p != "" {
		return f.GetWorkloadId() + " (" + p + ")"
	}
	return f.GetWorkloadId()
}

// evidenceSummary 是证据摘要行（state/exit/restarts/最近事件——缺席字段
// 省略，缺席也是诚实陈述：exit 缺席 = 未观测/非终态）。
func evidenceSummary(ev *runtimev1.FindingEvidence) string {
	if ev == nil {
		return ""
	}
	parts := []string{"state=" + ev.GetState()}
	if ev.ExitCode != nil { // optional 字段：缺席 = 未观测/非终态，省略不猜
		parts = append(parts, fmt.Sprintf("exit_code=%d", ev.GetExitCode()))
	}
	parts = append(parts, fmt.Sprintf("restart_count=%d", ev.GetRestartCount()))
	if e := ev.GetRecentEvent(); e != "" {
		parts = append(parts, "recent_event="+e)
	}
	return strings.Join(parts, " ")
}

// fixSummary 是修复动作行（verb + params 预填 + console 深链；verb 空 =
// 无直达动作，深链即动线；两者皆空 = 无动作）。
func fixSummary(fix *runtimev1.FindingFix) string {
	if fix == nil {
		return "none"
	}
	if v := fix.GetVerb(); v != "" {
		keys := make([]string, 0, len(fix.GetParams()))
		for k := range fix.GetParams() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kv := make([]string, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, k+"="+fix.GetParams()[k])
		}
		out := v
		if len(kv) > 0 {
			out += " " + strings.Join(kv, " ")
		}
		if p := fix.GetConsolePath(); p != "" {
			out += " (console: " + p + ")"
		}
		return out
	}
	if p := fix.GetConsolePath(); p != "" {
		return "no direct verb (console: " + p + ")"
	}
	return "none"
}

// newAppsDiagnoseVerb 是 DiagnoseApp 的 CLI 皮（B2a W3）：单 App 读时诊断
// ——「为什么现在坏」。取参形态沿 apps 读面惯例（apps spec/workloads 的
// 位置参数 APP_ID）；空条目集 = 无失败态可解释（健康也是答案），unreadable
// 面恒诚实展示（读不了 ≠ 不存在）。
func newAppsDiagnoseVerb() commands.Command {
	const name = "diagnose"
	return &flaggedVerb{
		name:     name,
		synopsis: "Diagnose an app's failure states (read-time explanation: findings + evidence + suggested fix)",
		usage:    "apps diagnose APP_ID",
		setFlags: func(*flag.FlagSet) {},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one APP_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Diagnosis.DiagnoseApp(ctx, &runtimev1.DiagnoseAppRequest{AppId: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if len(resp.GetFindings()) == 0 {
						_, _ = fmt.Fprintln(env.Stdout, "no findings (no failure state to explain — healthy is also an answer)")
					}
					for _, f := range resp.GetFindings() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t[%s]\t%s\t%s\n",
							f.GetFindingId(), severityBadge(f.GetSeverity()), f.GetFaultClass(), findingTarget(f))
						_, _ = fmt.Fprintf(env.Stdout, "  message:  %s\n", f.GetMessage())
						if s := evidenceSummary(f.GetEvidence()); s != "" {
							_, _ = fmt.Fprintf(env.Stdout, "  evidence: %s\n", s)
						}
						_, _ = fmt.Fprintf(env.Stdout, "  fix:      %s\n", fixSummary(f.GetFix()))
					}
					if faces := resp.GetUnreadableFaces(); len(faces) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "unreadable faces: %s (facts this diagnosis cannot read yet; unreadable does not mean absent)\n",
							strings.Join(faces, ","))
					}
				})
			})
		},
	}
}

// newFindingsListVerb 是 ListProjectFindings 的 CLI 皮（「需要你」页）：
// 跨 App 聚合条目表。分页沿 ListApps 惯例（游标轴 = App 创建序；健康 App
// 推进游标但不占响应——翻页至短页）；旗标挂法沿 project 族动词（--project
// 锚）+ events list 的轴命名游标（--after-app-seq）。
func newFindingsListVerb() commands.Command {
	const name = "list"
	var project string
	var afterAppSeq int64
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List diagnosis findings aggregated across the apps of a project (apps needing attention)",
		usage:    "findings list --project PROJECT_ID [--after-app-seq N] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.Int64Var(&afterAppSeq, "after-app-seq", 0, "pagination cursor: the app seq of the last app of the previous page (0 = first page)")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Diagnosis.ListProjectFindings(ctx, &runtimev1.ListProjectFindingsRequest{
					ProjectId:   project,
					AfterAppSeq: afterAppSeq,
					Limit:       int32(limit), //nolint:gosec // 旗标域内钳制
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "APP_ID\tAPP\tSEVERITY\tFAULT\tFINDING\tMESSAGE")
					for _, af := range resp.GetApps() {
						for _, f := range af.GetFindings() {
							_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\n",
								af.GetAppId(), af.GetAppName(), severityBadge(f.GetSeverity()),
								f.GetFaultClass(), f.GetFindingId(), f.GetMessage())
						}
					}
				})
			})
		},
	}
}
