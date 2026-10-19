package fleetlygrpc

// Alerting/Metrics 上下文服务（F2.5，ADR-0041）：MetricsService 查询面
//（PromQL 透传——行级过滤责任在调用方的查询构造，本面持平台凭证）与
// AlertingService 配置面（通道平台级 + 规则 per-App 行级授权链；写面走
// commit 原语。配置 CRUD 事件面：四族补事件批（P2 批二）起 CRUD 事实
// 默认进流——与 alert.fired/resolved 的状态迁移沿分立，ADR-0041）。

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// 告警配置面事件名（usage 反扫的字面量锚点；四族 CRUD 补事件批——P2 批二
// 声明级遗漏收口：配置 CRUD 此前只有审计无事件。通道是平台级材料（无
// owner Team 域）——” 全员可见；规则随 App 归属解析资源轴 Team）。
const (
	eventAlertRuleCreated = "alert_rule.created"
	eventAlertRuleDeleted = "alert_rule.deleted"
	eventChannelCreated   = "channel.created"
	eventChannelDeleted   = "channel.deleted"
)

// alertRuleEventPayload 是 alert_rule.* 事件的载荷（字段只增）。
type alertRuleEventPayload struct {
	AppID  string `json:"app_id"`
	Metric string `json:"metric"`
}

// channelEventPayload 是 channel.* 事件的载荷（字段只增；凭证材料永不
// 出现——配置 age 信封只写不读。deleted 时不载荷——删除面无行读）。
type channelEventPayload struct {
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// MetricsService 是指标查询面。
type MetricsService struct {
	telemetryv1.UnimplementedMetricsServiceServer
	s *Services
}

// QueryMetrics 透传 PromQL 到受管 VM（平台管理员诊断面；Metrics 面停用时
// 精确失败）。
//
// 授权（H8 阶段 A 止血，设计文档 docs/design/2026-10-19-security-backlog-
// design.md §5.2）：raw PromQL 让调用方自带表达式与标签，服务端不引入
// PromQL 解析器就无法约束行级作用域；单存储 + 平台凭证下这是跨 Team
// 遥测读，故本面收归平台管理员（default Team 的 builtin-owner）。门在
// Metrics nil 检查之前：非平台管理员不得用 403/503 差异探测存储是否启用，
// 拒绝零副作用。
//
// 阶段 B（H8）：租户图表面走 QueryScopedMetrics（服务端固定模板 + 行级
// 授权）；本函数保留为平台管理员诊断面，仍受体/序列/超时上限。
func (svc *MetricsService) QueryMetrics(ctx context.Context, req *telemetryv1.QueryMetricsRequest) (*telemetryv1.QueryMetricsResponse, error) {
	if err := RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	if svc.s.Metrics == nil {
		// 未配 metrics.addr 是显式关态（P4-3 批 3）：精确 503，不再落
		// E_INTERNAL 脱敏 500。
		return nil, apperr.New("E_METRICS_STORE_UNAVAILABLE", "metrics queries require the managed metrics store; set config metrics.addr to enable it")
	}
	// query 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致；start/end
	// 时间窗是动态文案（RFC3339 解析带 got 回显），保留手写。
	start, end, err := parseMetricsWindow(req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, err
	}
	// raw 步长：与 scoped 共用符号校验，但诊断面不钳制 24h/点数（管理员
	// 可查长窗；体/序列/超时上限兜底）。
	if req.GetStepSeconds() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "step_seconds: must not be negative (got %d)", req.GetStepSeconds())
	}
	step := time.Duration(req.GetStepSeconds()) * time.Second
	if step == 0 {
		step = capability.ScopedMetricsMinStep
	}
	series, err := svc.s.Metrics.QuerySeries(ctx, req.GetQuery(), start, end, step)
	if err != nil {
		return nil, mapStateError(err, "metrics")
	}
	return &telemetryv1.QueryMetricsResponse{Series: metricSeriesMsgs(series)}, nil
}

