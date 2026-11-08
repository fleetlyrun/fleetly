// rules_deployment.go 是部署面规则（立案③残留缺口批：torchwood 走查实录
// ——d1 failed 时错误正文在 deployments 行，观测/事件面无源，apps diagnose
// 报 no findings）。边界：本规则不重复承载部署失败全文（deployment error
// 与 build log 仍是错误全文的承载面）——只陈述「App 最新部署 failed」事实、
// 透出错误首行、并把修复动作指路到部署行详情；不解析错误文本归因
// （FaultClass 取 declaration 的论证见条目注册表 Source 锚）。App 级锚定：
// 部署失败发生在载体投影之前，观测/事件面可能整面无源，DB 权威部署行是
// 唯一事实源——这正是本规则的存在理由。
package diagnosis

import (
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
)

// ruleLatestDeploymentFailed：App 最新部署行 state=failed 且带 error →
// 条目。幂等由读时计算结构承载：事实面是单行快照，同部署 id 的重复诊断
// 恒产同一条目（无累积、无去重窗——与 workload 规则族同构）。非 failed
// （succeeded/cancelled/superseded/在途态）一律沉默：零噪声（succeeded 的
// App「没有可解释的坏」，不产条目）。
var ruleLatestDeploymentFailed = Rule{
	ID: "latest-deployment-failed",
	Check: func(facts Facts) []Finding {
		d, ok := facts.latestDeployment()
		if !ok {
			return nil // 部署面读不了（None）或无部署行（ID 空）：读不了 ≠ 有失败，沉默
		}
		if d.State != deployment.StateFailed || d.Error == "" {
			return nil // 最新行非 failed（含在途/回滚中）或行无错误正文：不是本规则的输入
		}
		params := map[string]string{
			"deployment": d.ID,
			"state":      string(d.State),
			"error":      errorFirstLine(d.Error),
		}
		if d.Generation != 0 {
			params["generation"] = strconv.FormatUint(d.Generation, 10)
		}
		if d.FinishedAt != "" {
			params["finished_at"] = d.FinishedAt
		}
		// Fix 预填部署行 id：动线 = apps deployment <id> 看错误全文（常见前置
		// 清单指引见注册表 Summary——镜像引用/卷附件/Secret/变量是部署期失败
		// 的大宗面）。
		out := instantiateAppAnchored("F_DEPLOYMENT_LATEST_FAILED",
			map[string]string{"deployment": d.ID}, params,
			Evidence{Kind: EvidenceDeployment, Ref: d.ID})
		return []Finding{out}
	},
}

// errorFirstLine 取错误正文首行（多行日志形态只携首行进 Params——全文
// 留在部署行，条目不转写放大）。
func errorFirstLine(errText string) string {
	if head, _, found := strings.Cut(errText, "\n"); found {
		return head
	}
	return errText
}
