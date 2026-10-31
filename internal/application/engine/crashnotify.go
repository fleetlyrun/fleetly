package engine

// crash-loop 通知投递（B2a W2，zeabur 借鉴设计 §2.1 已裁投递两类，本批落
// crash-loop 类；Platform Incident 类随 B2b 事故层）：F_WORKLOAD_CRASH_LOOP
// 命中且**迁移沿**才通知——进入/退出各一次，防重复推送（沿 Alert Rule 的
// transition 沿通知纪律，alerting.go transitionRule 同款）。
//
// 沿记录选型（最小诚实形态）：**事件账本沿 marker**——进入/退出各落一条
// workload.crash_loop_entered / cleared 事件（eventcode 三链入册），沿的
// 开合以「该 workload 聚合账本里最后一条 marker」判定（重启安全、受 Event
// 保留窗约束——与终态计数纯派生、alert pending 落行同哲学：事实持久化，
// 派生不留状态；不建内存沿 map，不加沿列迁移）。marker 先入账、后投递：
// 投递失败不丢沿（下一次评估不会因投递失败重轰）。
//
// 评估点（诊断评估点，事件驱动）：Watch 消费处 handleObservation——
// 容器终态观测（新 exit 事实入账后）评估进入沿；脱离失败/失联态的观测
// （prev 失败/失联或未知——重启冷窗）评估退出沿。稳态 running 的重复
// 轮询零查询（prev 门）；退出沿带滞回（窗内计数回落才清沿——重启间隙
// 的健康观测不清沿，防 entered/cleared 振荡刷屏），计数未回落时的收口
// 由下一个终态观测在进入沿侧承担（!inLoop && open 分支）。
//
// App 级其余 Finding 不投递（已裁投递两类——本文件是唯一 Finding 派发
// 点，结构上只触 crash-loop 面；测试断言 OOM 单发不投）。