// scopedProcessPattern 是 process 词法的受理面形态（与 Process 名字符集
// 同源：小写字母数字与内部连字符，首尾字母数字；设计文档 §5.2）。
var scopedProcessPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// QueryScopedMetrics 是租户图表面（H8 阶段 B）：调用方只给 scope（App 或
// Database 恰一）+ 指标白名单 + 时间窗；PromQL 由 capability 固定模板
// 构造，服务端不接受表达式。
//
// 处理顺序（设计文档 §5.2；未授权调用方不得用 503/400 差异探测资源）：
// protovalidate（拦截器）→ 行级授权 → Metrics store 状态 → process/
// database+cpu_percent 动态校验 → 窗口/step 钳制 → QueryScoped → 映射。
func (svc *MetricsService) QueryScopedMetrics(ctx context.Context, req *telemetryv1.QueryScopedMetricsRequest) (*telemetryv1.QueryScopedMetricsResponse, error) {
	q := capability.ScopedQuery{Metric: req.GetMetric()}
	var projectID string
	switch {
	case req.GetAppId() != "":
		// project_id 不由客户端给：从 App 行解析，避免"伪造 project +
		// 合法 app"的组合；行不存在 404 / 他队 403 与既有单源同源。
		a, err := svc.s.authorizeAppID(ctx, req.GetAppId())
		if err != nil {
			return nil, err
		}
		projectID = a.ProjectID
		q.AppID = a.ID
	case req.GetDatabaseId() != "":
		db, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
		if err != nil {
			return nil, mapStateError(err, "database")
		}
		if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
			return nil, err
		}
		projectID = db.ProjectID
		q.DatabaseID = db.ID
	default:
		// protovalidate 的 message CEL 已在受理面拦截（scope 恰一）；防御
		// 分支保持同一可编程错误。
		return nil, apperr.New("E_INVALID_ARGUMENT", "exactly one of app_id or database_id must be set")
	}
	// store 状态在授权之后暴露（503 不泄漏资源存在性）。
	if svc.s.Metrics == nil {
		return nil, apperr.New("E_METRICS_STORE_UNAVAILABLE", "metrics queries require the managed metrics store; set config metrics.addr to enable it")
	}
	// 动态参数校验：database 页没有 process/cpu_percent 消费面，显式拒绝
	// 而非静默忽略（设计文档 §5.7）。
	if req.GetProcess() != "" {
		if q.DatabaseID != "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "process: only available for app scope (database scope has no process dimension)")
		}
		if !scopedProcessPattern.MatchString(req.GetProcess()) {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"process: must be a dns label (lowercase letters, digits, inner hyphens; start and end alphanumeric, got %q)", req.GetProcess())
		}
		q.Process = req.GetProcess()
	}
	if q.DatabaseID != "" && q.Metric == capability.MetricCPUPercent {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"metric: cpu_percent is only available for app scope (database scope supports %s and %s)",
			capability.MetricCPUCores, capability.MetricMemoryWorkingSet)
	}
	start, end, err := parseMetricsWindow(req.GetStart(), req.GetEnd())
	if err != nil {
		return nil, err
	}
	step, err := capability.ClampScopedStep(start, end, req.GetStepSeconds())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "%s", err.Error())
	}
	q.ProjectID = projectID
	q.Start, q.End, q.Step = start, end, step
	q.MaxSeries = capability.ScopedMetricsMaxSeries
	page, err := svc.s.Metrics.QueryScoped(ctx, q)
	if err != nil {
		// scoped 模板是服务端固定的：VM 4xx 是平台缺陷，不能沿用 raw 的
		// E_INVALID_ARGUMENT（调用方无法修模板）；带 cause 供排查。
		var qe *capability.MetricsQueryError
		if errors.As(err, &qe) {
			return nil, apperr.New("E_INTERNAL",
				"scoped metrics query was rejected by the metrics store (server-owned template): %s", qe.Message).WithCause(err)
		}
		// 超体/超序列与超时走共享映射（E_METRICS_RESULT_TOO_LARGE /
		// E_METRICS_QUERY_TIMEOUT）。
		return nil, mapStateError(err, "metrics")
	}
	return &telemetryv1.QueryScopedMetricsResponse{
		Series:      metricSeriesMsgs(page.Series),
		Truncated:   page.Truncated,
		SeriesTotal: int32(page.Total), //nolint:gosec // 命中数受 VM 响应体上限约束（远小于 int32）
	}, nil
}

