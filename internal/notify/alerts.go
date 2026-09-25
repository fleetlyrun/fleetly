package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 告警投递（B 线 W5 设计 §2.3，D-V3W5-1，v0.3 W5-S2）：内建 Alertmanager
// webhook 兼容接收器（internal/runtime）→ 本文件的投递面。映射语义：
//
//	端点解析   annotations.channels（逗号分隔端点 id 集）→ 启用端点子集；
//	           缺省/空 = 投**全部启用端点**（设计 §2.3「缺省投全部端点」）；
//	           引用的 id 不存在或已停用 → 跳过（warn 日志，不阻塞其余端点
//	           ——规则引用的端点可后删，投递期是唯一解析点）。
//	文案       firing → 头行 `[fleetly] FIRING <alertname> (severity=<v>)`；
//	           resolved → `[fleetly] RESOLVED <alertname> …`——**RESOLVED
//	           前缀恢复通知**（vmalert 原生 resolved 语义，设计 §2.3 原文）；
//	           severity 进头行与详情行（labels 展开原样携带）。
//	预算       台账行（webhook_deliveries，alert_payload 列物化载荷）+
//	           既有投递 worker/退避/终态预算逐字复用（Config 全部生效）。
//	红线       **告警投递零事件**（防回环——vmalert 不经事件流；台账行
//	           event_seq 恒 0，事件消费游标不动）。
//
// 本文件不新增通道执行体：webhook=签名 POST / slack={"text"} / email=SMTP
// 会话（channels.go 既有 sendPayload/sendSlack/sendEmail 逐字复用）。

// AlertStatusFiring / AlertStatusResolved 是告警状态词表（Alertmanager
// webhook 载荷的 status 形态）。
const (
	AlertStatusFiring   = "firing"
	AlertStatusResolved = "resolved"
)

// PayloadTypeAlert 是告警投递的载荷 type 标位（事件投递 type=event、试发
// type=test 的同位扩展——接收方按 type 分流，既有键序不变）。
const PayloadTypeAlert = "alert"

// AlertNotice 是一条待投递的告警事实（接收器解析 Alertmanager 载荷后的
// 归一形态；一条 webhook 可能携带多条 alert——每条独立投递）。
type AlertNotice struct {
	// Name 是 alertname label（缺省 "unnamed"）。
	Name string
	// Status 是 firing | resolved（载荷缺省 firing——Alertmanager v2 语义）。
	Status string
	// Severity 是 severity label（缺省 ""——文案头行如实留空档）。
	Severity string
	// StartsAt/EndsAt 是载荷时刻（RFC3339 解析；零值 = 载荷未带——详情行
	// 省略该行）。
	StartsAt time.Time
	EndsAt   time.Time
	// Labels / Annotations 是载荷原样（文案详情行展开；annotations.channels
	// 是平台内路由事实，从文案展开面摘除，载荷 JSON 中仍可见）。
	Labels      map[string]string
	Annotations map[string]string
	// Channels 是 annotations.channels 解析出的端点 id 集（空 = 全部启用
	// 端点）。
	Channels []string
}

// Resolved 报告恢复态（RESOLVED 前缀的判据）。
func (n AlertNotice) Resolved() bool { return n.Status == AlertStatusResolved }

func (n AlertNotice) status() string {
	if n.Resolved() {
		return AlertStatusResolved
	}
	return AlertStatusFiring
}

// HeadText 是通知文案头行（slack text 首行 / email 主题行同源）：
// `[fleetly] FIRING <name> (severity=<v>)`，恢复态以 `RESOLVED` 置换
// FIRING——设计 §2.3 的「RESOLVED 前缀」承载位。
func (n AlertNotice) HeadText() string {
	status := "FIRING"
	if n.Resolved() {
		status = "RESOLVED"
	}
	if n.Severity == "" {
		return fmt.Sprintf("[fleetly] %s %s", status, n.Name)
	}
	return fmt.Sprintf("[fleetly] %s %s (severity=%s)", status, n.Name, n.Severity)
}

