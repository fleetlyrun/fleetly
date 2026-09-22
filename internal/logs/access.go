package logs

// 访问日志采集（E6 观测专项设计 §3.2，W5-S2）：平台入口服务
//（fleetly-ingress，traefik global 服务）的服务日志轮询 → JSON 访问行
// 识别 → RouterName 反解 app/service（ingress 命名公式 + 候选匹配——
// app/service 含 '-' 时字符串切分有歧义，候选匹配是唯一确定性反解）→
// Entry{Source: access} + 结构化 Fields → 只进入湖批量器。
//
// 零改动条款（设计 §2.4 延续）：访问行**不进 ring、不落 JSONL**——直播
// 面（FollowLogs）与磁盘历史不感知本采集器的存在；量级与用途不同，直播
// 面不加噪。backend=jsonl 时无消费方，采集整体跳过（与 JSONL 边界一致）。
//
// 部署归因：Fields[deployment_id] = 该 app 最近 succeeded 部署（滚动窗内
// **近似**语义——多副本滚动窗内外流量可能分属新旧两代部署，不声称精确；
// 精确到 task 的蓝绿归因挂 v0.3）。查询经短 TTL 缓存收敛（逐行查库不可
// 接受，逐轮查库仍嫌密）。
//
// 脱敏：访问行经该 app 的同一 redactor（URL query 可能带凭证——既有纪律
// 覆盖；Line 摘要与 host/path 字段均过 redactor）。
//
// 入口侧输入面：traefik 静态参数 --accesslog=true --accesslog.format=json
// （internal/ingress buildTraefikSpec，spec 漂移比对覆盖——存量部署自动
// 收敛）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// accessIngressService 是访问日志的采集对象（平台入口服务，契约 =
// internal/ingress.IngressServiceName；本包不反向 import ingress——字符串
// 由 access_ingress_test.go 对照 ingress.IngressServiceName 钉死）。
const accessIngressService = "fleetly-ingress"

// accessRouterName 是 app 路由键公式（与 internal/ingress.RouterName 同式：
// `fleetly-<app>-<service>`——ingress 动态配置的路由/服务键，traefik access
// JSON 的 RouterName 剥离 provider 后缀与入口后缀后即此键。公式原文在
// ingress；本包不反向 import——一致性由 access_routername_test.go 对照
// ingress.RouterName 输出钉死，单边改式即测试红）。
func accessRouterName(app, service string) string {
	return "fleetly-" + app + "-" + service
}

// accessTarget 是一条访问行的归属（反解产物）：app 行（携带 ID——脱敏值集
// 与部署归因的查询键）+ compose 服务名（Entry.Service 投影）。
type accessTarget struct {
	app     state.App
	service string
}

// traefikAccess 是 traefik access JSON 行的识别字段投影（v3 JSON 格式；
// 字段缺省容忍——非访问行/字段缺失的行识别失败即跳过）。字段名对齐
// traefik v3.5 access log 实测形态：RouterName/DownstreamStatus/
// RequestURI/RequestMethod/Duration(纳秒)/ClientAddr/StartUTC；RequestPath
// 为旧形态兜底键（识别逻辑对两者同权）。
type traefikAccess struct {
	RouterName       string      `json:"RouterName"`
	DownstreamStatus json.Number `json:"DownstreamStatus"`
	RequestMethod    string      `json:"RequestMethod"`
	RequestHost      string      `json:"RequestHost"`
	RequestAddr      string      `json:"RequestAddr"`
	RequestURI       string      `json:"RequestURI"`
	RequestPath      string      `json:"RequestPath"`
	Duration         json.Number `json:"Duration"`
	ClientAddr       string      `json:"ClientAddr"`
	StartUTC         string      `json:"StartUTC"`
}

// accessLineOf 把一条服务日志行解析为访问行（第二返回值 false = 非访问行
// ——traefik 运行日志/启动横幅/坏 JSON——调用方跳过并计数）。识别判据：
// JSON 对象 + RouterName 非空 + DownstreamStatus 可解析（运行日志两者皆
// 缺；仅凭 RouterName 会把引用路由名的运行日志误收）。
func accessLineOf(line string) (traefikAccess, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '{' {
		return traefikAccess{}, false
	}
	var acc traefikAccess
	if err := json.Unmarshal([]byte(line), &acc); err != nil {
		return traefikAccess{}, false
	}
	if acc.RouterName == "" || acc.DownstreamStatus.String() == "" {
		return traefikAccess{}, false
	}
	if _, err := acc.DownstreamStatus.Int64(); err != nil {
		return traefikAccess{}, false
	}
	return acc, true
}

