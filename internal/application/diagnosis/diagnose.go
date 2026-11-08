// Package diagnosis 是诊断域卫星（B2a W2，zeabur 借鉴设计 §2.4——「facts
// 不是现成而是经导出面装配」）：Facts 把五事实源装配为域规则层的
// diagnosis.Facts 供规则层读时计算——观测快照（ObservationSnapshot 活状态
// 导出面 + WorkloadProcess 窄导出）、近窗事件（outbox (team, aggregate,
// aggregate_id) 索引查询 + diagnosis.ContainerExitedFacts/DeriveTerminalCounts
// 纯派生——终态计数派生已归 domain，P3a 第 8 单元）、告警态（alerting
// 行读面 firing+pending，W1b 已持久化）、组件健康（本期恒 None——B2b 才有
// 共享导出面，Option 三态正是为此设计）、App 最新部署行（deployment repo
// 行读面——DB 权威态非事件面，立案③残留缺口批：d1 failed 时错误正文在
// deployments 行而观测/事件面无源，诊断规则族缺部署腿则 apps diagnose 对
// 失败部署报 no findings）。
//
// P4 收官归属表复核的「diagnosis 应用侧半步驻核（P5 前专议）」于 P5
// wave-6 裁决出包（ADR-0088 决策 1 终态第 9 星，九星对账闭合 9/9）：
// 规则层在 domain/services/diagnosis（ADR-0087），本卫星是 application
// 装配半边——无自有状态、无环、无写面（纯读时装配器），依赖显式逐字段
// 注入 + 观测导出面端口（ObservationSnapshot/WorkloadProcess——obs 私有
// 缓存的唯一合法出口，metrics 卫星 WorkloadProcess 端口同款）；告警行读
// 经 alertRules repo 端口（组合根绑核门面 AlertRuleRepo——repo 持有权随
// metrics 卫星，P5 wave-2；星间零 import）。禁回引 *engine.Engine
// （enginebackref 跨包执法）。
//
// 零旁路纪律（guards/diagnosbypass_test.go 执法）：本文件只准经导出面
// （ObservationSnapshot/WorkloadProcess）与 repo 端口（Outbox/AlertRules）
// 取数，禁触观测域内部符号——拆包后跨包私有由编译器执法，守卫载绿锚
// （端口消费在用）与禁触名回归装甲。单一事实源读不了时该面落 None
// （读不了 ≠ 不存在，规则对 None 沉默），UnreadableFaces 透出供诚实展示。
package diagnosis

import (
	"context"
	"sort"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
)

// Facts 重导出域规则事实面（engine 核门面签名经本别名原可达——通则③：
// 消费面零改；真源 domain/services/diagnosis.Facts，别名零漂移）。
type Facts = diagnosis.Facts

// DiagnoseWindow 是诊断近窗口径：终态计数与事件面共用同一窗（崩溃循环
// 阈值 diagnosis.CrashLoopThreshold 在窗内计数——规则阈值与装配窗口分离，
// 窗口是装配面参数而非域知识）。单源在 engineports.DiagnoseWindow（P5
// wave-2 随 metrics 拆包下沉——crash-loop 计数窗与本装配窗同一口径），
// 本别名保域内既名零漂移。
const DiagnoseWindow = engineports.DiagnoseWindow

