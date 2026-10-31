package alertrule

// 评估决策面（P3a 首单元，ADR-0087 决策 1/3/7：裁决规则归域、状态机搬家
// 禁令——迁移表与裁决下沉聚合域包，行写执行〔Observe/BeginPending/
// ClearPending/Transition + 事件 + 通道派发〕留 engine）。纯判定：给定
// 规则行态与一拍读数，裁决观测落点、pending 窗操作与迁移目标。零 I/O；
// 时间一律作参数传入（禁 time.Now——domainpurity 纪律项，ADR-0087 验收锚）。

import (
	"time"
)

// Verdict 是一拍评估的裁决（迁移表输出）。全零 = 零动作（无样本拍不动窗、
// 稳态采样拍无迁移沿）。不变量：BeginPending 与 ClearPending/Transition
// 互斥——起窗拍无其他动作（窗满迁移发生在后续拍）。
type Verdict struct {
	// Observe = 落最近评估观测（值 + 时刻；不迁态）。仅采样拍为真：无样本
	// 拍不评估**不动窗**（CPU 差分首拍恒无率；回落清窗只认显式低值——修复
	// 批 F3，与「重启续窗」〔ADR-0090 决策 4〕咬合）。
	Observe bool
	// BeginPending 非零 = 落起窗时刻（越限首见：行缺失/坏值即首见，起窗
	// 覆写即自愈）。该时刻即持续窗的持久锚——engine 重启/规则装载从行
	// 恢复续窗，不重置。
	BeginPending time.Time
	// ClearPending = 回落归位清 pending 窗（幂等——无窗行清写无效果）。
	ClearPending bool
	// Transition 非空 = 迁移目标态（StateFiring | StateOK）；空 = 不迁移。
	// 迁移即离窗：pending_since 随迁移清列由 repo Transition 承载。
	Transition string
}

// Evaluate 裁决一拍评估（迁移表全行；fired/resolved 迁移沿各一次，
// ADR-0041）：
//
//	无样本                 → 零动作（不评估不动窗）
//	越限 × 行无起窗点       → Observe + 起窗（首见；坏值同首见，不迁移）
//	越限 × 窗未满          → Observe（窗内采样推进不重置起算点）
//	越限 × 窗满 × 非 firing → Observe + 迁移 firing
//	越限 × 窗满 × firing    → Observe（迁移沿语义：不逐拍重发）
//	回落 × 行带窗           → Observe + 清窗
//	回落 × firing           → Observe + 迁移 ok（可与清窗同拍）
//	回落 × 非 firing 无窗   → Observe
//
// 窗满判定 elapsed ≥ ForSecs（for 持续窗按采样序近似，ADR-0041 诚实边界；
// 时钟回拨按未满处理）。纯函数：不写 receiver，行写与事件由调用方执行。
func (r *Rule) Evaluate(value float64, sampled bool, now time.Time) Verdict {
	if !sampled {
		return Verdict{}
	}
	v := Verdict{Observe: true}
	if value > r.Threshold {
		since := parsePendingSince(r.PendingSince)
		if since == nil {
			v.BeginPending = now // 越限首见：起窗（本拍不迁移）
			return v
		}
		if now.Sub(*since) < time.Duration(r.ForSecs)*time.Second {
			return v // 持续窗未满（窗内采样推进不重置）
		}
		if r.State != StateFiring {
			v.Transition = StateFiring
		}
		return v
	}
	if r.PendingSince != "" {
		v.ClearPending = true // 回落归位：清窗（清窗只认显式低值）
	}
	if r.State == StateFiring {
		v.Transition = StateOK
	}
	return v
}

// parsePendingSince 解析落库起窗时刻（空串/坏值 = nil——坏值按首见处理，
// 起窗覆写即自愈；与 engine 侧 parseDeadline 同口径的域内单份）。
func parsePendingSince(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
