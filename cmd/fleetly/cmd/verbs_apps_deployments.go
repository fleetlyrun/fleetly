package cmd

// apps 组的部署行观测面（立案②，staging 记录·六十二发现②/③）：apps 组
// 收窄后走查无 CLI 面看部署行状态/错误正文（d3-d8 连环 failed 只能 SDK
// 直读）。两形态：deployments（list——ID/STATE/GEN/ERROR 行表，error 截
// 断）+ deployment（get——error 完整正文、from/to revision 与时间戳；
// 排查连环失败时正文不截断是刚需）。RPC 面复用 delivery.v1 既有
// ListDeployments/GetDeployment，零 proto 改动。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// newAppsDeploymentsVerb 构造 apps deployments（list 面）：ListDeployments
// 的 apps 组挂载——与顶层 deployments list 同 RPC，差异在人读表的排查取
// 向（ERROR 列在场；REVISION/UPDATED 让位）。error 截断 ~80 字符，全文走
// apps deployment get。
func newAppsDeploymentsVerb() commands.Command {
	const name = "deployments"
	var app string
	var pages pageFlags // 游标分页旗标收敛共享 struct（deployments list 同款）
	return &flaggedVerb{
		name:     name,
		synopsis: "List an app's deployments with state and error (newest first; full errors via 'apps deployment')",
		usage:    "apps deployments --app APP_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id, name or project/name form (required)")
			pages.register(fs, "after")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				appID, err := resolveAppID(ctx, c, "", app)
				if err != nil {
					return err
				}
				resp, err := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{
					AppId: appID, AfterDeploymentSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tGEN\tERROR")
					for _, d := range resp.GetDeployments() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\n",
							deploymentShortID(d.GetId()), d.GetState(), generationCell(d),
							truncateCell(d.GetError(), 80))
					}
				})
			})
		},
	}
}

// newAppsDeploymentVerb 构造 apps deployment（get 面）：GetDeployment 的
// apps 组挂载——单行全文（error 完整正文不截断 + from/to revision + 双代
// 窗 + 时间戳）。顶层 deployments get 同 RPC；本面收位置参数形态（d3-d8
// 连环失败的逐行下钻动线）。
func newAppsDeploymentVerb() commands.Command {
	const name = "deployment"
	return &flaggedVerb{
		name:     name,
		synopsis: "Show one deployment in full (untruncated error, from/to revisions, generation window, timestamps)",
		usage:    "apps deployment DEPLOYMENT_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DEPLOYMENT_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				resp, err := c.Deployments.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetDeployment(), func() {
					d := resp.GetDeployment()
					_, _ = fmt.Fprintf(env.Stdout, "deployment %s %s\n", d.GetId(), d.GetState())
					_, _ = fmt.Fprintf(env.Stdout, "  app:        %s\n", d.GetAppId())
					_, _ = fmt.Fprintf(env.Stdout, "  revision:   %s → %s\n", d.GetFromRevision(), d.GetToRevision())
					_, _ = fmt.Fprintf(env.Stdout, "  generation: %s\n", generationCell(d))
					_, _ = fmt.Fprintf(env.Stdout, "  created:    %s\n", d.GetCreatedAt())
					_, _ = fmt.Fprintf(env.Stdout, "  updated:    %s\n", d.GetUpdatedAt())
					if d.GetFinishedAt() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "  finished:   %s\n", d.GetFinishedAt())
					}
					if d.GetObserveDeadline() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "  observe deadline: %s\n", d.GetObserveDeadline())
					}
					if d.GetPreDeployTaskId() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "  pre-deploy task: %s\n", d.GetPreDeployTaskId())
					}
					if d.GetError() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "  error: %s\n", d.GetError())
					}
				})
			})
		},
	}
}

// deploymentShortID 是部署行 ID 的短形（"-d<seq>" 尾段，ADR-0092 派生律）：
// 同一 App 的行集 App 前缀恒同，尾段即行锚（staging d3-d8 连环失败的排查
// 读法）。非派生形态（无连字符）原样返回。
func deploymentShortID(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// truncateCell 把人读表单元格截到 max rune（末尾 "..."）：list 的 ERROR 列
// ~80 字符——多行并读时长正文压行宽，全文走 apps deployment get。
func truncateCell(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