// parseMetricsWindow 解析 raw/scoped 共用的时间窗（RFC3339；空 end = now，
// 空 start = end 前 1h），并校验 start < end。
func parseMetricsWindow(startRaw, endRaw string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	if raw := endRaw; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "end: must be RFC3339 (got %q)", raw)
		}
		end = t
	}
	start := end.Add(-time.Hour)
	if raw := startRaw; raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "start: must be RFC3339 (got %q)", raw)
		}
		start = t
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, apperr.New("E_INVALID_ARGUMENT", "start: must be before end")
	}
	return start, end, nil
}

// metricSeriesMsgs 是 capability.Series → proto 传输形态（raw/scoped 共用）。
func metricSeriesMsgs(series []capability.Series) []*telemetryv1.MetricSeries {
	out := make([]*telemetryv1.MetricSeries, 0, len(series))
	for _, s := range series {
		msg := &telemetryv1.MetricSeries{Labels: s.Metric}
		for _, p := range s.Points {
			msg.Points = append(msg.Points, &telemetryv1.MetricPoint{
				Time:  p.Time.Format(time.RFC3339),
				Value: p.Value,
			})
		}
		out = append(out, msg)
	}
	return out
}

// AlertingService 是告警配置面。
type AlertingService struct {
	telemetryv1.UnimplementedAlertingServiceServer
	s *Services
}

// CreateNotificationChannel 登记通道（配置 age 信封入库——URL/token 只写
// 不读；重名 E_ALREADY_EXISTS）。
func (svc *AlertingService) CreateNotificationChannel(ctx context.Context, req *telemetryv1.CreateNotificationChannelRequest) (*telemetryv1.CreateNotificationChannelResponse, error) {
	// name 必填 + dns 标签形态已退役 descriptor 注解（P4-1 批 1 Run B2）：
	// 空值报 required、坏形态报形状（空名短路不进形状），protovalidate
	// 拦截器受理面执法，文案与退役前逐字节一致；kind 值域与 url/bot_token
	// 条件必填是动态/条件文案，保留手写。
	name := req.GetName()
	kind := req.GetKind()
	var cfg alertChannelConfig
	switch kind {
	case channel.KindWebhook:
		if req.GetUrl() == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "url: required for a webhook channel")
		}
		cfg.URL = req.GetUrl()
	case channel.KindTelegram:
		if req.GetBotToken() == "" || req.GetChatId() == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT", "bot_token and chat_id: both required for a telegram channel")
		}
		cfg.BotToken, cfg.ChatID = req.GetBotToken(), req.GetChatId()
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "kind: must be webhook or telegram (got %q)", kind)
	}
	if svc.s.Cipher == nil {
		// 未配主密钥是显式关态（P4-3 批 3）：cipher 面缺席精确 503，不再落
		// E_INTERNAL 脱敏 500。括号注记 "(platform misconfigured)" 随码分流
		// 退役——off 态语义由 E_MASTER_KEY_UNAVAILABLE 的 Summary 承载。
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "notification channels need the secret cipher")
	}
	plain, err := json.Marshal(cfg)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "encode channel config").WithCause(err)
	}
	sealed, err := svc.s.Cipher.Seal(plain)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "seal channel config").WithCause(err)
	}
	row := &channel.Channel{
		Name: name, Kind: kind,
		ConfigCiphertext: sealed, Enabled: true,
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			return st.Channels.Create(ctx, row)
		},
		facts: []func() writes.FactRow{
			// 资源轴 ''（无 owner 域——平台级材料，全员可见）：通道是平台级
			// 配置（ADR-0041），行无 Team 轴。
			func() writes.FactRow {
				return writes.FactRow{
					Name: eventChannelCreated, Aggregate: "channel", ID: row.ID,
					Payload: mustJSON(channelEventPayload{Name: row.Name, Kind: row.Kind}),
					Action:  "channel.create", Resource: "channel/" + row.ID, AfterFP: kind + ":" + name,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
		},
	})
	if err != nil {
		if errors.Is(err, state.ErrAlreadyExists) {
			return nil, apperr.New("E_ALREADY_EXISTS", "a notification channel named %q already exists", name)
		}
		return nil, mapStateError(err, "channel")
	}
	return &telemetryv1.CreateNotificationChannelResponse{Channel: channelMsg(row)}, nil
}

