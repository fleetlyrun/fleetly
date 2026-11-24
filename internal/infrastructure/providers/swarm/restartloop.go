package swarm

// App 域崩溃循环的观测装配（2026-11-24，缺陷②）。
//
// 为什么需要：taskEvent 对 App 域只取「活槽位」task（DesiredState=running）
// ——崩溃后被重启策略替换的旧 task 是 desired=shutdown，被当历史任务滤出
// 观测。于是崩溃循环中的 App 工作负载在观测面上恒为 pending / running 的
// 交替：workload.container_exited 一条都不入账（RestartCount 无从派生，
// crash-loop 诊断与告警在真 swarm 上不触发），L2 看门狗没有 degraded 可咬
// ——部署只能等 L1 超时（干净 dind 实测：150 秒内 container_exited 恒为 0，
// 部署约 120 秒后以「health gate L1 timed out」收场）。k3s 面按
// CrashLoopBackOff → Degraded + RestartCount 上报（watchdogBite 的咬合条件
// 正是「degraded 且 RestartCount > 0」）；本文件让 swarm 面上报同一形状。
//
// 口径：同一 Workload、同一代次下有被替换的失败 task，即把最近一次失败的
// ExitCode / Reason / Message / Instance 叠写到聚合观测上，RestartCount =
// 被替换的失败 task 数——非零退出码即终态事实，逐次失败各入账一次（去抖
// 签名的实例段随最近一次失败的 task ID 变化，重复轮询零重复）。失败数达到
// restartLoopThreshold 才把状态改写为 Degraded（L2 看门狗的咬合条件是
// 「degraded 且 RestartCount > 0」——首次失败不咬，单次崩溃后恢复的形态不
// 误报；k3s 面 CrashLoopBackOff 同样在退避开始、即第二次失败之后才出现）。
// 活槽位已 running 则不改写：在服事实优先。
//
// 第一版把「附带失败信息」的门槛也设在 2，真 dind 实测露出缺陷：首次失败
// 永不入账，事实数 = 失败数 − 1。

import (
	"math"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// restartLoopThreshold 是同 Workload 同代次下被替换的失败 task 达到多少个
// 才按崩溃循环上报。取 2：单次失败后的立即重启不计循环（k3s 面
// CrashLoopBackOff 同样在退避开始、即第二次失败之后才出现）。
const restartLoopThreshold = 2

// replacedExitEvent 由一个已被替换的异常终态 task 构造「失败事实」观测
// （纯函数）。ok=false 表示不是 App 域的被替换失败 task：活槽位（归
// taskEvent）、非异常终态（complete = 退出码 0 的正常完成）、Task 域、非
// 受管载体。at 是该终态的发生时刻（最近一次失败的时序依据）。
func replacedExitEvent(t swarm.Task, labels map[string]string) (ev capability.WorkloadEvent, at time.Time, ok bool) {
	if labels[labelManaged] != "true" || labels[labelTask] != "" {
		return capability.WorkloadEvent{}, time.Time{}, false
	}
	if t.DesiredState == swarm.TaskStateRunning {
		return capability.WorkloadEvent{}, time.Time{}, false
	}
	if t.Status.State != swarm.TaskStateFailed && t.Status.State != swarm.TaskStateRejected {
		return capability.WorkloadEvent{}, time.Time{}, false
	}
	gen, _ := strconv.ParseUint(labels[labelGeneration], 10, 64)
	ev = capability.WorkloadEvent{
		WorkloadID: labels[labelWorkload],
		Generation: capability.Generation(gen),
		State:      taskEventState(t.Status.State),
		Node:       labels[labelNodeID],
		Message:    taskStatusMessage(t),
		Reason:     t.Status.Err,
		Instance:   t.ID,
	}
	// rejected 等未建容器的终态 ContainerStatus 为 nil——指针解引用会击穿
	// Watch goroutine；nil 时退出码保持缺失（taskEvent 同款防御）。
	if cs := t.Status.ContainerStatus; cs != nil {
		code := cs.ExitCode
		ev.ExitCode = &code
	}
	if ev.WorkloadID == "" {
		return capability.WorkloadEvent{}, time.Time{}, false
	}
	return ev, t.Status.Timestamp, true
}

// replacedExit 是一个被替换的异常终态 task 的观测与其发生时刻（pollTasks
// 的收集序无契约——「最近一次失败」按时刻取）。
type replacedExit struct {
	ev capability.WorkloadEvent
	at time.Time
}

// overlayRestartLoops 把崩溃循环叠写到逐 workload 的聚合观测上（纯函数）。
func overlayRestartLoops(aggregated []capability.WorkloadEvent, replaced []replacedExit) []capability.WorkloadEvent {
	if len(replaced) == 0 {
		return aggregated
	}
	out := aggregated
	for i := range out {
		cur := &out[i]
		if cur.State == capability.WorkloadRunning {
			continue // 活槽位健康：不拿历史失败盖掉在服事实
		}
		// 同 Workload 同代次的被替换失败 task（代次圈定与聚合子集同口径：
		// 旧代滚动替换留下的失败不是本代的崩溃循环）：一遍扫出计数与
		// 「最近一次」（收集序无契约，按终态时刻取）。
		count := 0
		var latest replacedExit
		for _, r := range replaced {
			if r.ev.WorkloadID != cur.WorkloadID || r.ev.Generation != cur.Generation {
				continue
			}
			if count == 0 || r.at.After(latest.at) {
				latest = r
			}
			count++
		}
		if count == 0 {
			continue
		}
		cur.RestartCount = restartCountOf(count)
		cur.ExitCode = latest.ev.ExitCode
		cur.Reason = latest.ev.Reason
		cur.Message = latest.ev.Message
		cur.Instance = latest.ev.Instance
		if count >= restartLoopThreshold {
			cur.State = capability.WorkloadDegraded
		}
	}
	return out
}

// restartCountOf 把被替换的失败 task 数转成观测面的 int32。真实计数受
// daemon 的任务历史上限约束（个位数到几十），越界钳到 MaxInt32 而不是回绕。
func restartCountOf(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n) //nolint:gosec // G115：上一行已按 MaxInt32 钳制，转换不溢出
}