// stripAccessRouterKey 把 access 行的 RouterName 归一为路由键：剥离
// provider 限定后缀（`@http` 等——HTTP provider 的路由名在 access 行里带
// provider 限定）与入口后缀（`-websecure` 优先于 `-web`——前者是后者的
// 超集后缀，次序不可换）。结果即 accessRouterName(app, service) 键。
func stripAccessRouterKey(raw string) string {
	if idx := strings.LastIndexByte(raw, '@'); idx >= 0 {
		raw = raw[:idx]
	}
	if suffix := "-websecure"; strings.HasSuffix(raw, suffix) {
		raw = raw[:len(raw)-len(suffix)]
	} else if suffix := "-web"; strings.HasSuffix(raw, suffix) {
		raw = raw[:len(raw)-len(suffix)]
	}
	return raw
}

// accessRouteOf 反解 RouterName → 归属（app, service）：归一化后的键与
// 候选集（本轮 active apps × 各自受管服务按公式构造）逐一比对——app/
// service 含 '-' 时无候选集的字符串切分有歧义，候选匹配是唯一确定性反解。
// 平台路由段（registry/fallback/ACME 挑战）不在候选集，自然跳过并计数。
func accessRouteOf(routerName string, candidates map[string]accessTarget) (accessTarget, bool) {
	t, ok := candidates[stripAccessRouterKey(routerName)]
	return t, ok
}

// ── 部署归因（TTL 缓存）───────────────────────────────────────────────────

// depAttributionTTL 是部署归因的缓存窗：窗内同 app 复用上次查询结果（逐行
// 查库不可接受）。窗 = 短于典型滚动部署时长、远长于扫描周期——归因的近似
// 语义（滚动窗内流量可能跨两代部署）在字段注释与设计文档明示，缓存窗只
// 影响新旧交替边界处的滞后，不改变语义上限。
const depAttributionTTL = 30 * time.Second

// depAttributor 是部署归因的短 TTL 缓存（key = app ID）。lookup 可注入
// 替换（单测数查询次数钉缓存行为）；clock 同理。
type depAttributor struct {
	lookup  func(ctx context.Context, appID string) (string, error)
	ttl     time.Duration
	clock   func() time.Time
	mu      sync.Mutex
	entries map[string]depAttributionEntry
	queries int
}

type depAttributionEntry struct {
	id string
	at time.Time
}

func newDepAttributor(st *state.Store) *depAttributor {
	return &depAttributor{
		lookup: func(ctx context.Context, appID string) (string, error) {
			return st.LatestSucceededDeploymentID(ctx, appID)
		},
		ttl:     depAttributionTTL,
		clock:   time.Now,
		entries: make(map[string]depAttributionEntry),
	}
}

// get 返回该 app 当前生效部署 ID（TTL 窗内复用缓存；无成功部署 = 空串，
// 同样进缓存——省掉「从未部署成功」app 的重复查库）。查询失败返回错误
// （调用方按无归因处理并下轮重试，不缓存失败）。
func (d *depAttributor) get(ctx context.Context, appID string) (string, error) {
	d.mu.Lock()
	if ent, ok := d.entries[appID]; ok && d.clock().Sub(ent.at) < d.ttl {
		d.mu.Unlock()
		return ent.id, nil
	}
	d.mu.Unlock()

	id, err := d.lookup(ctx, appID)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.queries++
	d.entries[appID] = depAttributionEntry{id: id, at: d.clock()}
	d.mu.Unlock()
	return id, nil
}

// ── 采集路径 ─────────────────────────────────────────────────────────────

// pollAccess 拉取入口服务日志的本轮增量并按访问行入湖（scanOnce 尾部调用；
// backend != victorialogs 时整体跳过——jsonl 模式无消费方，采集即纯浪费）。
// candidates 是本轮 active apps 的路由键候选集（scanOnce 顺路构造，零额外
// 发现开销）。游标 = 单条平台流 since（独立于 per-app 游标表——app 级延迟
// 淘汰不适用平台服务）；首轮锚点 = 发现时刻（不回灌历史，长驻服务口径）。
func (m *Manager) pollAccess(ctx context.Context, candidates map[string]accessTarget) {
	if m.ing == nil || !m.vlEnabled() {
		return
	}
	m.mu.Lock()
	if !m.accStarted {
		m.accStarted = true
		m.accSince = m.clock()
	}
	since := m.accSince
	m.mu.Unlock()

	lines, err := m.port.StreamServiceLogs(ctx, accessIngressService, since, false)
	if err != nil {
		// 服务不存在（首次创建前的稳态）/底座暂态：安静跳过下轮再试。
		m.log.Debug("logs: access stream open failed", "service", accessIngressService, "error", err.Error())
		return
	}
	// MG-1 同款纵深防御：单轮看门狗（轮询流恒会排水结束，此处只兜极端面）。
	watchdog := time.NewTimer(m.pollWatchdog())
	defer watchdog.Stop()
	var last time.Time
deliver:
	for {
		select {
		case <-ctx.Done():
			return
		case <-watchdog.C:
			m.log.Error("logs: access poll round exceeded watchdog deadline, abandoning this round",
				"service", accessIngressService)
			return
		case line, ok := <-lines:
			if !ok {
				break deliver
			}
			if at := line.At; at.After(last) {
				last = at
			}
			m.ingestAccessLine(ctx, lineAt{text: line.Line, at: line.At}, candidates)
		}
	}
	if !last.IsZero() {
		m.mu.Lock()
		if last.After(m.accSince) {
			m.accSince = last
		}
		m.mu.Unlock()
	}
}