import (
	"context"
	"encoding/json"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// 事件名（eventcode 注册表同源；usage 反扫的字面量锚点）。
const (
	eventCrashLoopEntered = "workload.crash_loop_entered"
	eventCrashLoopCleared = "workload.crash_loop_cleared"
)

// crashLoopScanLimit 是沿 marker 扫描的账本行上界（ListByAggregate 钳制
// 内）：marker 取「最后一条」，常态下 entered 落在第 3 次退出附近、行集
// 极浅；超热循环（窗内千次退出）把更早的 cleared 挤出扫描窗是诚实边界
// （最坏形态 = 多投一次进入，方向安全）。
const crashLoopScanLimit = 1000

// crashLoopEventPayload 是沿 marker 事件的载荷（字段只增；与
// container_exited 载荷同族——workload/app/process 归属 + 计数/终态快照）。
type crashLoopEventPayload struct {
	WorkloadID   string `json:"workload_id"`
	AppID        string `json:"app_id,omitempty"`
	Process      string `json:"process,omitempty"`
	Generation   uint64 `json:"generation"`
	RestartCount int    `json:"restart_count"`
	State        string `json:"state"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// evaluateCrashLoopNotify 是诊断评估点（handleObservation 消费处调用）：
// 只评 App 域载体（诊断/通知域是 App 面——与 container_exited 事实同口径；
// Run/Database/受管域载体不进）。ev 是本次观测，prev/hadPrev 是观测缓存
// 更新前的同载体上一观测（退出沿的迁移判定锚）。
func (e *Engine) evaluateCrashLoopNotify(ctx context.Context, ev capability.WorkloadEvent, prev capability.WorkloadEvent, hadPrev bool) {
	owner, owned := e.obs.ownerOf(ev.WorkloadID)
	if !owned || owner.domain != ownerApp {
		return
	}
	if containerExitFact(ev) {
		e.crashLoopEntryScan(ctx, ev, owner)
		return
	}
	if failedOrDegraded(ev.State) {
		return // 仍在失败/失联态（无新终态事实）：沿不变
	}
	// 脱离失败/失联态的观测：prev 失败/失联（或未知——重启冷窗后首观测，
	// 有界：每载体重启后一次）才查退出沿；稳态 running 重复轮询零查询。
	if hadPrev && !failedOrDegraded(prev.State) {
		return
	}
	e.crashLoopExitScan(ctx, ev, owner)
}

// crashLoopEntryScan 在新终态事实入账后评估：计数纯派生（账本近窗）×
// 最近终止态仍在（与 diagnosis.ruleCrashLoop 同判据——count ≥ 阈值且
// failed/degraded；域不导出内部判定，此处按同口径复写并锚注释）。
// 进入沿开（无 marker 或最后 marker 是 cleared）才投；窗内计数回落而沿
// 仍开时收退出沿（循环去升级——Alert Rule 回归属位的同族语义）。
func (e *Engine) crashLoopEntryScan(ctx context.Context, ev capability.WorkloadEvent, owner workloadOwner) {
	rows, err := e.repos.Outbox.ListByAggregate(ctx, "", "workload", ev.WorkloadID, 0, crashLoopScanLimit)
	if err != nil {
		e.log.Error("crash-loop notify: scan ledger", "workload", ev.WorkloadID, "err", err)
		return
	}
	count := DeriveTerminalCounts(ContainerExitedFacts(rows), DiagnoseWindow, e.clock.Now())[ev.WorkloadID]
	inLoop := count >= diagnosis.CrashLoopThreshold && failedOrDegraded(ev.State)
	open := crashLoopMarkerOpen(rows)
	switch {
	case inLoop && !open:
		e.crashLoopTransition(ctx, ev, owner, count, eventCrashLoopEntered)
	case !inLoop && open:
		e.crashLoopTransition(ctx, ev, owner, count, eventCrashLoopCleared)
	}
}

// crashLoopExitScan 在脱离失败/失联态的观测上评估退出沿：沿开且**滞回
// 达成**才投——窗内计数已回落（count < 阈值，与进入沿共用同一计数面）。
// 滞回（修复批 F3）：重启间隙的健康观测（pending/running）不代表循环
// 结束，任意非 failed/degraded 观测即投 cleared 会让下轮崩溃 re-enter，
// 振荡刷屏；只有计数面回落（窗内证据老化/不足）才允许收沿。计数未回落
// 的真恢复沿由下一个终态观测在 entryScan 收口（!inLoop && open 分支）。
func (e *Engine) crashLoopExitScan(ctx context.Context, ev capability.WorkloadEvent, owner workloadOwner) {
	rows, err := e.repos.Outbox.ListByAggregate(ctx, "", "workload", ev.WorkloadID, 0, crashLoopScanLimit)
	if err != nil {
		e.log.Error("crash-loop notify: scan ledger", "workload", ev.WorkloadID, "err", err)
		return
	}
	if !crashLoopMarkerOpen(rows) {
		return
	}
	count := DeriveTerminalCounts(ContainerExitedFacts(rows), DiagnoseWindow, e.clock.Now())[ev.WorkloadID]
	if count >= diagnosis.CrashLoopThreshold {
		return // 滞回未达成：窗内计数仍在阈值上，重启间隙的健康观测不清沿
	}
	e.crashLoopTransition(ctx, ev, owner, count, eventCrashLoopCleared)
}

// crashLoopTransition 落沿 marker + 派发（进入/退出同一载荷形态，沿
// transitionRule 的 fired/resolved 同构）。marker 先入账：沿纪律以账本为
// 锚，投递必须后于沿入账（投递失败不丢沿——账本已开/已合，下一次评估
// 不重轰）。派发在 wg 跟踪的 goroutine——通道 HTTP（每通道 10s 超时）不得
// 阻塞 Watch 消费环；Stop 排水有界（dispatchNotification 循环全部通道）。
func (e *Engine) crashLoopTransition(ctx context.Context, ev capability.WorkloadEvent, owner workloadOwner, restartCount int, marker string) {
	process, _ := e.WorkloadProcess(ev.WorkloadID)
	payload, _ := json.Marshal(crashLoopEventPayload{
		WorkloadID: ev.WorkloadID, AppID: owner.id, Process: process,
		Generation: uint64(ev.Generation), RestartCount: restartCount,
		State: string(ev.State), ExitCode: ev.ExitCode, Reason: ev.Reason,
	})
	if err := e.emitObservation(ctx, e.ownerTeam(ctx, owner), marker, "workload", ev.WorkloadID, payload); err != nil {
		e.log.Error("crash-loop notify: emit marker", "workload", ev.WorkloadID, "marker", marker, "err", err)
		return
	}
	entered := marker == eventCrashLoopEntered
	loopState := "cleared"
	if entered {
		loopState = "entered"
	}
	e.dispatchFindingAsync(findingNotification{
		Type: "finding", FindingID: "F_WORKLOAD_CRASH_LOOP", AppID: owner.id,
		WorkloadID: ev.WorkloadID, Process: process,
		RestartCount: restartCount, State: loopState, At: state.FormatTime(e.clock.Now()),
	})
	e.log.Info("crash loop state transitioned",
		"workload", ev.WorkloadID, "app", owner.id, "to", loopState, "restart_count", restartCount)
}

// dispatchFindingAsync 在 wg 跟踪的 goroutine 派发 Finding 类通知（投递
// 两类裁决的 crash-loop 类；复用 Notification Channel 全通道面）。根 ctx
// 取 buildRootCtx 同款（进程生命期 runCtx；未 Start 的手动驱动形态退化为
// Background——测试直驱路径可投递，Stop 后取消）。
func (e *Engine) dispatchFindingAsync(n findingNotification) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(e.buildRootCtx(), 30*time.Second)
		defer cancel()
		e.dispatchNotification(ctx, n)
	}()
}

// crashLoopMarkerOpen 报告沿 marker 是否开：该 workload 聚合账本里最后一
// 条 marker 是 entered 即开（cleared 收口；无 marker = 未开）。行集升序，
// 末条 marker 胜。
func crashLoopMarkerOpen(rows []outbox.Event) bool {
	open := false
	for _, r := range rows {
		switch r.Name {
		case eventCrashLoopEntered:
			open = true
		case eventCrashLoopCleared:
			open = false
		}
	}
	return open
}

// failedOrDegraded 与 diagnosis 域 stillTerminated 同口径（failed|degraded
// ——最近终止态仍在；域不导出内部判定，此处按同判据复写，改判据须同批
// 两侧同步）。
func failedOrDegraded(s capability.WorkloadState) bool {
	return s == capability.WorkloadFailed || s == capability.WorkloadDegraded
}
