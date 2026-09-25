package runtime

// 平台内建告警接收器（B 线 W5 设计 §2.3，D-V3W5-1，v0.3 W5-S2）：
//
//	POST /internal/alerts —— Alertmanager v2 webhook 兼容载荷（alerts[]，
//	status firing/resolved、labels、annotations、startsAt/endsAt）。调用方
//	= 托管 vmalert（-notifier.url 指向本端点）；**gateway 原生端点例外清单
//	登记面**（gateway.go——非 proto 派生 HTTP 端点禁止在别处悄悄挂载）。
//
//	认证：ingress token 同源（provider.go 的 authorize 语义）——Bearer
//	`Authorization: Bearer <token>` 与 Basic（vmalert -notifier.basicAuth.*
// 形态发送 `Basic base64(<user>:<token>)`，凭据 = 密码位）双形态接受，
//	常量时间比较；失败恒 401 无信息泄露。
//
//	映射：每条 alert 独立投递（notify.EnqueueAlert）——annotations.channels
//	端点 id 集缺省投全部启用端点；resolved → RESOLVED 前缀恢复通知；预算
//	沿 notify 既有投递退避。**告警投递零事件**（防回环红线，设计 §2.3）。
//
//	形态取舍（provider.go 同款纪律）：不挂 gateway mux——本端点的调用方是
//	vmalert 任务（host 网络，宿主回环可达），鉴权模型（静态凭据）与 gRPC/
//	REST 契约面完全不同；混挂会把两类暴露面耦合进同一个 mux。

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/notify"
)

// internalAlertsPath 是接收器的精确路径（分派面 = 豁免面，gateway.go 例外
// 清单）。
const internalAlertsPath = "/internal/alerts"

// maxAlertsBody 是载荷读取上限（Alertmanager 载荷是小 JSON；1MiB 已是
// 数千条 alert 的形态——H7 的 32MiB 根上限之外的本端点收紧面）。
const maxAlertsBody = 1 << 20

// alertNotifier 是接收器对 notify 投递面的最小消费端口（internal/notify.
// Manager 实现；接口化便于单测注入账面假件）。
type alertNotifier interface {
	EnqueueAlert(ctx context.Context, n notify.AlertNotice) (int, error)
}

// newAlertsReceiverHandler 构造接收器 handler（token = ingress token 明文，
// 生产装配 = ingress.Manager.Token；notifier = notify.Manager）。
func newAlertsReceiverHandler(token string, notifier alertNotifier, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !authorizeReceiver(r, token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		notices, err := parseAlertmanagerPayload(io.LimitReader(r.Body, maxAlertsBody))
		if err != nil {
			http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		accepted := 0
		for _, n := range notices {
			count, err := notifier.EnqueueAlert(r.Context(), n)
			if err != nil {
				// 单条受理失败（库故障）= 5xx——vmalert 按其重试预算重投
				//（Alertmanager 语义：receiver 5xx = 可重试）。
				log.Warn("alerts receiver: enqueue failed", "alert", n.Name, "error", err)
				http.Error(w, "delivery enqueue failed", http.StatusInternalServerError)
				return
			}
			accepted += count
		}
		log.Info("alerts receiver: payload accepted", "alerts", len(notices), "endpoint_deliveries", accepted)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:gosec // G705：固定字面量，无用户可控标记
	})
}

// authorizeReceiver 校验接收器凭据（ingress token 同源）：Bearer 形态比对
// token 本体；Basic 形态比对密码位（vmalert -notifier.basicAuth.username=
// fleetly / passwordFile=token 的发送形态）。常量时间比较。
func authorizeReceiver(r *http.Request, token string) bool {
	const bearer = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(bearer) && strings.EqualFold(h[:len(bearer)], bearer) {
		return subtle.ConstantTimeCompare([]byte(h[len(bearer):]), []byte(token)) == 1
	}
	const basic = "Basic "
	if len(h) > len(basic) && strings.EqualFold(h[:len(basic)], basic) {
		raw, err := base64.StdEncoding.DecodeString(h[len(basic):])
		if err != nil {
			return false
		}
		_, pass, ok := strings.Cut(string(raw), ":")
		if !ok {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(pass), []byte(token)) == 1
	}
	return false
}

// alertmanagerPayload 是 Alertmanager v2 webhook 载荷的消费投影（只取本
// 端点消费的字段；其余字段 DiscardUnknown 语义忽略）。
type alertmanagerPayload struct {
	Status string           `json:"status"`
	Alerts []alertmanagerAlert `json:"alerts"`
}

type alertmanagerAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
}

// parseAlertmanagerPayload 解析载荷为告警事实集（每条 alert 独立一条；
// 空数组合法 = 无所投；status 缺省逐条回退顶层再回退 firing——Alertmanager
// v2 语义）。非 JSON/超限 body = 400。
func parseAlertmanagerPayload(r io.Reader) ([]notify.AlertNotice, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, errors.New("empty body")
	}
	var payload alertmanagerPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := make([]notify.AlertNotice, 0, len(payload.Alerts))
	for _, a := range payload.Alerts {
		status := a.Status
		if status == "" {
			status = payload.Status
		}
		if status == "" {
			status = notify.AlertStatusFiring
		}
		if status != notify.AlertStatusFiring && status != notify.AlertStatusResolved {
			return nil, errors.New("alert status must be firing or resolved")
		}
		n := notify.AlertNotice{
			Name:        a.Labels["alertname"],
			Status:      status,
			Labels:      a.Labels,
			Annotations: a.Annotations,
			Channels:    parseChannelsAnnotation(a.Annotations["channels"]),
		}
		if n.Name == "" {
			n.Name = "unnamed"
		}
		if sev := a.Labels["severity"]; sev != "" {
			n.Severity = sev
		}
		if t, err := time.Parse(time.RFC3339, a.StartsAt); err == nil {
			n.StartsAt = t
		}
		if t, err := time.Parse(time.RFC3339, a.EndsAt); err == nil {
			n.EndsAt = t
		}
		out = append(out, n)
	}
	return out, nil
}

// parseChannelsAnnotation 解析 annotations.channels（逗号分隔端点 id 集；
// 空白与空段丢弃——空结果 = 缺省投全部端点的语义由 notify 层承载）。
func parseChannelsAnnotation(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
