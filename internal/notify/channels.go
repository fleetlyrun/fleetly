package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// 通道分发与执行体（observability 设计 §8.1/§8.4，D-W4-4，W4-S3）：通道
// 分叉只发生在单次投递尝试的执行体——webhook = 签名 POST（signer.go 既有
// 链路逐字不变）/ slack = 裸 POST {"text"}（无 HMAC 头——Slack Incoming
// Webhook 的鉴权就是 URL 本身）/ email = SMTP 会话（net/smtp + STARTTLS，
// 零第三方依赖）。投递管线（游标/匹配/退避/台账/janitor）三通道共用。
//
// 台账语义（§8.1）：webhook/slack 记 HTTP 码（response_code 列）；email
// 用通用 success bool + detail 字符串（last_error），response_code 不承载
// SMTP 语义（保持 NULL——既有列兼容，不发明新词表）。

// SmtpConfig 是一次 email 投递所需的平台级 SMTP 设置投影（密码已由调用方
// 解密为明文——解密边界在持有 box 的一侧，本包只消费）。
type SmtpConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// deliver 按端点类型执行一次投递尝试（Manager 投递与 TestEndpoint 共链路
// ——同一渲染/头/超时语义，测试的验证才有意义）。timeout 是单次尝试预算
// （Manager 传 Config.AttemptTimeout——包缺省 10s；测试发送方传包缺省）。
// 返回 (ok, code, errText)：webhook/slack code = HTTP 响应码；email code =
// DATA 应答码（250 语义），会话未达 DATA 应答 = 0。
func deliver(ctx context.Context, client *http.Client, ep Endpoint, p Payload, smtpCfg *SmtpConfig, timeout time.Duration) (bool, int, string) {
	switch ep.Type {
	case ChannelWebhook, "":
		body, err := MarshalPayload(p)
		if err != nil {
			return false, 0, "payload marshal failed: " + err.Error()
		}
		return sendPayload(ctx, client, ep.URL, ep.Secret, body, timeout)
	case ChannelSlack:
		body, err := json.Marshal(map[string]string{"text": RenderSlackText(p)})
		if err != nil {
			return false, 0, "slack text marshal failed: " + err.Error()
		}
		return sendSlack(ctx, client, ep.URL, body, timeout)
	case ChannelEmail:
		if smtpCfg == nil {
			return false, 0, "smtp settings are not configured (set them with 'notifications smtp set' before email deliveries)"
		}
		ok, _, errText := sendEmail(ctx, *smtpCfg, ep.Target, RenderEmailSubject(p), RenderEmailBody(p), timeout)
		// 台账语义（§8.1）：email 走通用 success bool + detail 字符串——
		// response_code 不承载 SMTP 语义（恒 NULL，既有列兼容）。
		return ok, 0, errText
	default:
		// 词表外的脏数据只可能绕过存储写入通道产生——loud 不静默（终态
		// 由台账可见，零事件红线不破）。
		return false, 0, fmt.Sprintf("unknown channel type %q (dirty row?)", ep.Type)
	}
}

// Endpoint 是 deliver 的通道投影（从 state.WebhookEndpoint 收敛——本包不
// 依赖存储层类型做投递决策）。
type Endpoint struct {
	Type   string
	URL    string
	Target string
	Secret []byte
}

// 通道词表（与 state 层常量同词表；本地重复声明避免 state→notify 的
// 反向依赖——notify 已依赖 state，常量随包语义就近登记）。
const (
	ChannelWebhook = "webhook"
	ChannelSlack   = "slack"
	ChannelEmail   = "email"
)

// sendSlack 执行一次 slack 投递（POST {"text": ...}，无签名头；2xx = ok）。
func sendSlack(ctx context.Context, client *http.Client, rawURL string, body []byte, timeout time.Duration) (bool, int, string) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return false, 0, "request build failed: " + err.Error()
	}
	req.Header.Set("Content-Type", HeaderContentType)
	req.Close = true
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, "post failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, resp.StatusCode, fmt.Sprintf("slack receiver answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return true, resp.StatusCode, ""
}

