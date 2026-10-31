package engine

// 通道派发面（P3-2 收编自 metrics.go）：告警通知派发到全部启用通道
//（webhook POST JSON / telegram sendMessage）、test 动词的一次性载荷面
// 与投递失败事件（alert.channel_failed）；评估半边见 alerting.go。
// B2a W2 起投递两类裁决落地：Finding 类（crash-loop，crashnotify.go）复用
// 同一通道面——载荷族经 outboundNotification 泛化，Alert 与 Finding 各自
// 实现 webhook JSON 体与 telegram 文本。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// 事件名（eventcode 注册表同源）。
const eventAlertChannelFail = "alert.channel_failed"

// outboundNotification 是通道载荷的通用面（投递两类：alert | finding）：
// webhook POST JSON 体与 telegram 文本各自实现，派发管线（deliverOne/
// deliverChannel）对载荷族封闭。
type outboundNotification interface {
	// kind 是载荷族名（alert | alert_test | finding；telegram 测试文案分流）。
	kind() string
	// payloadJSON 是 webhook POST 体（JSON 字节）。
	payloadJSON() []byte
	// telegramText 是 telegram sendMessage 文本。
	telegramText() string
}

// alertNotification 是 Alert Rule 迁移的通道载荷。
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

// kind 实现 outboundNotification（载荷族名）。
func (n alertNotification) kind() string { return n.Type }

// payloadJSON 实现 outboundNotification（webhook POST 体）。
func (n alertNotification) payloadJSON() []byte {
	b, _ := json.Marshal(n)
	return b
}

// telegramText 实现 outboundNotification（telegram 文本）。
func (n alertNotification) telegramText() string {
	return fmt.Sprintf("fleetly alert %s: %s %s (value %.2f, threshold %.2f) on app %s",
		n.State, n.RuleID, n.Metric, n.Value, n.Threshold, n.AppID)
}

// findingNotification 是 Finding 类通知的通道载荷（B2a W2 已裁投递两类，
// 本批 crash-loop 类先行；App 级其余 Finding 不投递）。State 值域
// entered | cleared（沿 marker 同源，crashnotify.go）。
type findingNotification struct {
	Type         string `json:"type"` // finding
	FindingID    string `json:"finding_id"`
	AppID        string `json:"app_id,omitempty"`
	WorkloadID   string `json:"workload_id,omitempty"`
	Process      string `json:"process,omitempty"`
	RestartCount int    `json:"restart_count,omitempty"`
	State        string `json:"state"` // entered | cleared
	At           string `json:"at"`
}

// kind 实现 outboundNotification（载荷族名）。
func (n findingNotification) kind() string { return n.Type }

// payloadJSON 实现 outboundNotification（webhook POST 体）。
func (n findingNotification) payloadJSON() []byte {
	b, _ := json.Marshal(n)
	return b
}

// telegramText 实现 outboundNotification（telegram 文本）。
func (n findingNotification) telegramText() string {
	text := fmt.Sprintf("fleetly finding %s: workload %s on app %s %s crash loop",
		n.FindingID, n.WorkloadID, n.AppID, n.State)
	if n.State == "entered" {
		return fmt.Sprintf("%s (%d terminal exits in the near window)", text, n.RestartCount)
	}
	return text
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
	ch, err := e.channels.Get(ctx, channelID)
	if err != nil {
		return err
	}
	return e.deliverOne(ctx, ch, alertNotification{
		Type: "alert_test", RuleID: "test", Metric: "test", State: "test",
		At: state.FormatTime(e.clock.Now()),
	})
}

// dispatchNotification 派发到全部启用通道（载荷族封闭于 outboundNotification）。
func (e *Engine) dispatchNotification(ctx context.Context, n outboundNotification) {
	chs, err := e.channels.ListEnabled(ctx)
	if err != nil {
		e.log.Error("metrics step: list channels", "err", err)
		return
	}
	for i := range chs {
		_ = e.deliverOne(ctx, &chs[i], n)
	}
}

// deliverOne 投递一条通道（webhook POST JSON / telegram sendMessage）。
func (e *Engine) deliverOne(ctx context.Context, ch *channel.Channel, n outboundNotification) error {
	err := e.deliverChannel(ctx, ch, n)
	failure := ""
	if err != nil {
		failure = err.Error()
		if payload, merr := json.Marshal(alertChannelFailedPayload{ChannelID: ch.ID, Error: failure}); merr == nil {
			// 通知通道是平台级资源（无 owner 域——平台级，全员可见）：team
			// 留 ''，终态口径。
			if aerr := e.emitObservation(ctx, "", eventAlertChannelFail, "channel", ch.ID, payload); aerr != nil {
				e.log.Error("metrics step: emit channel failure", "channel", ch.ID, "err", aerr)
			}
		}
	}
	if derr := e.channels.RecordDelivery(ctx, ch.ID, failure); derr != nil {
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
func (e *Engine) deliverChannel(ctx context.Context, ch *channel.Channel, n outboundNotification) error {
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
		req, rerr := http.NewRequestWithContext(dctx, http.MethodPost, cfg.URL, bytes.NewReader(n.payloadJSON()))
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
		text := n.telegramText()
		if n.kind() == "alert_test" {
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