// bumpAccessSkipped 计一次跳过（非访问行 / 反解失败）+ Debug 留痕（跳过是
// 预期路径，不红不静默计数即可）。
func (m *Manager) bumpAccessSkipped(reason string, sample string) {
	m.mu.Lock()
	m.accSkipped++
	m.mu.Unlock()
	m.log.Debug("logs: access line skipped", "reason", reason, "sample", truncateSample(sample))
}

// truncateSample 是跳过计数的取证样本（单行限长——日志行不进观测面原文）。
func truncateSample(s string) string {
	s = strings.TrimSpace(s)
	const max = 120
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// ingestAccessLine 把一条入口服务日志行按访问行采集：识别 → 反解归属 →
// 脱敏 → 部署归因 → Entry{Source: access} 只进批量器（不进 ring、不落
// JSONL——本函数不存在 m.hub/m.dsk 的任何调用路径，由测试钉死）。
func (m *Manager) ingestAccessLine(ctx context.Context, line lineAt, candidates map[string]accessTarget) {
	acc, ok := accessLineOf(line.text)
	if !ok {
		m.bumpAccessSkipped("not-access-json", line.text)
		return
	}
	target, ok := accessRouteOf(acc.RouterName, candidates)
	if !ok {
		// 平台路由段（registry/fallback/ACME 挑战）与已下线 app 的路由：
		// 无归属即不采（访问日志面只服务 app 检索）。
		m.bumpAccessSkipped("route-unresolved", acc.RouterName)
		return
	}
	e := accessEntryOf(acc, target, line.at, m.red.forApp(ctx, target.app.ID))
	if depID, err := m.dep.get(ctx, target.app.ID); err != nil {
		// 归因查询失败 = 无归因诚实呈现（不伪造、不阻塞采集）。
		m.log.Debug("logs: deployment attribution lookup failed", "app", target.app.Name, "error", err.Error())
	} else if depID != "" {
		e.Fields[FieldDeploymentID] = depID
	}
	m.ing.Add(e)
}

// lineAt 是采集行的统一输入形态（真实路径 = substrate.LogLine 的投影；
// 独立小结构让解析与归属逻辑可 hermetic 单测，不必起 fake 底座流）。
type lineAt struct {
	text string
	at   time.Time
}

// accessEntryOf 把识别后的访问行组装为入湖 Entry：Line = 紧凑摘要
//（method status host path duration），Fields = 结构化字段（host/path 过
// 同一 redactor——URL query 可能带凭证）。At 优先取底座行时间戳（docker
// 记录时间），缺失回退行内 StartUTC。
func accessEntryOf(acc traefikAccess, target accessTarget, at time.Time, red *redactor) Entry {
	if at.IsZero() {
		if ts, err := time.Parse(time.RFC3339, acc.StartUTC); err == nil {
			at = ts
		}
	}
	host := firstNonEmpty(acc.RequestHost, hostOnly(acc.RequestAddr))
	path := firstNonEmpty(acc.RequestURI, acc.RequestPath)
	ms := durationMS(acc.Duration)
	summary := strings.Join([]string{
		firstNonEmpty(acc.RequestMethod, "-"),
		firstNonEmpty(acc.DownstreamStatus.String(), "-"),
		firstNonEmpty(host, "-"),
		firstNonEmpty(path, "-"),
		fmt.Sprintf("%dms", ms),
	}, " ")
	fields := map[string]string{
		FieldMethod:     acc.RequestMethod,
		FieldStatus:     acc.DownstreamStatus.String(),
		FieldHost:       red.redact(host),
		FieldPath:       red.redact(path),
		FieldRoute:      acc.RouterName,
		FieldDurationMS: strconv.Itoa(ms),
	}
	if ip := clientIP(acc.ClientAddr); ip != "" {
		fields[FieldClientIP] = ip
	}
	return Entry{
		App:     target.app.Name,
		Service: target.service,
		At:      at,
		Line:    red.redact(summary),
		Source:  SourceAccess,
		Fields:  fields,
	}
}

// durationMS 折算 traefik Duration（纳秒）为毫秒整数（0/不可解析 = 0——
// 诚实缺失，不伪造时长）。
func durationMS(n json.Number) int {
	if n.String() == "" {
		return 0
	}
	if f, err := n.Float64(); err == nil {
		return int(f / float64(time.Millisecond))
	}
	return 0
}

// clientIP 从 ClientAddr（"ip:port" 形态）剥出 IP（不可解析 = 空串）。
func clientIP(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// hostOnly 从 RequestAddr（host:port 形态）剥出 host。
func hostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
