package engine

// 通道派发面（P3-2 收编自 metrics.go）：告警通知派发到全部启用通道
//（webhook POST JSON / telegram sendMessage）、test 动词的一次性载荷面
// 与投递失败事件（alert.channel_failed）；评估半边见 alerting.go。

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
	ch, err := e.channels.Get(ctx, channelID)
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
func (e *Engine) deliverOne(ctx context.Context, ch *channel.Channel, n alertNotification) error {
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
