package engine

// Metrics 采集环（第 10 收敛环，F2.5/ADR-0041 决策 2/3）：每节拍（15s 粗
// 节拍——tick 1s 内自持锚）抓取各节点 cadvisor 端点（host 发布 8080），
// 原文透传 VM（平台归因 extra_label）+ 同一遍在手法评估阈值规则（内存
// 最新样本——评估零查询依赖）。状态迁移沿才通知（fired/resolved 各一次）。
//
// 诚实边界（ADR-0041）：评估是 15s 采样近似（for-window 连续性按采样序
// 判定；重启重置 pending 面）；节点下线 = 该节点序列停写（VM 断口诚实
// 呈现）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	alertrule "github.com/fleetlyrun/fleetly/internal/state/alertrule"
	"github.com/fleetlyrun/fleetly/internal/state/channel"
)

// 采集环参数（环本地常量；无运维面诉求——同 logging 环纪律）。
const (
	// metricsScrapeInterval 是抓取节拍（与 cadvisor --housekeeping_interval
	// 对齐）。
	metricsScrapeInterval = 15 * time.Second
	// metricsScrapeTimeout 是单节点抓取上界。
	metricsScrapeTimeout = 10 * time.Second
	// cadvisorPort 是受管 cadvisor 端点的宿主发布端口（Provider 声明同源）。
	metricsCadvisorPort = 8080
	// systemOffsiteRuleID 是系统内置规则 id（platform-offsite-backup——
	// F2.2 挂账消化：s3 未配持续告警；不落行、不可删）。
	systemOffsiteRuleID = "platform-offsite-backup"
	// systemOffsiteFor 是内置规则的持续窗（s3 缺席满一天才轰——升级窗口
	// 与临时摘仓不误报）。
	systemOffsiteFor = 24 * time.Hour
)

// 事件名（eventcode 注册表同源）。
const (
	eventAlertFired       = "alert.fired"
	eventAlertResolved    = "alert.resolved"
	eventAlertChannelFail = "alert.channel_failed"
)

// metricsDomain 是采集环实例态（C5：实例内聚合）。
type metricsDomain struct {
	// lastScrapeAt 是节拍锚（15s 粗节拍在 1s tick 内自持）。
	lastScrapeAt time.Time
	// above 是规则越限起始时刻（pending 持续窗的内存面；重启重置——采样
	// 近似语义的一部分，ADR-0041）。键 = 规则 ID。
	above map[string]time.Time
	// lastCPU 是容器 CPU 计数器差分基面（cadvisor counter → 率）。键 =
	// node|containerID。
	lastCPU map[string]cpuCounter
	// cores 是节点核数观测（cpu_percent 的分母）。
	cores map[string]float64
	// sysOffsiteAbove 是内置规则的越限起始（daemon 生命期内存面）。
	sysOffsiteAbove  time.Time
	sysOffsiteFiring bool
}

type cpuCounter struct {
	value float64
	at    time.Time
}

// containerSample 是评估面的容器样本（cadvisor 序列标签还原平台归因）。
type containerSample struct {
	node     string
	app      string // fleetly.ns.app（container_label_fleetly_ns_app）
	id       string // docker 容器 ID（cadvisor id 标签）
	cpuRate  float64
	memBytes float64
}

// metricsStep 是采集环收敛步：节拍锚 → 集群视图 → 逐节点抓取/透传/解析
// → 规则评估（含内置规则）。
func (e *Engine) metricsStep(ctx context.Context) {
	if e.metrics == nil {
		return
	}
	now := e.clock.Now()
	if now.Sub(e.metricsDom.lastScrapeAt) < metricsScrapeInterval {
		return // 粗节拍锚：tick 1s 内自持 15s
	}
	e.metricsDom.lastScrapeAt = now
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		e.log.Error("metrics step: describe cluster", "err", err)
		return
	}
	var samples []containerSample
	for _, node := range view.Nodes {
		if !node.Available || node.Addr == "" || node.NodeID == "" {
			continue
		}
		body, err := e.scrapeNode(ctx, node.Addr)
		if err != nil {
			e.log.Warn("metrics step: scrape node", "node", node.NodeID, "addr", node.Addr, "err", err)
			continue
		}
		// 原文透传（平台归因 extra_label；失败下拍重试——样本断口诚实）。
		if err := e.metrics.ImportPrometheus(ctx, body, map[string]string{
			"job":  "fleetly-cadvisor",
			"node": node.NodeID,
		}); err != nil {
			e.log.Warn("metrics step: import node samples", "node", node.NodeID, "err", err)
		}
		samples = append(samples, e.parseCadvisor(node.NodeID, body, now)...)
	}
	e.evaluateRules(ctx, samples, now)
	e.evaluateSystemRules(ctx, now)
}