// Diagnosis 是诊断域卫星实例态（C5：实例内聚合；读时装配器——无自有可变
// 状态，struct 只是端口的具名载体）。行为随域、依赖显式逐字段 + 导出面
// 端口注入（New 装配面，engine construct.go 组合根调用）。
type Diagnosis struct {
	// clock 是装配节拍（Now 进 Facts——读时计算面）。
	clock state.Clock
	// outbox 是事件账本读面（ListByAggregateIDs 的 App 级一次往返）。
	outbox outbox.Repo
	// alertRules 是告警行读面（firing+pending；组合根绑核门面 AlertRuleRepo
	// ——repo 持有权随 metrics 卫星，星间零 import）。
	alertRules alertrule.Repo
	// deployments 是 App 部署行读面（ListByApp 新→旧前 N 行——第五事实源
	// 的 repo 端口，DB 权威态非事件面）。
	deployments deployment.Repo
	// observationSnapshot 是 obs 域活状态快照导出面端口（核方法值；obs
	// 私有缓存的唯一合法出口——diagnosbypass 守卫绿锚）。
	observationSnapshot func(ctx context.Context, appID string) []capability.WorkloadEvent
	// workloadProcess 是载体进程模板名窄导出端口（核方法值；metrics 卫星
	// 同款端口）。
	workloadProcess func(workloadID string) (string, bool)
}

// Deps 是卫星依赖与导出面端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：依赖显式逐字段注入 + 端口绑定为核方法值）。
type Deps struct {
	Clock      state.Clock
	Outbox     outbox.Repo
	AlertRules alertrule.Repo
	// Deployments 是部署行读面（repos.Deployments 直绑——与 Outbox 同款
	// repo 端口形态，组合根注入）。
	Deployments deployment.Repo
	// ObservationSnapshot 是 obs 活状态快照导出面（核 ObservationSnapshot
	// 方法值——App 域归属圈定、workloadID 字典序稳定）。
	ObservationSnapshot func(ctx context.Context, appID string) []capability.WorkloadEvent
	// WorkloadProcess 是进程模板名窄导出（核 WorkloadProcess 方法值）。
	WorkloadProcess func(workloadID string) (string, bool)
}

// New 构造诊断域卫星（读时装配器，无启动面——调用即装配）。
func New(deps Deps) *Diagnosis {
	return &Diagnosis{
		clock:               deps.Clock,
		outbox:              deps.Outbox,
		alertRules:          deps.AlertRules,
		deployments:         deps.Deployments,
		observationSnapshot: deps.ObservationSnapshot,
		workloadProcess:     deps.WorkloadProcess,
	}
}

