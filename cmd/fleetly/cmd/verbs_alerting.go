package cmd

// Alerting/Metrics 动词组（F2.5，ADR-0041；H8 阶段 B 扩面）：metrics
// query（raw PromQL 平台管理员诊断）、metrics series（scoped 固定模板——
// member 图表/日常诊断）、channels create/test/list/delete（通知通道——
// 凭证只写不读）、alerts rules create/list/delete + alerts list（现行状态
// 含系统内置行）。

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/lynx-go/commands"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// metrics query：raw PromQL 透传（位置参数 = 查询文本；平台管理员诊断面，
// member 由服务端返回 403）。
func newMetricsQueryVerb() commands.Command {
	const name = "query"
	var start, end string
	var step int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Run a raw PromQL query (platform-admin diagnostics)",
		usage:    "metrics query PROMQL [--start T] [--end T] [--step-seconds N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&start, "start", "", "time window start (RFC3339; default: end minus 1h)")
			fs.StringVar(&end, "end", "", "time window end (RFC3339; default: now)")
			fs.Int64Var(&step, "step-seconds", 15, "sampling step in seconds")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 || args[0] == "" {
				return usageErr(name, "exactly one PromQL query argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Metrics.QueryMetrics(ctx, &telemetryv1.QueryMetricsRequest{
					Query: args[0], Start: start, End: end, StepSeconds: step,
				})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				for _, s := range resp.GetSeries() {
					for _, p := range s.GetPoints() {
						_, _ = fmt.Fprintf(env.Stdout, "%s %v\n", p.GetTime(), p.GetValue())
					}
				}
				return nil
			})
		},
	}
}

// metrics series：scoped 固定模板查询（H8 阶段 B，设计 §5.4）——调用方只给
// App/Database 作用域 + 指标白名单 + 时间窗，PromQL 由服务端按已授权行
// 构造；member 可用。人类输出逐点保留序列身份（series=<labels> <time>
// <value>），--json 输出 QueryScopedMetricsResponse protojson。
func newMetricsSeriesVerb() commands.Command {
	const name = "series"
	var appID, databaseID, metric, process, start, end string
	var step int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Query a scoped metric template for an app or database (no PromQL)",
		usage: "metrics series (--app APP_ID | --database DB_ID) --metric M [--process P] " +
			"[--start T] [--end T] [--step-seconds N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&appID, "app", "", "app id (exactly one of --app/--database)")
			fs.StringVar(&databaseID, "database", "", "database id (exactly one of --app/--database)")
			fs.StringVar(&metric, "metric", "", "metric: cpu_percent | cpu_cores | memory_working_set_bytes (required)")
			fs.StringVar(&process, "process", "", "process filter (app scope only)")
			fs.StringVar(&start, "start", "", "time window start (RFC3339; default: end minus 1h)")
			fs.StringVar(&end, "end", "", "time window end (RFC3339; default: now)")
			fs.Int64Var(&step, "step-seconds", 0, "sampling step in seconds (0 = server auto)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			switch {
			case appID == "" && databaseID == "":
				return usageErr(name, "exactly one of --app or --database is required")
			case appID != "" && databaseID != "":
				return usageErr(name, "--app and --database are mutually exclusive")
			case metric == "":
				return usageErr(name, "--metric is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Metrics.QueryScopedMetrics(ctx, &telemetryv1.QueryScopedMetricsRequest{
					AppId: appID, DatabaseId: databaseID, Metric: metric, Process: process,
					Start: start, End: end, StepSeconds: step,
				})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				for _, s := range resp.GetSeries() {
					labels := formatMetricLabels(s.GetLabels())
					for _, p := range s.GetPoints() {
						_, _ = fmt.Fprintf(env.Stdout, "series=%s %s %v\n", labels, p.GetTime(), p.GetValue())
					}
				}
				return nil
			})
		},
	}
}