// scrapeNode 抓取单节点 cadvisor 端点（host 发布）。
func (e *Engine) scrapeNode(ctx context.Context, addr string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, metricsScrapeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/metrics", addr, metricsCadvisorPort), nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{}).Do(req) //nolint:gosec // 集群内网 HTTP（VPC-only 边界，ADR-0041）
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("cadvisor endpoint status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32MiB 上界（超大家庭防御）
}

// parseCadvisor 解析 exposition 文本为评估样本（最小解析面：只认三个
// metric 家族 + 平台容器标签；非 /docker/ 载体的 cgroup 聚合行跳过）。
// 同步维护 CPU 差分基面与节点核数观测。
func (e *Engine) parseCadvisor(nodeID string, body []byte, now time.Time) []containerSample {
	byContainer := map[string]*containerSample{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, ok := parseExpositionLine(line)
		if !ok {
			continue
		}
		switch name {
		case "machine_cpu_cores":
			if v, err := strconv.ParseFloat(value, 64); err == nil && v > 0 {
				e.metricsDom.cores[nodeID] = v
			}
		case "container_cpu_usage_seconds_total", "container_memory_working_set_bytes":
			id := labels["id"]
			app := labels["container_label_fleetly_ns_app"]
			if !strings.HasPrefix(id, "/docker/") || app == "" {
				continue // 平台 Workload 载体之外（cgroup 聚合/系统容器）
			}
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
			key := nodeID + "|" + id
			s := byContainer[id]
			if s == nil {
				s = &containerSample{node: nodeID, app: app, id: id}
				byContainer[id] = s
			}
			if name == "container_memory_working_set_bytes" {
				s.memBytes = v
				continue
			}
			// CPU counter → 率（差分基面；首拍无率）。
			prev, had := e.metricsDom.lastCPU[key]
			e.metricsDom.lastCPU[key] = cpuCounter{value: v, at: now}
			if !had || now.Sub(prev.at) <= 0 {
				continue
			}
			rate := (v - prev.value) / now.Sub(prev.at).Seconds()
			if rate < 0 {
				continue // 容器重启计数回零
			}
			s.cpuRate = rate
		}
	}
	cores := e.metricsDom.cores[nodeID]
	out := make([]containerSample, 0, len(byContainer))
	for _, s := range byContainer {
		if cores > 0 {
			s.cpuRate = s.cpuRate / cores * 100 // 百分比形态（docker stats 同口径）
		}
		out = append(out, *s)
	}
	return out
}

// parseExpositionLine 解析一行 exposition：name{labels} value [ts]。
// 标签值按 Prometheus 文本格式转义（\\ \" \n）。注释行（# 前缀）在此拒收
// （调用方也跳过——双保险）。
func parseExpositionLine(line string) (name string, labels map[string]string, value string, ok bool) {
	if strings.HasPrefix(line, "#") {
		return "", nil, "", false
	}
	open := strings.IndexByte(line, '{')
	if open < 0 {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return "", nil, "", false
		}
		return parts[0], nil, parts[1], true
	}
	closeIdx := strings.LastIndexByte(line, '}')
	if closeIdx < open {
		return "", nil, "", false
	}
	name = line[:open]
	labels = parseExpositionLabels(line[open+1 : closeIdx])
	rest := strings.Fields(strings.TrimSpace(line[closeIdx+1:]))
	if len(rest) == 0 {
		return "", nil, "", false
	}
	return name, labels, rest[0], true
}

