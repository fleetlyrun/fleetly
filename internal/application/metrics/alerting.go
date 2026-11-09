package metrics

// 告警评估面（P3-2 收编自 metrics.go）：用户规则（App 聚合 max）与内置
// 规则（platform-offsite-backup）的阈值评估、迁移沿落库（fired/resolved
// 各一次）与事件落库（outbox）；通道派发半边见 channelnotify.go。
// P3a（ADR-0087）：评估决策（迁移表/pending 窗语义）单源在域——
// domain/telemetry/alertrule.Evaluate；本文件持读数装配与行写执行。
// P4 wave-1b（ADR-0088 决策 2）：接收者下域（应用模块 struct 见 metrics.go）。
// P5 wave-2 拆包随域迁入本包（事件名常量导出——engine 核 converge 缺省
// 收敛循环与 application module 白盒消费经包名引）。

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
)

// 事件名（eventcode 注册表同源）。
const (
	// EventAlertFired 是用户/系统规则迁入 firing 的事件名（converge 缺省
	// 收敛循环的事件发射面同源消费）。
	EventAlertFired = "alert.fired"
	// EventAlertResolved 是规则迁回 ok 的事件名。
	EventAlertResolved = "alert.resolved"
)

// evaluateRules 评估用户规则（App 聚合 = 该 App 全部容器取 max——最热副本
// 代表，ADR-0041）。ADR-0087 P3a：迁移表与裁决单源在域（alertrule.Evaluate
// ——起窗/续窗/满窗/清窗、无样本不动窗的语义表见彼处白盒测试），采集循环
// 只做读数装配与行写执行（迁移沿：fired/resolved 各一次 + 事件 + 通道
// 派发）。pending 持续窗直接落 pending_since 列（B2a W1b：无双写、重启
// 续窗——起算点从行恢复）。
func (m *Metrics) evaluateRules(ctx context.Context, samples []containerSample, now time.Time) {
	rules, err := m.alertRules.ListEnabled(ctx)
	if err != nil {
		m.log.Error("metrics step: list rules", "err", err)
		return
	}
	if len(rules) == 0 {
		return
	}
	// App 聚合样本（max）。键 = 小写 appID（cadvisor 标签值经
	// sanitizeNamePart 小写化——与规则行的大写 ULID 在此归一）。
	agg := map[string]map[string]float64{} // lower(appID) → metric → value
	for _, s := range samples {
		mm := agg[s.app]
		if mm == nil {
			mm = map[string]float64{}
			agg[s.app] = mm
		}
		if s.cpuRate > mm[alertrule.MetricCPUPercent] {
			mm[alertrule.MetricCPUPercent] = s.cpuRate
		}
		if s.memBytes > mm[alertrule.MetricMemoryWorkingSet] {
			mm[alertrule.MetricMemoryWorkingSet] = s.memBytes
		}
	}
	for i := range rules {
		rule := &rules[i]
		// 装配读数（聚合 max + 规则匹配；无样本 = 键缺席）→ 域裁决 →
		// 行写执行（Observe/窗操作/迁移）。
		value, sampled := agg[strings.ToLower(rule.AppID)][rule.Metric]
		v := rule.Evaluate(value, sampled, now)
		if v.Observe {
			if err := m.alertRules.Observe(ctx, rule.ID, value, state.FormatTime(now)); err != nil {
				m.log.Error("metrics step: observe rule", "rule", rule.ID, "err", err)
			}
		}
		if !v.BeginPending.IsZero() {
			if err := m.alertRules.BeginPending(ctx, rule.ID, state.FormatTime(v.BeginPending)); err != nil {
				m.log.Error("metrics step: begin pending window", "rule", rule.ID, "err", err)
			}
			continue // 起窗拍无其他动作（Verdict 不变量：与清窗/迁移互斥）
		}
		if v.ClearPending {
			if err := m.alertRules.ClearPending(ctx, rule.ID); err != nil {
				m.log.Error("metrics step: clear pending window", "rule", rule.ID, "err", err)
			}
		}
		if v.Transition != "" {
			m.transitionRule(ctx, rule, v.Transition, value, now)
		}
	}
}