// formatMetricLabels 把序列标签渲染为稳定顺序的 k=v 组（map 遍历序不
// 确定，按 key 排序保 golden 稳定）；空标签落 "-"（无标签序列仍有身份
// 行，不与时间/数值列混淆）。
func formatMetricLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

// channels create：登记通道（凭证只写不读——建后不回显）。
func newChannelsCreateVerb() commands.Command {
	const name = "create"
	var chName, kind, url, botToken, chatID string
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Register a notification channel (webhook or telegram; credentials are write-only)",
		usage:    "channels create --name NAME (--kind webhook --url URL | --kind telegram --bot-token T --chat-id C)",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&chName, "name", "", "channel name (unique)")
			fs.StringVar(&kind, "kind", "webhook", "channel kind: webhook | telegram")
			fs.StringVar(&url, "url", "", "webhook receiver endpoint (webhook kind)")
			fs.StringVar(&botToken, "bot-token", "", "telegram bot token (telegram kind)")
			fs.StringVar(&chatID, "chat-id", "", "telegram chat id (telegram kind)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if chName == "" {
				return usageErr(name, "--name is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				ctx = idem.bind(ctx)
				resp, err := c.Alerting.CreateNotificationChannel(ctx, &telemetryv1.CreateNotificationChannelRequest{
					Name: chName, Kind: kind, Url: url, BotToken: botToken, ChatId: chatID,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetChannel(), func() {
					ch := resp.GetChannel()
					_, _ = fmt.Fprintf(env.Stdout, "created channel %s [%s] (id %s)\n", ch.GetName(), ch.GetKind(), ch.GetId())
				})
			})
		},
	}
}

// channels test：即时派发测试载荷（通道验收锚）。
func newChannelsTestVerb() commands.Command {
	const name = "test"
	var channelID string
	return &flaggedVerb{
		name:     name,
		synopsis: "Send one test payload through a notification channel",
		usage:    "channels test --channel CHANNEL_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&channelID, "channel", "", "channel id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if channelID == "" {
				return usageErr(name, "--channel is required")
			}
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Alerting.TestNotificationChannel(ctx, &telemetryv1.TestNotificationChannelRequest{ChannelId: channelID})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				if resp.GetDelivered() {
					_, _ = fmt.Fprintln(env.Stdout, "test payload delivered")
					return nil
				}
				_, _ = fmt.Fprintf(env.Stdout, "delivery failed: %s\n", resp.GetError())
				return fmt.Errorf("channel test failed: %s", resp.GetError())
			})
		},
	}
}

// channels list：列通道（凭证永不回显）。
func newChannelsListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List notification channels (credentials are write-only and never shown)",
		usage:    "channels list",
		setFlags: func(*flag.FlagSet) {},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Alerting.ListNotificationChannels(ctx, &telemetryv1.ListNotificationChannelsRequest{})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				for _, ch := range resp.GetChannels() {
					fail := ch.GetLastFailure()
					if fail != "" {
						fail = " last-failure: " + fail
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s%s\n", ch.GetId(), ch.GetKind(), ch.GetName(), fail)
				}
				return nil
			})
		},
	}
}

// channels delete：删通道。
func newChannelsDeleteVerb() commands.Command {
	const name = "delete"
	var channelID string
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete a notification channel",
		usage:    "channels delete --channel CHANNEL_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&channelID, "channel", "", "channel id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if channelID == "" {
				return usageErr(name, "--channel is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				if _, err := c.Alerting.DeleteNotificationChannel(ctx, &telemetryv1.DeleteNotificationChannelRequest{ChannelId: channelID}); err != nil {
					return err
				}
				if jsonOut {
					// 删除成功必须回可解析对象（R10-M1；与 tasks/schedules
					// delete 同形）——空 stdout 会让 Agent 误判"命令未执行"。
					return writeJSON(env.Stdout, map[string]string{"deleted": channelID})
				}
				_, _ = fmt.Fprintf(env.Stdout, "deleted channel %s\n", channelID)
				return nil
			})
		},
	}
}

