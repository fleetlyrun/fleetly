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
	"time"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
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
// 阶段 B 锚（设计文档 §4/§5.1）：新增 QueryScopedMetrics（服务端固定模板
// + 行级授权）恢复 member 图表面后，本函数保留为平台管理员诊断面，
// Console/CLI 切到 scoped 动线；阶段 B 落地前 member 图表失明是已裁决的
// 已知代价（§3.3）。
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
	end := time.Now().UTC()
	if raw := req.GetEnd(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "end: must be RFC3339 (got %q)", raw)
		}
		end = t
	}
	start := end.Add(-time.Hour)
	if raw := req.GetStart(); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "start: must be RFC3339 (got %q)", raw)
		}
		start = t
	}
	if !start.Before(end) {
		return nil, apperr.New("E_INVALID_ARGUMENT", "start: must be before end")
	}
	step := time.Duration(req.GetStepSeconds()) * time.Second
	series, err := svc.s.Metrics.QuerySeries(ctx, req.GetQuery(), start, end, step)
	if err != nil {
		return nil, mapStateError(err, "metrics")
	}
	resp := &telemetryv1.QueryMetricsResponse{}
	for _, s := range series {
		out := &telemetryv1.MetricSeries{Labels: s.Metric}
		for _, p := range s.Points {
			out.Points = append(out.Points, &telemetryv1.MetricPoint{
				Time:  p.Time.Format(time.RFC3339),
				Value: p.Value,
			})
		}
		resp.Series = append(resp.Series, out)
	}
	return resp, nil
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

// ListAlertRules 列规则（app_id 非空 = 行级授权锚；空 = 全量可读面）。
func (svc *AlertingService) ListAlertRules(ctx context.Context, req *telemetryv1.ListAlertRulesRequest) (*telemetryv1.ListAlertRulesResponse, error) {
	if req.GetAppId() != "" {
		if _, err := svc.s.authorizeAppID(ctx, req.GetAppId()); err != nil {
			return nil, err
		}
	}
	var rows []alertrule.Rule
	var err error
	if req.GetAppId() != "" {
		rows, err = svc.s.Engine.AlertRuleRepo().ListByApp(ctx, req.GetAppId())
	} else {
		rows, err = svc.s.Engine.AlertRuleRepo().ListAll(ctx)
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

// ListAlertStates 列现行状态（用户规则行 + 内置系统行）。
func (svc *AlertingService) ListAlertStates(ctx context.Context, _ *telemetryv1.ListAlertStatesRequest) (*telemetryv1.ListAlertStatesResponse, error) {
	rows, err := svc.s.Engine.AlertRuleRepo().ListAll(ctx)
	if err != nil {
		return nil, mapStateError(err, "alert rule")
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