// TestNotificationChannel 即时派发测试载荷（验收锚；失败原文回显）。
func (svc *AlertingService) TestNotificationChannel(ctx context.Context, req *telemetryv1.TestNotificationChannelRequest) (*telemetryv1.TestNotificationChannelResponse, error) {
	// channel_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	if err := svc.s.Engine.TestNotificationChannel(ctx, req.GetChannelId()); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "notification channel %s not found", req.GetChannelId())
		}
		return &telemetryv1.TestNotificationChannelResponse{Delivered: false, Error: err.Error()}, nil
	}
	return &telemetryv1.TestNotificationChannelResponse{Delivered: true}, nil
}

// ListNotificationChannels 列通道（配置载荷永不回显——凭证单向）。
func (svc *AlertingService) ListNotificationChannels(ctx context.Context, _ *telemetryv1.ListNotificationChannelsRequest) (*telemetryv1.ListNotificationChannelsResponse, error) {
	chs, err := svc.s.Engine.NotificationChannelRepo().ListAll(ctx)
	if err != nil {
		return nil, mapStateError(err, "channel")
	}
	out := &telemetryv1.ListNotificationChannelsResponse{}
	for i := range chs {
		out.Channels = append(out.Channels, channelMsg(&chs[i]))
	}
	return out, nil
}

// DeleteNotificationChannel 删通道（受理：行在场）。
func (svc *AlertingService) DeleteNotificationChannel(ctx context.Context, req *telemetryv1.DeleteNotificationChannelRequest) (*telemetryv1.DeleteNotificationChannelResponse, error) {
	// channel_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			return st.Channels.Delete(ctx, req.GetChannelId())
		},
		facts: []func() writes.FactRow{
			// 资源轴 ''（无 owner 域——平台级材料，全员可见）：通道是平台级
			// 配置（ADR-0041），行无 Team 轴。删除面无行读（ErrNotFound 映射
			// 由调用侧承担）——载荷留空。
			func() writes.FactRow {
				return writes.FactRow{
					Name: eventChannelDeleted, Aggregate: "channel", ID: req.GetChannelId(),
					// outbox.payload 非空约束：删除面无行读，载荷为空对象。
					Payload: mustJSON(channelEventPayload{}),
					Action:  "channel.delete", Resource: "channel/" + req.GetChannelId(),
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
		},
	})
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "notification channel %s not found", req.GetChannelId())
		}
		return nil, mapStateError(err, "channel")
	}
	return &telemetryv1.DeleteNotificationChannelResponse{}, nil
}

// CreateAlertRule 登记规则（App 行级授权 + metric 值域执法）。
func (svc *AlertingService) CreateAlertRule(ctx context.Context, req *telemetryv1.CreateAlertRuleRequest) (*telemetryv1.CreateAlertRuleResponse, error) {
	// app_id 必填与 threshold 非负已退役 descriptor 注解（P4-1 批 1 Run
	// B2）：空值/负值由 protovalidate 拦截器在受理面拦截，文案与退役前逐
	// 字节一致；metric 值域是动态文案（got 回显），保留手写。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	metric := req.GetMetric()
	if metric != alertrule.MetricCPUPercent && metric != alertrule.MetricMemoryWorkingSet {
		return nil, apperr.New("E_INVALID_ARGUMENT", "metric: must be %s or %s (got %q)",
			alertrule.MetricCPUPercent, alertrule.MetricMemoryWorkingSet, metric)
	}
	row := &alertrule.Rule{
		AppID: appRow.ID, Metric: metric,
		Threshold: req.GetThreshold(), ForSecs: req.GetForSeconds(), Enabled: true,
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(appRow.ProjectID)},
		write: func(ctx context.Context, st transact.Stores) error {
			return st.AlertRules.Create(ctx, row)
		},
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, appRow.ProjectID, func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventAlertRuleCreated, Aggregate: "alert_rule", ID: row.ID,
					Payload: mustJSON(alertRuleEventPayload{AppID: row.AppID, Metric: row.Metric}),
					TeamID:  team,
					Action:  "alert_rule.create", Resource: "alert_rule/" + row.ID,
					AfterFP: appRow.ID + ":" + metric,
					Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	return &telemetryv1.CreateAlertRuleResponse{Rule: ruleMsg(row)}, nil
}