// transitionRule 落迁移 + 事件 + 派发（fire 与 resolve 同一通道载荷形态）。
// 迁移即离窗：Transition 落行同时清 pending_since（repo 面承载）。
//
// 归属裁定（P4 wave-1b）：transitionRule 是行写执行，但行写经域自有 repo
// （alertRules.Transition——transact.Stores 注入的域 repo，写原语单份在
// repo 面），非 transit/commitWrite 家族（后者是部署态 CAS+outbox+审计的
// 单写原语）；事件走 emitter 端口 best-effort 面、派发走本域通道面——全
// 部消费面都在域内，随应用模块走（ADR-0088 决策 2）。
func (m *Metrics) transitionRule(ctx context.Context, rule *alertrule.Rule, to string, value float64, now time.Time) {
	if err := m.alertRules.Transition(ctx, rule.ID, to, state.FormatTime(now)); err != nil {
		m.log.Error("metrics step: transition rule", "rule", rule.ID, "err", err)
		return
	}
	prev := rule.State
	rule.State = to
	rule.PendingSince = ""
	eventName := EventAlertFired
	if to == alertrule.StateOK {
		eventName = EventAlertResolved
	}
	m.emitAlertEvent(ctx, eventName, rule, value)
	m.log.Info("alert state transitioned", "rule", rule.ID, "app", rule.AppID, "from", prev, "to", to, "value", value)
	m.dispatchAlert(ctx, rule, value, now)
}

// evaluateSystemRules 评估内置规则（F2.2 挂账：s3 未配持续 24h → firing）。
// 状态对的读写收进 metrics 锁临界区（gRPC handler 并发读同一对面）；
// 事件落库与通道派发（I/O）在锁外执行。
func (m *Metrics) evaluateSystemRules(ctx context.Context, now time.Time) {
	if m.offsiteConfigured() {
		if m.sysOffsiteReset() {
			m.emitSystemAlertEvent(ctx, EventAlertResolved)
			m.dispatchSystemAlert(ctx, alertrule.StateOK, now)
		}
		return
	}
	if m.sysOffsiteObserve(now) {
		m.emitSystemAlertEvent(ctx, EventAlertFired)
		m.dispatchSystemAlert(ctx, alertrule.StateFiring, now)
	}
}

// emitAlertEvent 落用户规则事件（outbox）。资源轴 Team 经 App → Project
// 解析（P0-2）；解析器与发射面经 emitter 端口。
func (m *Metrics) emitAlertEvent(ctx context.Context, name string, rule *alertrule.Rule, value float64) {
	payload, _ := json.Marshal(AlertEventPayload{
		RuleID: rule.ID, AppID: rule.AppID, Metric: rule.Metric,
		Threshold: rule.Threshold, Value: value, State: rule.State,
	})
	if err := m.emitObservation(ctx, m.eventTeamOfApp(ctx, m.repos, rule.AppID),
		name, "app", rule.AppID, payload); err != nil {
		m.log.Error("metrics step: emit event", "rule", rule.ID, "err", err)
	}
}

// emitSystemAlertEvent 落内置规则事件（aggregate=platform）。平台级事件：
// team 留 ” = 全员可见。
func (m *Metrics) emitSystemAlertEvent(ctx context.Context, name string) {
	payload, _ := json.Marshal(AlertEventPayload{
		RuleID: systemOffsiteRuleID, Metric: "platform_offsite_backup",
		State: name,
	})
	if err := m.emitObservation(ctx, "", name, "platform", "platform-backup", payload); err != nil {
		m.log.Error("metrics step: emit system event", "err", err)
	}
}

// AlertEventPayload 是告警事件载荷（导出面：engine 核 converge 缺省收敛循环
// 的事件发射同一载荷形态——engine 侧经包内别名零漂移）。
type AlertEventPayload struct {
	RuleID    string  `json:"rule_id"`
	AppID     string  `json:"app_id,omitempty"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold,omitempty"`
	Value     float64 `json:"value,omitempty"`
	State     string  `json:"state"`
}

// AlertChannelFailedPayload 是 alert.channel_failed 事件载荷（诊断面）。
// 导出面：engine 核 schemareg 的事件载荷注册经包名引（P5 wave-2；常量
// EventAlertChannelFail 在 channelnotify.go）。
type AlertChannelFailedPayload struct {
	ChannelID string `json:"channel_id"`
	Error     string `json:"error"`
}
