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
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Channels.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{
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
	if err := svc.s.System.TestNotificationChannel(ctx, req.GetChannelId()); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "notification channel %s not found", req.GetChannelId())
		}
		return &telemetryv1.TestNotificationChannelResponse{Delivered: false, Error: err.Error()}, nil
	}
	return &telemetryv1.TestNotificationChannelResponse{Delivered: true}, nil
}

// ListNotificationChannels 列通道（配置载荷永不回显——凭证单向）。
func (svc *AlertingService) ListNotificationChannels(ctx context.Context, _ *telemetryv1.ListNotificationChannelsRequest) (*telemetryv1.ListNotificationChannelsResponse, error) {
	chs, err := svc.s.System.NotificationChannelRepo().ListAll(ctx)
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
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Channels.Delete(ctx, req.GetChannelId())
		},
		Facts: []func() writes.FactRow{
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
	// 事件归属在进事务前解析（R08-M8：解析失败整单拒绝，不落平台级可见行）。
	fact, err := svc.s.factOf(ctx, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventAlertRuleCreated, Aggregate: "alert_rule", ID: row.ID,
			Payload: mustJSON(alertRuleEventPayload{AppID: row.AppID, Metric: row.Metric}),
			TeamID:  team,
			Action:  "alert_rule.create", Resource: "alert_rule/" + row.ID,
			AfterFP: appRow.ID + ":" + metric,
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{svc.s.parentProjectAlive(appRow.ProjectID)},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.AlertRules.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
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
		rows, err = svc.s.System.AlertRuleRepo().ListByApp(ctx, req.GetAppId())
	} else {
		rows, err = svc.s.System.AlertRuleRepo().ListAll(ctx)
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
	row, err := svc.s.System.AlertRuleRepo().Get(ctx, req.GetRuleId())
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
	fact, err := svc.s.factOf(ctx, appRow.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventAlertRuleDeleted, Aggregate: "alert_rule", ID: row.ID,
			Payload: mustJSON(alertRuleEventPayload{AppID: row.AppID, Metric: row.Metric}),
			TeamID:  team,
			Action:  "alert_rule.delete", Resource: "alert_rule/" + row.ID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.AlertRules.Delete(ctx, req.GetRuleId())
		},
		Facts: []func() writes.FactRow{fact},
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
	rows, err := svc.s.System.AlertRuleRepo().ListAll(ctx)
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
	sys := svc.s.System.SystemOffsiteAlertState()
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
	cvg := svc.s.System.SystemConvergeAlertState()
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
