// rewrite.go 是 platform 改写接口（设计 §2.3 产品原则：platform 的错永远
// 不读成用户的错）。Platform Incident 能解释的 Finding，其生效 FaultClass
// 改写为 platform 且 evidence 链回事故条目 id（zeabur link() 同型：改写为
// 事故自己的归属）。B2b 接入真事故装配；本批留纯函数 + 单测。
package diagnosis

import "github.com/fleetlyrun/fleetly/internal/domain/shared/model/finding"

// Incident 是 Platform Incident 面向改写的纯值投影：id + 波及 workload 集
// （B2b 由组件健康/engine health/managed 状态/Route 归属派生装配）。
type Incident struct {
	// ID 是 Platform Incident 条目 id（evidence 回链锚）。
	ID string
	// AffectedWorkloadIDs 是该事故波及的 workload 面（workload 维度匹配；
	// App 级/Route 级波及由 B2b 装配面展开到 workload 后进入）。
	AffectedWorkloadIDs []string
}

// RewriteByIncident 把事故可解释的条目改写为 platform 归属并链回事故 id。
// 纯函数：不修改入参切片（逐值拷贝改写），未命中条目原样返回；同一
// workload 命中多事故时以最先入参的事故为锚（装配面保证波及面互斥或有序）。
func RewriteByIncident(findings []Finding, incidents []Incident) []Finding {
	if len(incidents) == 0 {
		return findings
	}
	affected := map[string]string{} // workload id → incident id（首个事故优先）
	for _, inc := range incidents {
		for _, wid := range inc.AffectedWorkloadIDs {
			if _, dup := affected[wid]; !dup {
				affected[wid] = inc.ID
			}
		}
	}
	out := make([]Finding, len(findings))
	for i, f := range findings {
		if incID, hit := affected[f.WorkloadID]; hit {
			f.FaultClass = finding.FaultClassPlatform
			f.Evidence = append(f.Evidence, Evidence{Kind: EvidenceIncident, Ref: incID})
		}
		out[i] = f
	}
	return out
}