// DetailLines 是文案详情行（starts/ends/labels/annotations 展开键值对；
// 键序排序保证同输入恒同渲染——render.go 纪律）。annotations.channels 不
// 展开（端点 id 集是平台内路由事实，不是接收方需要的内容）。
func (n AlertNotice) DetailLines() []string {
	lines := []string{}
	if !n.StartsAt.IsZero() {
		lines = append(lines, "starts_at: "+n.StartsAt.UTC().Format(time.RFC3339))
	}
	if !n.EndsAt.IsZero() {
		lines = append(lines, "ends_at: "+n.EndsAt.UTC().Format(time.RFC3339))
	}
	lines = append(lines, alertKvLines("label", n.Labels)...)
	lines = append(lines, alertKvLines("annotation", n.Annotations)...)
	return lines
}

// alertKvLines 是一组键值的前缀行（键序排序；channels 注记见 DetailLines）。
func alertKvLines(prefix string, kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		if k == "channels" && prefix == "annotation" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s.%s: %s", prefix, k, kv[k]))
	}
	return out
}

// SlackText 渲染 slack 的 text 字段（头行 + 详情行；RenderSlackText 同形）。
func (n AlertNotice) SlackText() string {
	return strings.TrimRight(n.HeadText()+"\n"+strings.Join(n.DetailLines(), "\n"), "\n")
}

// EmailSubject 渲染邮件主题（HeadText 同串——状态前缀在主题即见）。
func (n AlertNotice) EmailSubject() string { return n.HeadText() }

// EmailBody 渲染纯文本正文（告警名行 + 详情行；不做 HTML——render.go 口径）。
func (n AlertNotice) EmailBody() string {
	return strings.Join(append([]string{"name: " + n.Name}, n.DetailLines()...), "\n")
}

// toPayload 构造 webhook 通道的投递载荷（type=alert；seq 恒 0——告警零
// 事件，事件 seq 自 1 起单调）。payload 字段 = 台账行物化的事实 JSON 原样。
func (n AlertNotice) toPayload(atUnix int64, factsRaw string) Payload {
	if !json.Valid([]byte(factsRaw)) {
		factsRaw = "{}"
	}
	return Payload{
		Type:    PayloadTypeAlert,
		Seq:     0,
		At:      atUnix,
		Name:    "alert." + n.status(),
		Subject: n.Name,
		Payload: json.RawMessage(factsRaw),
	}
}

// payloadJSON 物化进台账行 alert_payload 列的载荷事实（紧凑 JSON——投递时
// 重建 AlertNotice 的唯一事实源，重启恢复安全）。
func (n AlertNotice) payloadJSON() (string, error) {
	obj := map[string]any{
		"alertname": n.Name,
		"status":    n.status(),
		"labels":    n.labelsOrEmpty(),
	}
	if !n.StartsAt.IsZero() {
		obj["starts_at"] = n.StartsAt.UTC().Format(time.RFC3339)
	}
	if !n.EndsAt.IsZero() {
		obj["ends_at"] = n.EndsAt.UTC().Format(time.RFC3339)
	}
	if n.Severity != "" {
		obj["severity"] = n.Severity
	}
	if len(n.Annotations) > 0 {
		obj["annotations"] = n.Annotations
	}
	if len(n.Channels) > 0 {
		obj["channels"] = n.Channels
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("notify: marshal alert payload: %w", err)
	}
	return string(raw), nil
}

func (n AlertNotice) labelsOrEmpty() map[string]string {
	if n.Labels == nil {
		return map[string]string{}
	}
	return n.Labels
}