// ListAlertRules 列规则（app_id 非空 = 行级授权锚；空 = 按调用方 Team
// 过滤的全量可读面——R08-M2：规则行含他队 app_id/阈值，member 不得跨队
// 枚举）。
func (svc *AlertingService) ListAlertRules(ctx context.Context, req *telemetryv1.ListAlertRulesRequest) (*telemetryv1.ListAlertRulesResponse, error) {
	var rows []alertrule.Rule
	var err error
	if req.GetAppId() != "" {
		// 指定 App 时行级授权已把"他队 App"拒成 403，无需再做 List 面过滤。
		if _, err := svc.s.authorizeAppID(ctx, req.GetAppId()); err != nil {
			return nil, err
		}
		rows, err = svc.s.Engine.AlertRuleRepo().ListByApp(ctx, req.GetAppId())
	} else {
		rows, err = svc.s.Engine.AlertRuleRepo().ListAll(ctx)
		if err == nil {
			rows, err = svc.filterAlertRulesByTeam(ctx, rows)
		}
	}
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	out := &telemetryv1.ListAlertRulesResponse{}
	for i := range rows {
		out.Rules = append(out.Rules, ruleMsg(&rows[i]))
	}
	return out, nil
}

// filterAlertRulesByTeam 是告警规则 List 面的行级过滤核（R08-M2，规则
// 归属经 App → Project → Team）：平台管理员全量；其余调用者只保留本 Team
// Project 下 App 的规则。读路径两次批量查询（本队 Project 集 + 活跃 App
// 全量建 appID→projectID 表），不做逐规则 anchor 解析的 N+1；规则不随
// App tombstone 级联删除，活跃表未命中时用 GetAnyByID 兜底解析已删 App
// 的归属（"tombstone 是事实不是秘密"，ADR-0035）。App 行彻底缺失的孤儿
// 规则归属不可判定，非平台管理员不展示（fail-closed）。
func (svc *AlertingService) filterAlertRulesByTeam(ctx context.Context, rows []alertrule.Rule) ([]alertrule.Rule, error) {
	teamProjects, all, err := svc.s.teamProjectFilter(ctx)
	if err != nil {
		return nil, err
	}
	if all {
		return rows, nil
	}
	apps, err := svc.s.Apps.List(ctx)
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	projectOf := make(map[string]string, len(apps))
	for i := range apps {
		projectOf[apps[i].ID] = apps[i].ProjectID
	}
	visible := make(map[string]bool)
	out := make([]alertrule.Rule, 0, len(rows))
	for i := range rows {
		appID := rows[i].AppID
		allowed, seen := visible[appID]
		if !seen {
			projectID, ok := projectOf[appID]
			if !ok {
				a, err := svc.s.Apps.GetAnyByID(ctx, appID)
				if err != nil {
					if errors.Is(err, state.ErrNotFound) {
						visible[appID] = false
						continue
					}
					return nil, mapStateError(err, "app")
				}
				projectID = a.ProjectID
				projectOf[appID] = projectID
			}
			allowed = teamProjects[projectID]
			visible[appID] = allowed
		}
		if allowed {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// DeleteAlertRule 删规则（行级授权——App 归属比对；受理：行在场）。
func (svc *AlertingService) DeleteAlertRule(ctx context.Context, req *telemetryv1.DeleteAlertRuleRequest) (*telemetryv1.DeleteAlertRuleResponse, error) {
	// rule_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致。
	row, err := svc.s.Engine.AlertRuleRepo().Get(ctx, req.GetRuleId())
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "alert rule %s not found", req.GetRuleId())
		}
		return nil, mapStateError(err, "alert rule")
	}
	appRow, err := svc.s.authorizeAppID(ctx, row.AppID)
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			return st.AlertRules.Delete(ctx, req.GetRuleId())
		},
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, appRow.ProjectID, func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventAlertRuleDeleted, Aggregate: "alert_rule", ID: row.ID,
					Payload: mustJSON(alertRuleEventPayload{AppID: row.AppID, Metric: row.Metric}),
					TeamID:  team,
					Action:  "alert_rule.delete", Resource: "alert_rule/" + row.ID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	return &telemetryv1.DeleteAlertRuleResponse{}, nil
}

