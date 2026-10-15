package engine

// 告警评估面（P3-2 收编自 metrics.go）：用户规则（App 聚合 max）与内置
// 规则（platform-offsite-backup）的阈值评估、迁移沿落库（fired/resolved
// 各一次）与事件落库（outbox）；通道派发半边见 channelnotify.go。

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
	eventAlertFired    = "alert.fired"
	eventAlertResolved = "alert.resolved"
)

// evaluateRules 评估用户规则（App 聚合 = 该 App 全部容器取 max——最热副本
// 代表，ADR-0041）。迁移沿：fired/resolved 各一次 + 事件 + 通道派发。
func (e *Engine) evaluateRules(ctx context.Context, samples []containerSample, now time.Time) {
	rules, err := e.alertRules.ListEnabled(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("metrics step: list rules", "err", err)
		return
	}
	if len(rules) == 0 {
		return
	}
	// App 聚合样本（max）。键 = 小写 appID（cadvisor 标签值经
	// sanitizeNamePart 小写化——与规则行的大写 ULID 在此归一）。
	agg := map[string]map[string]float64{} // lower(appID) → metric → value
	for _, s := range samples {
		m := agg[s.app]
		if m == nil {
			m = map[string]float64{}
			agg[s.app] = m
		}
		if s.cpuRate > m[alertrule.MetricCPUPercent] {
			m[alertrule.MetricCPUPercent] = s.cpuRate
		}
		if s.memBytes > m[alertrule.MetricMemoryWorkingSet] {
			m[alertrule.MetricMemoryWorkingSet] = s.memBytes
		}
	}
	for i := range rules {
		rule := &rules[i]
		value := agg[strings.ToLower(rule.AppID)][rule.Metric]
		observed := value != 0 // 零 = 无样本（cpu 率首拍/容器缺席）——不评估
		if observed {
			if err := e.alertRules.Observe(ctx, e.db.Runner(), rule.ID, value, state.FormatTime(now)); err != nil {
				e.log.Error("metrics step: observe rule", "rule", rule.ID, "err", err)
			}
		}
		if value > rule.Threshold {
			above, seen := e.metricsDom.above[rule.ID]
			if !seen {
				e.metricsDom.above[rule.ID] = now
				continue // 越限首见：起窗
			}
			if now.Sub(above) < time.Duration(rule.ForSecs)*time.Second {
				continue // 持续窗未满
			}
			if rule.State != alertrule.StateFiring {
				e.transitionRule(ctx, rule, alertrule.StateFiring, value, now)
			}
			continue
		}
		delete(e.metricsDom.above, rule.ID)
		if rule.State == alertrule.StateFiring {
			e.transitionRule(ctx, rule, alertrule.StateOK, value, now)
		}
	}
}

// transitionRule 落迁移 + 事件 + 派发（fire 与 resolve 同一通道载荷形态）。
func (e *Engine) transitionRule(ctx context.Context, rule *alertrule.Rule, to string, value float64, now time.Time) {
	if err := e.alertRules.Transition(ctx, e.db.Runner(), rule.ID, to, state.FormatTime(now)); err != nil {
		e.log.Error("metrics step: transition rule", "rule", rule.ID, "err", err)
		return
	}
	prev := rule.State
	rule.State = to
	eventName := eventAlertFired
	if to == alertrule.StateOK {
		eventName = eventAlertResolved
	}
	e.emitAlertEvent(ctx, eventName, rule, value)
	e.log.Info("alert state transitioned", "rule", rule.ID, "app", rule.AppID, "from", prev, "to", to, "value", value)
	e.dispatchAlert(ctx, rule, value, now)
}

// evaluateSystemRules 评估内置规则（F2.2 挂账：s3 未配持续 24h → firing）。
func (e *Engine) evaluateSystemRules(ctx context.Context, now time.Time) {
	offsiteAbsent := e.opts.Backup.PlatformBackup == nil || e.opts.Backup.PlatformBackup.S3 == nil
	if !offsiteAbsent {
		e.metricsDom.sysOffsiteAbove = time.Time{}
		if e.metricsDom.sysOffsiteFiring {
			e.metricsDom.sysOffsiteFiring = false
			e.emitSystemAlertEvent(ctx, eventAlertResolved)
			e.dispatchSystemAlert(ctx, alertrule.StateOK, now)
		}
		return
	}
	if e.metricsDom.sysOffsiteAbove.IsZero() {
		e.metricsDom.sysOffsiteAbove = now
		return
	}
	if now.Sub(e.metricsDom.sysOffsiteAbove) < systemOffsiteFor || e.metricsDom.sysOffsiteFiring {
		return // 未满窗或已 firing（不逐拍重发——迁移沿语义）
	}
	e.metricsDom.sysOffsiteFiring = true
	e.emitSystemAlertEvent(ctx, eventAlertFired)
	e.dispatchSystemAlert(ctx, alertrule.StateFiring, now)
}

// emitAlertEvent 落用户规则事件（outbox）。资源轴 Team 经 App → Project
// 解析（P0-2）。
func (e *Engine) emitAlertEvent(ctx context.Context, name string, rule *alertrule.Rule, value float64) {
	payload, _ := json.Marshal(alertEventPayload{
		RuleID: rule.ID, AppID: rule.AppID, Metric: rule.Metric,
		Threshold: rule.Threshold, Value: value, State: rule.State,
	})
	if err := e.emitObservation(ctx, e.eventTeamOfApp(ctx, e.db.Runner(), rule.AppID),
		name, "app", rule.AppID, payload); err != nil {
		e.log.Error("metrics step: emit event", "rule", rule.ID, "err", err)
	}
}

// emitSystemAlertEvent 落内置规则事件（aggregate=platform）。平台级事件：
// team 留 ” = 全员可见。
func (e *Engine) emitSystemAlertEvent(ctx context.Context, name string) {
	payload, _ := json.Marshal(alertEventPayload{
		RuleID: systemOffsiteRuleID, Metric: "platform_offsite_backup",
		State: name,
	})
	if err := e.emitObservation(ctx, "", name, "platform", "platform-backup", payload); err != nil {
		e.log.Error("metrics step: emit system event", "err", err)
	}
}

// alertEventPayload 是告警事件载荷。
type alertEventPayload struct {
	RuleID    string  `json:"rule_id"`
	AppID     string  `json:"app_id,omitempty"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold,omitempty"`
	Value     float64 `json:"value,omitempty"`
	State     string  `json:"state"`
}

// alertChannelFailedPayload 是 alert.channel_failed 事件载荷（诊断面）。
type alertChannelFailedPayload struct {
	ChannelID string `json:"channel_id"`
	Error     string `json:"error"`
}