// parseExpositionLabels 解析 label 集（k="v" 逗号连接；值内转义还原）。
// 诚实边界：不处理值内嵌入引号/逗号（fleetly 标签域——ULID/消毒名/
// 镜像引用——不产生；遇即原样透传不误读键）。
func parseExpositionLabels(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range splitLabels(s) {
		k, v, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		v = strings.Trim(v, `"`)
		// 转义还原（Prometheus 文本格式）：\" → "，\\ → \（单趟非重叠）。
		v = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v)
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// splitLabels 按逗号切分（值内逗号被引号包裹——不切）。
func splitLabels(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && (i == 0 || s[i-1] != '\\'):
			inQuote = !inQuote
			b.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

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
	// App 聚合样本（max）。
	agg := map[string]map[string]float64{} // appID → metric → value
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
		value := agg[rule.AppID][rule.Metric]
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
	offsiteAbsent := e.opts.PlatformBackup == nil || e.opts.PlatformBackup.S3 == nil
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

// emitAlertEvent 落用户规则事件（outbox）。
func (e *Engine) emitAlertEvent(ctx context.Context, name string, rule *alertrule.Rule, value float64) {
	payload, _ := json.Marshal(alertEventPayload{
		RuleID: rule.ID, AppID: rule.AppID, Metric: rule.Metric,
		Threshold: rule.Threshold, Value: value, State: rule.State,
	})
	if _, err := e.outbox.Append(ctx, e.db.Runner(), name, "app", rule.AppID, payload); err != nil {
		e.log.Error("metrics step: emit event", "rule", rule.ID, "err", err)
	}
}

// emitSystemAlertEvent 落内置规则事件（aggregate=platform）。
func (e *Engine) emitSystemAlertEvent(ctx context.Context, name string) {
	payload, _ := json.Marshal(alertEventPayload{
		RuleID: systemOffsiteRuleID, Metric: "platform_offsite_backup",
		State: name,
	})
	if _, err := e.outbox.Append(ctx, e.db.Runner(), name, "platform", "platform-backup", payload); err != nil {
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

// alertNotification 是通道载荷（webhook POST JSON / telegram 文本）。
type alertNotification struct {
	Type      string  `json:"type"` // alert | alert_test
	RuleID    string  `json:"rule_id"`
	AppID     string  `json:"app_id,omitempty"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold,omitempty"`
	Value     float64 `json:"value,omitempty"`
	State     string  `json:"state"` // firing | ok | test
	At        string  `json:"at"`
}

// dispatchAlert 把一次用户规则迁移派发到全部启用通道（失败记行不阻断）。
func (e *Engine) dispatchAlert(ctx context.Context, rule *alertrule.Rule, value float64, now time.Time) {
	e.dispatchNotification(ctx, alertNotification{
		Type: "alert", RuleID: rule.ID, AppID: rule.AppID, Metric: rule.Metric,
		Threshold: rule.Threshold, Value: value, State: rule.State, At: state.FormatTime(now),
	})
}

// dispatchSystemAlert 是内置规则的派发面。
func (e *Engine) dispatchSystemAlert(ctx context.Context, stateStr string, now time.Time) {
	e.dispatchNotification(ctx, alertNotification{
		Type: "alert", RuleID: systemOffsiteRuleID, Metric: "platform_offsite_backup",
		State: stateStr, At: state.FormatTime(now),
	})
}

// TestNotificationChannel 是 test 动词的 engine 面（API 调用；一次性载荷）。
func (e *Engine) TestNotificationChannel(ctx context.Context, channelID string) error {
	ch, err := e.channels.Get(ctx, e.db.Runner(), channelID)
	if err != nil {
		return err
	}
	return e.deliverOne(ctx, ch, alertNotification{
		Type: "alert_test", RuleID: "test", Metric: "test", State: "test",
		At: state.FormatTime(e.clock.Now()),
	})
}

// dispatchNotification 派发到全部启用通道。
func (e *Engine) dispatchNotification(ctx context.Context, n alertNotification) {
	chs, err := e.channels.ListEnabled(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("metrics step: list channels", "err", err)
		return
	}
	for i := range chs {
		_ = e.deliverOne(ctx, &chs[i], n)
	}
}

// deliverOne 投递一条通道（webhook POST JSON / telegram sendMessage）。
func (e *Engine) deliverOne(ctx context.Context, ch *channel.Channel, n alertNotification) error {
	err := e.deliverChannel(ctx, ch, n)
	failure := ""
	if err != nil {
		failure = err.Error()
		if payload, merr := json.Marshal(alertChannelFailedPayload{ChannelID: ch.ID, Error: failure}); merr == nil {
			if _, aerr := e.outbox.Append(ctx, e.db.Runner(), eventAlertChannelFail, "channel", ch.ID, payload); aerr != nil {
				e.log.Error("metrics step: emit channel failure", "channel", ch.ID, "err", aerr)
			}
		}
	}
	if derr := e.channels.RecordDelivery(ctx, e.db.Runner(), ch.ID, failure); derr != nil {
		e.log.Error("metrics step: record delivery", "channel", ch.ID, "err", derr)
	}
	return err
}

// channelConfigJSON 是通道配置信封的明文形态。
type channelConfigJSON struct {
	URL      string `json:"url,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

// deliverChannel 按类别投递（配置经 Cipher 解封——URL/token 不落日志）。
func (e *Engine) deliverChannel(ctx context.Context, ch *channel.Channel, n alertNotification) error {
	if e.cipher == nil {
		return fmt.Errorf("notification channels need the secret cipher (platform misconfigured)")
	}
	plain, err := e.cipher.Open(ch.ConfigCiphertext)
	if err != nil {
		return fmt.Errorf("open channel config: %w", err)
	}
	var cfg channelConfigJSON
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return fmt.Errorf("channel config malformed: %w", err)
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch ch.Kind {
	case channel.KindWebhook:
		if cfg.URL == "" {
			return fmt.Errorf("webhook channel has no url configured")
		}
		body, _ := json.Marshal(n)
		req, rerr := http.NewRequestWithContext(dctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
		if rerr != nil {
			return rerr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "fleetly-alerting")
		return httpDo(dctx, req, cfg.URL)
	case channel.KindTelegram:
		if cfg.BotToken == "" || cfg.ChatID == "" {
			return fmt.Errorf("telegram channel needs bot_token and chat_id")
		}
		text := fmt.Sprintf("fleetly alert %s: %s %s (value %.2f, threshold %.2f) on app %s",
			n.State, n.RuleID, n.Metric, n.Value, n.Threshold, n.AppID)
		if n.Type == "alert_test" {
			text = "fleetly notification channel test: delivered"
		}
		form := url.Values{"chat_id": {cfg.ChatID}, "text": {text}}
		req, rerr := http.NewRequestWithContext(dctx, http.MethodPost,
			"https://api.telegram.org/bot"+cfg.BotToken+"/sendMessage",
			strings.NewReader(form.Encode()))
		if rerr != nil {
			return rerr
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return httpDo(dctx, req, "telegram api")
	default:
		return fmt.Errorf("unknown channel kind %q", ch.Kind)
	}
}

// httpDo 执行请求并核对 2xx（错误片段有界）。
func httpDo(_ context.Context, req *http.Request, what string) error {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("deliver to %s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("deliver to %s: status %d: %s", what, resp.StatusCode, string(snippet))
	}
	return nil
}

// SystemAlertStates 返回内置规则的现行状态（ListAlertStates 消费）。
type SystemAlertState struct {
	RuleID     string
	State      string
	StateSince time.Time
	System     bool
}

// SystemOffsiteAlertState 是内置规则的观测面（server 组装 states 用）。
func (e *Engine) SystemOffsiteAlertState() SystemAlertState {
	st := alertrule.StateOK
	since := time.Time{}
	if e.metricsDom.sysOffsiteFiring {
		st = alertrule.StateFiring
	}
	if !e.metricsDom.sysOffsiteAbove.IsZero() {
		since = e.metricsDom.sysOffsiteAbove
	}
	return SystemAlertState{RuleID: systemOffsiteRuleID, State: st, StateSince: since, System: true}
}

// NewAlertRuleID 铸规则 ID（API 受理面用）。
func NewAlertRuleID() string { return ulid.Make().String() }

// AlertRuleRepo / NotificationChannelRepo 是告警面 repo 访问器（API 受理面
// 经 commit 原语持 tx 写行——repo 的 Runner 参数即 tx）。
func (e *Engine) AlertRuleRepo() *alertrule.Repo         { return e.alertRules }
func (e *Engine) NotificationChannelRepo() *channel.Repo { return e.channels }

// MetricsProvider 返回受管指标存储（nil = Metrics 面停用；API 查询面消费，
// ADR-0041）。
func (e *Engine) MetricsProvider() capability.Metrics { return e.metrics }
