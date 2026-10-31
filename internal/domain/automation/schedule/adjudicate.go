package schedule

// 重叠策略与到期判定（P3a 批第 6 单元，ADR-0087 决策 1/2/7：Schedule 重叠
// 裁决与到期窗口归属自 application/engine/schedule.go 下沉聚合域包；
// 留驻 engine 的执行面：scheduleStep 驱动环、repo 读写〔Run 台账〕、铸
// Task〔minttask 链〕、skip/fired 事件发射、Kick——形态 = 装配〔读上一拍
// Task 的未终态 Run 台账〕→ 域裁决〔本文件〕→ 执行〔Fire CAS/铸 Task/
// 事件〕。纯度铁律（ADR-0087 决策 2 + domain 三红线）：零 I/O、时间作参数
// 传入（禁 time.Now——domainpurity 纪律项）、不写 receiver、不 import
// genproto。

import (
	"time"
)

// ---- 到期判定：窗口归属的纯部分（ADR-0018 §A）----

// Due 报告本行是否已到拍窗口（next_fire_at ≤ now）。cron 解释（ParseCron）
// 已在域，此处只做 next_fire_at 绝对时刻的窗口归属：空时刻 = 不触发；时间
// 面异常 = 未到期（创建面校验后的不可达防御——坏行诚实跳过等下一拍，不
// 误拍；墙钟口径与 runs.deadline 一致，不做 SQL 字符串比较）。相等边界 =
// 到期（错过窗口补跑一拍依赖该边界起拍）。时间作参数：不写 receiver，到
// 期后的拍/跳/推进由 engine 执行。
func (s *Schedule) Due(now time.Time) bool {
	if s.NextFireAt == "" {
		return false
	}
	due, err := time.Parse(time.RFC3339, s.NextFireAt)
	if err != nil {
		return false
	}
	return !now.Before(due)
}

// ---- 重叠策略面（ADR-0018 A.3 修订 / ADR-0017 附录 A.4）----

// 重叠策略档位（字面单源在本包——engine 侧常量是配置键词汇面，
// ParseScheduleOverlap 归一 + fill 缺省）。queue/等待第三档仍不预支
// （torchwood 规模实证后再议）。
const (
	// OverlapSkip：上一拍铸出的 Task 仍有未终态 Run 时跳过本拍
	//（schedule.skipped reason=overlap、next_fire_at 照常推进）——最少
	// 惊异默认。
	OverlapSkip = "skip"
	// OverlapFire：重叠时照常拍，允许并行拍（torchwood 类并行派发的实证
	// 形态）。
	OverlapFire = "fire"
)

// Overlap 是重叠事实（engine 装配：读上一拍 Task 的未终态 Run 台账——Run
// 状态是真源，Task 行的终态镜像随 taskStep 有拍延迟，以 Run 为准）。
type Overlap struct {
	// LastTaskID 是上一拍铸出的 Task（空 = 从未拍过 → 无重叠）。
	LastTaskID string
	// DrivingRuns 是上一拍 Task 的未终态 Run 计数（pending/running/
	// stopping）。
	DrivingRuns int
}

// Overlapping 报告上一拍是否仍未收口（重叠锚）。手动触发面（RunNow 语义）
// 不问档位——重叠事实即诚实拒绝（先停上一拍或等其收口；旋钮只改到期拍，
// ADR-0017 附录 A.4）。
func (o Overlap) Overlapping() bool { return o.LastTaskID != "" && o.DrivingRuns > 0 }

// SkipDueFire 裁决一次到期拍在重叠事实 × 档位下的处置：
//
//	重叠 × fire 档 → 照常拍（允许并行拍）
//	重叠 × 其余档（skip——空/未登记值同 skip：值域执法在 engine
//	      ParseScheduleOverlap fail-fast，域侧忠实承载「非 fire 即跳」）
//	无重叠 → 照常拍
//
// 纯函数：跳拍的 Fire CAS 推进与 schedule.skipped 事件、拍面的铸 Task 由
// engine 执行。
func (o Overlap) SkipDueFire(policy string) bool {
	return o.Overlapping() && policy != OverlapFire
}