// Facts 装配某 App 的诊断事实快照（读时计算面）。无失败返回：某事实源
// 读不了 → 该面 None（诚实三态），不影响其余面。
func (d *Diagnosis) Facts(ctx context.Context, appID string) diagnosis.Facts {
	now := d.clock.Now()
	facts := diagnosis.Facts{Now: now}

	// 观测面（导出面快照——活状态 State/ExitCode/Reason，App 域归属圈定、
	// workloadID 字典序稳定）：逐载体投影为 WorkloadObservationFact；
	// RestartCount/LastExitAt 由事件账本纯派生（无独立计数器状态）。账本面
	// 是 App 级一次往返（修复批 F4 扇出收口）：快照 workload 集一次
	// ListByAggregateIDs 分组返回——逐 workload ListByAggregate 会在「需要
	// 你」页放大为页级 N×M 次查询。
	snap := d.observationSnapshot(ctx, appID)
	ids := make([]string, 0, len(snap))
	for _, ev := range snap {
		ids = append(ids, ev.WorkloadID)
	}
	rowsByWorkload, rerr := d.eventRows(ctx, ids)
	obs := make([]diagnosis.WorkloadObservationFact, 0, len(snap))
	var events []diagnosis.EventFact
	eventsReadable := rerr == nil
	for _, ev := range snap {
		process, _ := d.workloadProcess(ev.WorkloadID)
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
		exitFacts := diagnosis.ContainerExitedFacts(rows)
		counts := diagnosis.DeriveTerminalCounts(exitFacts, DiagnoseWindow, now)
		obs = append(obs, diagnosis.WorkloadObservationFact{
			WorkloadID: ev.WorkloadID, Process: process, State: ev.State,
			ExitCode: ev.ExitCode, Reason: ev.Reason,
			RestartCount: counts[ev.WorkloadID],
			LastExitAt:   diagnosis.LastExitAt(exitFacts, DiagnoseWindow, now),
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
	// 持久化；行读失败 → None）。repo 持有权随 metrics 卫星（P5 wave-2），
	// 组合根绑核门面 AlertRuleRepo 读（repo 端口面——diagnosbypass 守卫的
	// 合法取数出口不变）。
	rules, err := d.alertRules.ListByApp(ctx, appID)
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

	// 部署面（第五事实源）：App 最新部署行——repo 行读面单查询（ListByApp
	// 新→旧前 N 行，既有查询面复用、零新开）；行读失败 → None（读不了 ≠
	// 不存在），App 无部署行 → Some 零值（可读且确无）。
	rows, derr := d.deployments.ListByApp(ctx, appID, 0, latestDeploymentWindow)
	if derr != nil {
		facts.LatestDeployment = diagnosis.None[diagnosis.LatestDeploymentFact]()
	} else {
		facts.LatestDeployment = diagnosis.Some(latestDeploymentRow(rows))
	}
	return facts
}

// latestDeploymentWindow 是部署面的取行窗（ListByApp 新→旧前 N 行）：正常
// 形态首行即答案（单写者下活跃行恒最新、终态行之下无活跃行）；窗 >1 只为
// 吸收异常交错（planted 行/崩溃恢复窗下「终态行压着活跃行」的病态序）——
// 首条非终态行优先，否则取窗首（最新终态）。
const latestDeploymentWindow = 5

// latestDeploymentRow 从新→旧窗内选事实行：最新非终态优先/否则最新终态
// （空窗 = 零值行：可读且确无部署，规则对零值沉默）。
func latestDeploymentRow(rows []deployment.Deployment) diagnosis.LatestDeploymentFact {
	var pick deployment.Deployment
	for i := range rows {
		if rows[i].State.Active() {
			pick = rows[i]
			break
		}
	}
	if pick.ID == "" && len(rows) > 0 {
		pick = rows[0]
	}
	return diagnosis.LatestDeploymentFact{
		ID: pick.ID, State: pick.State, Error: pick.Error,
		Generation: pick.Generation, FinishedAt: pick.FinishedAt,
	}
}

// CheapHit 报告廉价先行筛命中（设计 §2.4「需要你」页成本口径，B2a W2
// 分页/扇出预算）：观测快照存在非健康态即命中——v1 全部规则的输入态
// 超集（failed/degraded/orphaned 兜底 + stopped 稳态解释），命中才值得深
// 诊断（Facts + Evaluate，账本面 = 快照 workload 集一次 ListByAggregateIDs
// 单查询）。纯内存判定（观测导出面扫描，零 I/O——ctx 仅为导出面签名
// 兼容）；firing/pending 告警行命中由调用面另并（ListAll 单查）。
func (d *Diagnosis) CheapHit(appID string) bool {
	for _, ev := range d.observationSnapshot(context.TODO(), appID) {
		switch ev.State {
		case capability.WorkloadFailed, capability.WorkloadDegraded,
			capability.WorkloadOrphaned, capability.WorkloadStopped:
			return true
		}
	}
	return false
}

// eventRows 拉一组 workload 的账本行（App 级一次 IN 查询分组返回，
// (team, aggregate, aggregate_id) 索引承载，00037 迁移）。修复批 F4 扇出
// 收口：快照 workload 集单查询替代逐 workload ListByAggregate（「需要你」
// 页 = 每命中 App × 每 workload 的页级放大）。team 留空 = 不过滤：
// workload ID 全局唯一、聚合轴已精确圈定行集，归属解析不给诊断读面新增
// 失败面（与 crash-loop 沿扫描同口径）；每键 limit 钳制在 repo 面（缺省/
// 上界 1000，最新 N 行截断与逐键形态同向）。
func (d *Diagnosis) eventRows(ctx context.Context, workloadIDs []string) (map[string][]outbox.Event, error) {
	return d.outbox.ListByAggregateIDs(ctx, "", "workload", workloadIDs, 0, 1000)
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
