package run

// 观测裁决表（P3a 批第 2 单元，ADR-0087 决策 1/3/7：裁决规则归域、状态机
// 搬家禁令——迁移表下沉聚合域包，行写执行〔transitRunFour 四件一 + 事件
// 发射〕留 engine）。切分契约（提案 §4 P3a 表）：裁决表下沉，eventcode
// 发射留 engine 归 emitter——域只裁决不发事件；观测缓存刷新与终态回收、
// Kick 同留 engine（装配与执行）。纯判定：零 I/O、不写 receiver；本表无
// 时间参与（TTL 与停止收口兜底在驱动环 janitor，不在观测沿）。

import (
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// Verdict 是一条 Run 观测的裁决（观测裁决表输出）。零值 = 零动作（组合未
// 命中表：行已终态、观测态不在表内、或活跃观测 × 收口态行）。
type Verdict struct {
	// Transit = 执行状态迁移（表命中的唯一动作形态）。
	Transit bool
	// From 是 CAS from-state 集（并发防御纵深：与行写竞态退化为
	// ErrConflict，不误写）。
	From []State
	// To 是迁移目标态。
	To State
	// StopReason 是终态起因的表值写（completed/failed/platform_drained）；
	// 空串 = 起因保留——stopping 起因已由停止请求（stopRunRow）写在行上，
	// 观测确认即终态，不覆写。
	StopReason string
	// ExitCode 是终态退出码：观测值原样携带（nil = 未观测照写 nil——成败
	// 归类是观测状态的事，退出码只记账）。
	ExitCode *int
}

// Mutation 返回裁决的行写回调（engine 交 Transit 的 mut 参数）。终态裁决
// 写终列：起因非空才写（stopping 起因保留），退出码恒写（观测值原样）；
// 非迁移/非终态裁决返回 nil——pending→running 迁移不触终列。纯列值写，
// 零 I/O。
func (v Verdict) Mutation() func(*Run) {
	if !v.Transit || !v.To.Terminal() {
		return nil
	}
	return func(r *Run) {
		if v.StopReason != "" {
			r.StopReason = v.StopReason
		}
		r.ExitCode = v.ExitCode
	}
}

// Adjudicate 裁决一条 Run 观测（终态观测与 Run 行态的对账表，F1.5/F1.6；
// 自 engine taskobs.go 下沉，ADR-0087 决策 7）：
//
//	观测 running        + 行 pending            → running
//	观测 completed      + 行 pending/running    → stopped/completed（退出码 0）
//	观测 failed         + 行 pending/running    → failed/failed（退出码非 0）
//	观测 stopped        + 行 stopping           → stopped/<stopping 起因>
//	观测 stopped        + 行 pending/running    → stopped/platform_drained
//	（载体被平台侧移除：节点排空/人工拆载体——诚实归类，不伪装成败）
//
// 未列组合零动作：running 观测只对 pending 行起效（重复 running 不迁移、
// 收口中的行不回 running）；completed/failed/stopped 对 stopping 行不起效
// （停止收口的唯一确认沿是 stopped 观测、兜底是收口截止——收口期的自然
// 完成/失败不劫持归类）；行已终态零动作（迟到观测不重开状态机——engine
// 对终态行先行回收）。表注的退出码语义（completed = 0 / failed = 非 0）是
// 观测状态的归类口径（capability.WorkloadState），本表对退出码只作原样
// 记账、不复核。纯函数：不写 receiver，行写与事件由调用方执行。
func (r *Run) Adjudicate(ev capability.WorkloadEvent) Verdict {
	switch {
	case ev.State == capability.WorkloadRunning && r.State == StatePending:
		return Verdict{Transit: true, From: []State{StatePending}, To: StateRunning}
	case ev.State == capability.WorkloadCompleted && r.State.Active():
		return Verdict{Transit: true, From: ActiveStates, To: StateStopped,
			StopReason: ReasonCompleted, ExitCode: ev.ExitCode}
	case ev.State == capability.WorkloadFailed && r.State.Active():
		return Verdict{Transit: true, From: ActiveStates, To: StateFailed,
			StopReason: ReasonFailed, ExitCode: ev.ExitCode}
	case ev.State == capability.WorkloadStopped && r.State == StateStopping:
		// stopping 起因已在行上（stopRunRow 写入）；观测确认即终态。
		return Verdict{Transit: true, From: []State{StateStopping}, To: StateStopped,
			ExitCode: ev.ExitCode}
	case ev.State == capability.WorkloadStopped && r.State.Active():
		return Verdict{Transit: true, From: ActiveStates, To: StateStopped,
			StopReason: ReasonPlatformDrained, ExitCode: ev.ExitCode}
	}
	return Verdict{}
}