// alerts rules create：登记阈值规则。
func newAlertsRulesCreateVerb() commands.Command {
	const name = "create"
	var appID, metric string
	var threshold float64
	var forSecs int64
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a per-app threshold alert rule (cpu_percent or memory_working_set_bytes)",
		usage:    "alerts rules create --app APP_ID --metric M --threshold V [--for-seconds N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&appID, "app", "", "app id (required)")
			fs.StringVar(&metric, "metric", "cpu_percent", "metric: cpu_percent | memory_working_set_bytes")
			fs.Float64Var(&threshold, "threshold", 0, "threshold value (cpu percent, or bytes)")
			fs.Int64Var(&forSecs, "for-seconds", 0, "breach must hold this long before firing (0 = immediate)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if appID == "" {
				return usageErr(name, "--app is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				ctx = idem.bind(ctx)
				resp, err := c.Alerting.CreateAlertRule(ctx, &telemetryv1.CreateAlertRuleRequest{
					AppId: appID, Metric: metric, Threshold: threshold, ForSeconds: forSecs,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetRule(), func() {
					r := resp.GetRule()
					_, _ = fmt.Fprintf(env.Stdout, "created alert rule %s (%s > %v, for %ds)\n", r.GetId(), r.GetMetric(), r.GetThreshold(), r.GetForSeconds())
				})
			})
		},
	}
}

// alerts rules list：列规则。
func newAlertsRulesListVerb() commands.Command {
	const name = "list"
	var appID string
	return &flaggedVerb{
		name:     name,
		synopsis: "List alert rules (optionally filtered by app)",
		usage:    "alerts rules list [--app APP_ID]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&appID, "app", "", "filter by app id")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Alerting.ListAlertRules(ctx, &telemetryv1.ListAlertRulesRequest{AppId: appID})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				for _, r := range resp.GetRules() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%v\tfor %ds\t%s\n",
						r.GetId(), r.GetAppId(), r.GetMetric(), r.GetThreshold(), r.GetForSeconds(), r.GetState())
				}
				return nil
			})
		},
	}
}

// alerts rules delete：删规则。
func newAlertsRulesDeleteVerb() commands.Command {
	const name = "delete"
	var ruleID string
	return &flaggedVerb{
		name:     name,
		synopsis: "Delete an alert rule",
		usage:    "alerts rules delete --rule RULE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&ruleID, "rule", "", "rule id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if ruleID == "" {
				return usageErr(name, "--rule is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				if _, err := c.Alerting.DeleteAlertRule(ctx, &telemetryv1.DeleteAlertRuleRequest{RuleId: ruleID}); err != nil {
					return err
				}
				if jsonOut {
					// 删除成功必须回可解析对象（R10-M1；与 tasks/schedules
					// delete 同形）。
					return writeJSON(env.Stdout, map[string]string{"deleted": ruleID})
				}
				_, _ = fmt.Fprintf(env.Stdout, "deleted alert rule %s\n", ruleID)
				return nil
			})
		},
	}
}

// alerts list：现行状态（含系统内置行）。
func newAlertsListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List current alert states (user rules and built-in system rules)",
		usage:    "alerts list",
		setFlags: func(*flag.FlagSet) {},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Alerting.ListAlertStates(ctx, &telemetryv1.ListAlertStatesRequest{})
				if err != nil {
					return err
				}
				if jsonOut {
					return renderOut(env, jsonOut, resp, func() {})
				}
				for _, s := range resp.GetStates() {
					sys := ""
					if s.GetSystem() {
						sys = "\tsystem"
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s%s\n",
						s.GetRuleId(), s.GetAppId(), s.GetMetric(), s.GetState(), s.GetStateSince(), sys)
				}
				return nil
			})
		},
	}
}
