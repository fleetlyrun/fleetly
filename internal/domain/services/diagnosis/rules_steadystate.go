// rules_steadystate.go 是稳态面规则。stopped 不是故障：看门狗语义解释化
// （设计 §2.5）——把「平台为什么不动它」翻译给操作者（声明态如此，收敛环
// 不会自作主张拉起），advice 档。
package diagnosis

import "github.com/fleetlyrun/fleetly/internal/domain/shared/capability"

// ruleStopped：观测 stopped 即产 advice 条目（看门狗语义解释化：stopped
// 是声明的期望态/操作者停止的既成态，平台保持它——要跑起来改声明）。
// Fix 预填 scale replicas=1（§8.10 一等动词：声明化启动的直达动作）。
var ruleStopped = Rule{
	ID: "steady-state-stopped",
	Check: func(facts Facts) []Finding {
		obs, ok := facts.observations()
		if !ok {
			return nil
		}
		var out []Finding
		for _, o := range obs {
			if o.State != capability.WorkloadStopped {
				continue
			}
			params := map[string]string{"state": string(o.State)}
			if code := exitCodeString(o.ExitCode); code != "" {
				params["exit_code"] = code
			}
			// 143 = SIGTERM：平台停摆/操作者停止的惯常收尾面，解释化透出。
			if meaning := ExitCodeMeaning(derefInt(o.ExitCode)); meaning != "" {
				params["exit_code_meaning"] = meaning
			}
			if !o.LastExitAt.IsZero() {
				params["last_exit_at"] = o.LastExitAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			out = append(out, instantiate("F_WORKLOAD_STOPPED_EXPLAINED", o,
				map[string]string{"process": o.Process}, params))
		}
		return out
	},
}