// ListAlertStates 列现行状态（用户规则行 + 内置系统行）。用户规则按
// 调用方 Team 过滤（R08-M2：状态行同样泄露他队 app_id）；内置系统行无
// Team 轴（平台级、行不落库），沿用既有全员可见语义——Console 的
// 平台告警卡依赖它们，不随本批收窄。
func (svc *AlertingService) ListAlertStates(ctx context.Context, _ *telemetryv1.ListAlertStatesRequest) (*telemetryv1.ListAlertStatesResponse, error) {
	rows, err := svc.s.Engine.AlertRuleRepo().ListAll(ctx)
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	rows, err = svc.filterAlertRulesByTeam(ctx, rows)
	if err != nil {
		return nil, err
	}
	out := &telemetryv1.ListAlertStatesResponse{}
	for i := range rows {
		r := &rows[i]
		out.States = append(out.States, &telemetryv1.AlertState{
			RuleId: r.ID, AppId: r.AppID, Metric: r.Metric,
			State: r.State, StateSince: r.StateSince, ObservedValue: r.LastValue,
		})
	}
	sys := svc.s.Engine.SystemOffsiteAlertState()
	since := ""
	if !sys.StateSince.IsZero() {
		since = sys.StateSince.UTC().Format(time.RFC3339)
	}
	out.States = append(out.States, &telemetryv1.AlertState{
		RuleId: sys.RuleID, Metric: "platform_offsite_backup",
		State: sys.State, StateSince: since, System: true,
	})
	// 内置收敛告警行（ADR-0082 §4：任一 App 处于收敛冷却窗即 firing——
	// platform-offsite-backup 先例对齐：不落行、不可删、doctor 可见）。
	cvg := svc.s.Engine.SystemConvergeAlertState()
	csince := ""
	if !cvg.StateSince.IsZero() {
		csince = cvg.StateSince.UTC().Format(time.RFC3339)
	}
	out.States = append(out.States, &telemetryv1.AlertState{
		RuleId: cvg.RuleID, Metric: "converge_failed",
		State: cvg.State, StateSince: csince, System: true,
	})
	return out, nil
}

// alertChannelConfig 是通道配置的明文信封形态（engine 派发侧同构）。
type alertChannelConfig struct {
	URL      string `json:"url,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
}

// channelMsg 是行 → 传输形态（凭证永不回显）。
func channelMsg(c *channel.Channel) *telemetryv1.NotificationChannel {
	return &telemetryv1.NotificationChannel{
		Id: c.ID, Name: c.Name, Kind: c.Kind, Enabled: c.Enabled,
		LastFailure: c.LastFailure, CreatedAt: c.CreatedAt,
	}
}

// ruleMsg 是规则行 → 传输形态。
func ruleMsg(r *alertrule.Rule) *telemetryv1.AlertRule {
	return &telemetryv1.AlertRule{
		Id: r.ID, AppId: r.AppID, Metric: r.Metric, Threshold: r.Threshold,
		ForSeconds: r.ForSecs, Enabled: r.Enabled, State: r.State,
		StateSince: r.StateSince, CreatedAt: r.CreatedAt,
	}
}
