package engine

// 诊断 facts 装配器（B2a W2，zeabur 借鉴设计 §2.4——「facts 不是现成而是
// 经 Engine 导出面装配」）：DiagnoseFacts 把四事实源装配为 diagnosis.Facts
// 供规则层读时计算——观测快照（ObservationSnapshot 活状态导出面 +
// WorkloadProcess 窄导出）、近窗事件（outbox (team, aggregate,
// aggregate_id) 索引查询 + ContainerExitedFacts/DeriveTerminalCounts 纯
// 派生）、告警态（alerting 行读面 firing+pending，W1b 已持久化）、组件
// 健康（本期恒 None——B2b 才有共享导出面，Option 三态正是为此设计）。
//
// 零旁路纪律（guards/diagnosbypass_test.go 执法）：本文件只准经导出面
// （ObservationSnapshot/WorkloadProcess）与 repo 端口（Outbox/AlertRules）
// 取数，禁触观测域内部符号（e.obs/未导出观测访问器）——obs 私有缓存的
// 唯一合法出口是导出面，旁路会让快照语义（副本、归属圈定、字典序）无人
// 保鲜。单一事实源读不了时该面落 None（读不了 ≠ 不存在，规则对 None
// 沉默），UnreadableFaces 透出供诚实展示。

import (
	"context"
	"sort"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// DiagnoseWindow 是诊断近窗口径：终态计数与事件面共用同一窗（崩溃循环
// 阈值 diagnosis.CrashLoopThreshold 在窗内计数——规则阈值与装配窗口分离，
// 窗口是装配面参数而非域知识）。
const DiagnoseWindow = 10 * time.Minute

// DiagnoseFacts 装配某 App 的诊断事实快照（读时计算面）。无失败返回：
// 某事实源读不了 → 该面 None（诚实三态），不影响其余面。
func (e *Engine) DiagnoseFacts(ctx context.Context, appID string) diagnosis.Facts {
	now := e.clock.Now()
	facts := diagnosis.Facts{Now: now}

	// 观测面（导出面快照——活状态 State/ExitCode/Reason，App 域归属圈定、
	// workloadID 字典序稳定）：逐载体投影为 WorkloadObservationFact；
	// RestartCount/LastExitAt 由事件账本纯派生（无独立计数器状态）。账本面
	// 是 App 级一次往返（修复批 F4 扇出收口）：快照 workload 集一次
	// ListByAggregateIDs 分组返回——逐 workload ListByAggregate 会在「需要
	// 你」页放大为页级 N×M 次查询。
	snap := e.ObservationSnapshot(ctx, appID)
	ids := make([]string, 0, len(snap))
	for _, ev := range snap {
		ids = append(ids, ev.WorkloadID)
	}
	rowsByWorkload, rerr := e.diagnosisEventRows(ctx, ids)
	obs := make([]diagnosis.WorkloadObservationFact, 0, len(snap))
	var events []diagnosis.EventFact
	eventsReadable := rerr == nil
	for _, ev := range snap {
		process, _ := e.WorkloadProcess(ev.WorkloadID)
		if !eventsReadable {
			// 事件面读不了：Events 落 None（面向诚实三态）；RestartCount 派生
			// 与事件面同源——面缺席时计数为 0 且以 None 透出不可读（失败态
			// 观测本身仍在，由 unexplained 兜底解释，不猜循环次数）。
			obs = append(obs, diagnosis.WorkloadObservationFact{
				WorkloadID: ev.WorkloadID, Process: process, State: ev.State,
				ExitCode: ev.ExitCode, Reason: ev.Reason,
			})
			continue
		}
		rows := rowsByWorkload[ev.WorkloadID]
		exitFacts := ContainerExitedFacts(rows)
		counts := DeriveTerminalCounts(exitFacts, DiagnoseWindow, now)
		obs = append(obs, diagnosis.WorkloadObservationFact{
			WorkloadID: ev.WorkloadID, Process: process, State: ev.State,
			ExitCode: ev.ExitCode, Reason: ev.Reason,
			RestartCount: counts[ev.WorkloadID],
			LastExitAt:   lastExitAt(exitFacts, DiagnoseWindow, now),
		})
		// 近窗事件面（App 域内逐载体行合并，Seq 升序稳定）。
		for _, row := range rows {
			at, err := time.Parse(time.RFC3339, row.CreatedAt)
			if err != nil || at.Before(now.Add(-DiagnoseWindow)) || at.After(now) {
				continue // 窗外行/坏时间戳不入事实面（与计数窗同口径）
			}
			events = append(events, diagnosis.EventFact{
				Seq: row.Seq, Code: row.Name, At: at, Detail: string(row.Payload),
			})
		}
	}
	facts.Observations = diagnosis.Some(obs)
	if eventsReadable {
		sort.Slice(events, func(i, j int) bool { return events[i].Seq < events[j].Seq })
		facts.Events = diagnosis.Some(events)
	} else {
		facts.Events = diagnosis.None[[]diagnosis.EventFact]()
	}

	// 告警态面：alerting 行读面（firing + pending——W1b pending_since 已
	// 持久化；行读失败 → None）。
	rules, err := e.alertRules.ListByApp(ctx, appID)
	if err != nil {
		facts.Alerts = diagnosis.None[[]diagnosis.AlertFact]()
	} else {
		alerts := make([]diagnosis.AlertFact, 0, len(rules))
		for i := range rules {
			alerts = append(alerts, diagnosis.AlertFact{
				Rule: rules[i].ID, State: alertFaceState(&rules[i]), Metric: rules[i].Metric,
			})
		}
		facts.Alerts = diagnosis.Some(alerts)
	}

	// 组件健康面：本期恒 None（B2b 才有共享探活导出面；Option 三态为此
	// 设计——None 透出为 unreadable face，不猜）。
	facts.Components = diagnosis.None[[]diagnosis.ComponentHealthFact]()
	return facts
}

// DiagnoseAppCheapHit 报告廉价先行筛命中（设计 §2.4「需要你」页成本口径，
// B2a W2 分页/扇出预算）：观测快照存在非健康态即命中——v1 全部规则的
// 输入态超集（failed/degraded/orphaned 兜底 + stopped 稳态解释），命中才
// 值得深诊断（DiagnoseFacts + Evaluate，账本面 = 快照 workload 集一次
// ListByAggregateIDs 单查询）。纯内存判定（观测导出面扫描，零 I/O——ctx
// 仅为导出面签名兼容）；firing/pending 告警行命中由调用面另并（ListAll
// 单查）。
func (e *Engine) DiagnoseAppCheapHit(appID string) bool {
	for _, ev := range e.ObservationSnapshot(context.TODO(), appID) {
		switch ev.State {
		case capability.WorkloadFailed, capability.WorkloadDegraded,
			capability.WorkloadOrphaned, capability.WorkloadStopped:
			return true
		}
	}
	return false
}

// diagnosisEventRows 拉一组 workload 的账本行（App 级一次 IN 查询分组
// 返回，(team, aggregate, aggregate_id) 索引承载，00037 迁移）。修复批 F4
// 扇出收口：快照 workload 集单查询替代逐 workload ListByAggregate（「需要
// 你」页 = 每命中 App × 每 workload 的页级放大）。team 留空 = 不过滤：
// workload ID 全局唯一、聚合轴已精确圈定行集，归属解析不给诊断读面新增
// 失败面（与 crash-loop 沿扫描同口径）；每键 limit 钳制在 repo 面（缺省/
// 上界 1000，最新 N 行截断与逐键形态同向）。
func (e *Engine) diagnosisEventRows(ctx context.Context, workloadIDs []string) (map[string][]outbox.Event, error) {
	return e.repos.Outbox.ListByAggregateIDs(ctx, "", "workload", workloadIDs, 0, 1000)
}

// alertFaceState 把规则行投影为诊断告警态（ok|firing|pending——诊断吃
// pending 态，W1b 持久化列；firing 优先于 pending：迁移即离窗）。
func alertFaceState(r *alertrule.Rule) string {
	if r.State == alertrule.StateFiring {
		return alertrule.StateFiring
	}
	if r.PendingSince != "" {
		return "pending"
	}
	return alertrule.StateOK
}

// lastExitAt 取窗内最近终态时刻（零值 = 窗内无终态）。
func lastExitAt(facts []EventFact, window time.Duration, now time.Time) time.Time {
	cutoff := now.Add(-window)
	var last time.Time
	for _, f := range facts {
		if f.Name != eventWorkloadContainerExit || f.WorkloadID == "" {
			continue
		}
		if f.OccurredAt.Before(cutoff) || f.OccurredAt.After(now) {
			continue
		}
		if f.OccurredAt.After(last) {
			last = f.OccurredAt
		}
	}
	return last
}