// alertNoticeFromJSON 从台账行 alert_payload 重建告警事实（重启恢复路径
// ——载荷物化在台账行自身，事件流零依赖）。
func alertNoticeFromJSON(raw string) (AlertNotice, error) {
	var facts struct {
		Alertname   string            `json:"alertname"`
		Status      string            `json:"status"`
		Severity    string            `json:"severity"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		Channels    []string          `json:"channels"`
		StartsAt    string            `json:"starts_at"`
		EndsAt      string            `json:"ends_at"`
	}
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return AlertNotice{}, fmt.Errorf("notify: parse stored alert payload: %w", err)
	}
	startsAt, _ := time.Parse(time.RFC3339, facts.StartsAt)
	endsAt, _ := time.Parse(time.RFC3339, facts.EndsAt)
	return AlertNotice{
		Name:        facts.Alertname,
		Status:      facts.Status,
		Severity:    facts.Severity,
		StartsAt:    startsAt,
		EndsAt:      endsAt,
		Labels:      facts.Labels,
		Annotations: facts.Annotations,
		Channels:    facts.Channels,
	}, nil
}

// EnqueueAlert 为一条告警落台账并入队（接收器的投递入口；零事件）。返回
// 受理的端点数（0 = 没有启用端点可投——调用方照常 2xx 应答 vmalert，投递
// 面的事实由日志披露；Alertmanager 语义里 receiver 不该 4xx 拒投）。
func (m *Manager) EnqueueAlert(ctx context.Context, n AlertNotice) (int, error) {
	payload, err := n.payloadJSON()
	if err != nil {
		return 0, err
	}
	endpoints, err := m.store.ListWebhookEndpoints(ctx)
	if err != nil {
		return 0, err
	}
	enabled := make(map[string]string, len(endpoints))
	for _, ep := range endpoints {
		if ep.Enabled {
			enabled[ep.ID] = ep.Name
		}
	}
	// 端点解析（映射语义表）：显式 channels → 启用端点子集（未知/停用跳过
	// + warn）；缺省/空 → 全部启用端点。
	targets := make([]string, 0, len(n.Channels))
	if len(n.Channels) > 0 {
		for _, id := range n.Channels {
			if _, ok := enabled[id]; ok {
				targets = append(targets, id)
				continue
			}
			m.log.Warn("notify: alert channel endpoint not found or disabled, skipping",
				"endpoint", id, "alert", n.Name, "status", n.status())
		}
	} else {
		for id := range enabled {
			targets = append(targets, id)
		}
	}
	sort.Strings(targets) // 渲染/台账序确定性
	if len(targets) == 0 {
		m.log.Warn("notify: alert has no enabled endpoint to deliver (nothing owed)", "alert", n.Name, "status", n.status())
		return 0, nil
	}
	rows, err := m.store.CreateAlertDeliveries(ctx, targets, payload)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		m.enqueue(deliveryJob{deliveryID: row.ID, endpointID: row.EndpointID})
	}
	m.log.Info("notify: alert accepted for delivery",
		"alert", n.Name, "status", n.status(), "endpoints", len(rows))
	return len(rows), nil
}

// attemptAlert 是告警台账行的单次投递尝试（attempt 的事件分支对偶）：载荷
// 取自台账行 alert_payload（重启恢复安全），渲染按告警文案（头行
// FIRING/RESOLVED + severity），通道执行体复用既有三件。
func (m *Manager) attemptAlert(ctx context.Context, alertPayload string, ep Endpoint, smtpCfg *SmtpConfig) (bool, int, string) {
	notice, err := alertNoticeFromJSON(alertPayload)
	if err != nil {
		return false, 0, err.Error()
	}
	switch ep.Type {
	case ChannelWebhook, "":
		body, err := MarshalPayload(notice.toPayload(time.Now().UTC().Unix(), alertPayload))
		if err != nil {
			return false, 0, "payload marshal failed: " + err.Error()
		}
		return sendPayload(ctx, m.client, ep.URL, ep.Secret, body, m.cfg.attemptTimeout())
	case ChannelSlack:
		body, err := json.Marshal(map[string]string{"text": notice.SlackText()})
		if err != nil {
			return false, 0, "slack text marshal failed: " + err.Error()
		}
		return sendSlack(ctx, m.client, ep.URL, body, m.cfg.attemptTimeout())
	case ChannelEmail:
		if smtpCfg == nil {
			return false, 0, "smtp settings are not configured (set them with 'notifications smtp set' before email deliveries)"
		}
		ok, _, errText := sendEmail(ctx, *smtpCfg, ep.Target,
			notice.EmailSubject(), notice.EmailBody(), m.cfg.attemptTimeout())
		// 台账语义（§8.1）：email 走通用 success bool + detail 字符串——
		// response_code 不承载 SMTP 语义（恒 0，与事件投递同口径）。
		return ok, 0, errText
	default:
		return false, 0, fmt.Sprintf("unknown channel type %q (dirty row?)", ep.Type)
	}
}