// sendEmail 执行一次 SMTP 投递（net/smtp + STARTTLS；net/mail 构造信封）：
// EHLO → （服务器支持则）STARTTLS → （配置了用户名则）PLAIN AUTH →
// MAIL FROM → RCPT TO → DATA。返回 ok 与 DATA 应答码（250 语义）；任一
// 步失败即失败（错误摘要单行化、零密码材料——smtp 包错误不回显口令）。
func sendEmail(ctx context.Context, cfg SmtpConfig, to, subject, body string, timeout time.Duration) (bool, int, string) {
	if cfg.From == "" || to == "" {
		return false, 0, "smtp from address or endpoint target is empty"
	}
	fromAddr, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return false, 0, "smtp from address invalid: " + err.Error()
	}
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		return false, 0, "endpoint target address invalid: " + err.Error()
	}
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return false, 0, "smtp dial failed: " + dialErrText(err)
	}
	// 会话自有预算（与 HTTP 10s/次同口径）；ctx 取消时 deadlines 关闭连接。
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return false, 0, "smtp handshake failed: " + err.Error()
	}
	defer func() { _ = client.Close() }()

	// STARTTLS（服务器宣告才升级；明文口令/信封在中继链路的暴露面由此
	// 收敛——不支持 STARTTLS 的内网中继仍可投递，单操作员信任模型）。
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil { //nolint:gosec // G402：MinVersion 留缺省以兼容老式内网中继（与整仓 tls 口径一致）
			return false, 0, "smtp STARTTLS failed: " + err.Error()
		}
	}
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return false, 0, "smtp auth failed: " + err.Error()
		}
	}
	if err := client.Mail(fromAddr.Address); err != nil {
		return false, 0, "smtp MAIL FROM failed: " + err.Error()
	}
	if err := client.Rcpt(toAddr.Address); err != nil {
		return false, 0, "smtp RCPT TO failed: " + err.Error()
	}
	w, err := client.Data()
	if err != nil {
		return false, 0, "smtp DATA failed: " + err.Error()
	}
	msg := buildRFC822(*fromAddr, *toAddr, subject, body)
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return false, 0, "smtp body write failed: " + err.Error()
	}
	if err := w.Close(); err != nil {
		return false, 0, "smtp body rejected: " + err.Error()
	}
	// 邮件已交付（DATA 应答已过），QUIT 失败不构成投递失败（defer Close
	// 收口连接）。
	_ = client.Quit()
	return true, 250, ""
}

// buildRFC822 构造纯文本邮件（net/mail 头 + CRLF 正文；不做 HTML——设计
// §8.2 原文）。
func buildRFC822(from, to mail.Address, subject, body string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from.String() + "\r\n")
	b.WriteString("To: " + to.String() + "\r\n")
	b.WriteString("Subject: " + textproto.TrimString(subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

// dialErrText 收敛拨号错误摘要（单行化；操作型错误已含地址，不另带材料）。
func dialErrText(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", " ")
}

// SendTestEndpoint 按端点类型试发 type=test 载荷（TestEndpoint RPC 的执行
// 体；结构同真实事件、通道语义同链路——设计 §5.2/§8.2）。单次同步投递
// （10s 预算）；不落台账——连通性检查不是投递事实。
func SendTestEndpoint(ctx context.Context, ep Endpoint, smtpCfg *SmtpConfig, endpointID string) (bool, int, string) {
	p := NewTestPayload(endpointID, time.Now().UTC().Unix())
	return deliver(ctx, &http.Client{}, ep, p, smtpCfg, DefaultAttemptTimeout)
}

// SendTestEmail 发送 SMTP 探针邮件（TestSmtp RPC 的执行体）：主题
// `[fleetly] test`、正文 = 探针事实行（收件/时刻）；候选配置由调用方给
// 定，绝不落库。不落台账——连通性检查不是投递事实。
func SendTestEmail(ctx context.Context, cfg SmtpConfig, to string) (bool, int, string) {
	now := time.Now().UTC()
	subject := "[fleetly] test"
	body := strings.Join([]string{
		"name: test",
		"subject: smtp-probe to " + to,
		"at: " + now.Format(time.RFC3339),
	}, "\n")
	return sendEmail(ctx, cfg, to, subject, body, DefaultAttemptTimeout)
}
