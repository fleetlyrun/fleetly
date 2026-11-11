package api

// Admission 判定附注（真源自 engine 根包迁出，P3-7 输入面契约收口）。

// AdmissionOutcome 是 admission 判定形态（P10 响应显式化；取值与 proto
// Admission.outcome 一致，kebab-case 用户可见）。
type AdmissionOutcome string

const (
	// AdmissionQueued：新受理，无合并无抢占。
	AdmissionQueued AdmissionOutcome = "queued"
	// AdmissionMerged：latest-wins——既有 queued 请求被本请求合并取代。
	AdmissionMerged AdmissionOutcome = "merged"
	// AdmissionSuperseded：显式 supersede 抢占在途（同批合并的 queued 不另
	// 报——显式动作优先归因）。
	AdmissionSuperseded AdmissionOutcome = "superseded"
	// AdmissionDeduplicated：同幂等键或同 commit 命中活跃既有部署。
	AdmissionDeduplicated AdmissionOutcome = "deduplicated"
)

// Admission 是 Submit 的判定附注（ADR-0016 语义的响应面）：调用方可判定
// "去重命中/排队第几位"。幂等层命中（ADR-0024）在 api 层逐字节重放原始
// 响应、不产生 Admission——幂等层与 admission 去重由此分立。
type Admission struct {
	Outcome  AdmissionOutcome
	Position int    // per-App 串行管线位次（1 = 队头/下一执行）
	Existing string // deduplicated 时命中的既有部署 ID
}
