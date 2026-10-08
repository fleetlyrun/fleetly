package fleetlygrpc

// Alerting/Metrics 上下文服务（F2.5，ADR-0041）：MetricsService 查询面
//（PromQL 透传——行级过滤责任在调用方的查询构造，本面持平台凭证）与
// AlertingService 配置面（通道平台级 + 规则 per-App 行级授权链；写面走
// commit 原语——事件面留给告警状态迁移沿，配置 CRUD 是审计面，ADR-0041）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/oklog/ulid/v2"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
	alertrule "github.com/fleetlyrun/fleetly/internal/state/alertrule"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/channel"
)

// MetricsService 是指标查询面。
type MetricsService struct {
	telemetryv1.UnimplementedMetricsServiceServer
	s *Services
}

// QueryMetrics 透传 PromQL 到受管 VM（Metrics 面停用时精确失败）。
func (svc *MetricsService) QueryMetrics(ctx context.Context, req *telemetryv1.QueryMetricsRequest) (*telemetryv1.QueryMetricsResponse, error) {
	if svc.s.Metrics == nil {
		return nil, apperr.New("E_INTERNAL", "metrics queries require the managed metrics store; set config metrics.addr to enable it")
	}
	if req.GetQuery() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "query: must not be empty")
	}
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
	name := req.GetName()
	if name == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
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
		return nil, apperr.New("E_INTERNAL", "notification channels need the secret cipher (platform misconfigured)")
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
		ID: ulid.Make().String(), Name: name, Kind: kind,
		ConfigCiphertext: sealed, Enabled: true,
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Engine.NotificationChannelRepo().Create(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "channel.create", Resource: "channel/" + row.ID, AfterFP: kind + ":" + name,
		}},
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
	if req.GetChannelId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "channel_id: must not be empty")
	}
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
	chs, err := svc.s.Engine.NotificationChannelRepo().ListAll(ctx, svc.s.DB.Runner())
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
	if req.GetChannelId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "channel_id: must not be empty")
	}
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Engine.NotificationChannelRepo().Delete(ctx, tx, req.GetChannelId())
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "channel.delete", Resource: "channel/" + req.GetChannelId(),
		}},
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
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	metric := req.GetMetric()
	if metric != alertrule.MetricCPUPercent && metric != alertrule.MetricMemoryWorkingSet {
		return nil, apperr.New("E_INVALID_ARGUMENT", "metric: must be %s or %s (got %q)",
			alertrule.MetricCPUPercent, alertrule.MetricMemoryWorkingSet, metric)
	}
	if req.GetThreshold() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "threshold: must not be negative")
	}
	row := &alertrule.Rule{
		ID: engine.NewAlertRuleID(), AppID: appRow.ID, Metric: metric,
		Threshold: req.GetThreshold(), ForSecs: req.GetForSeconds(), Enabled: true,
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(appRow.ProjectID)},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Engine.AlertRuleRepo().Create(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "alert_rule.create", Resource: "alert_rule/" + row.ID,
			AfterFP: appRow.ID + ":" + metric,
		}},
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
		rows, err = svc.s.Engine.AlertRuleRepo().ListByApp(ctx, svc.s.DB.Runner(), req.GetAppId())
	} else {
		rows, err = svc.s.Engine.AlertRuleRepo().ListAll(ctx, svc.s.DB.Runner())
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
	if req.GetRuleId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "rule_id: must not be empty")
	}
	row, err := svc.s.Engine.AlertRuleRepo().Get(ctx, svc.s.DB.Runner(), req.GetRuleId())
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "alert rule %s not found", req.GetRuleId())
		}
		return nil, mapStateError(err, "alert rule")
	}
	if _, err := svc.s.authorizeAppID(ctx, row.AppID); err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Engine.AlertRuleRepo().Delete(ctx, tx, req.GetRuleId())
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "alert_rule.delete", Resource: "alert_rule/" + row.ID,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	return &telemetryv1.DeleteAlertRuleResponse{}, nil
}

// ListAlertStates 列现行状态（用户规则行 + 内置系统行）。
func (svc *AlertingService) ListAlertStates(ctx context.Context, _ *telemetryv1.ListAlertStatesRequest) (*telemetryv1.ListAlertStatesResponse, error) {
	rows, err := svc.s.Engine.AlertRuleRepo().ListAll(ctx, svc.s.DB.Runner())
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
