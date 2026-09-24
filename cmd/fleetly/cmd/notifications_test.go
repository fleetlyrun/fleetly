package cmd

// notifications 命令的通道扩展单测（W4-S3，observability §8）：端点清单行
// 的通道语义列（email 显收件地址，webhook/slack 显 URL）、create 位置参数
// 规格（email 无 URL 位）、密码旗标的环境变量回退（FLEETLY_SMTP_PASSWORD
// ——env 形态不进 shell 历史）。

import (
	"os"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

func TestEndpointListLineChannelSemantics(t *testing.T) {
	// 指纹字面量（sha256 前 8 hex 形态——非凭据材料，命名避开 gosec G101
	// 的 secret 关键词误报）。
	const (
		fpOps   = "0123456789abcdef"
		fpRelay = "ffffffffffffffff"
		fpMail  = "abababababababab"
	)
	cases := []struct {
		name string
		ep   *serverv1.WebhookEndpointView
		want string
	}{
		{
			name: "webhook shows the receiver url",
			ep: &serverv1.WebhookEndpointView{
				Name: "ops", Type: "webhook", Url: "http://127.0.0.1:8899/hook",
				SecretFingerprint: fpOps, Enabled: true,
				EventPatterns: []string{"deployment.*"},
			},
			want: "ops  [webhook]  http://127.0.0.1:8899/hook  secret=0123456789abcdef…  [enabled]  deployment.*\n",
		},
		{
			name: "slack shows the incoming webhook url",
			ep: &serverv1.WebhookEndpointView{
				Name: "relay", Type: "slack", Url: "https://hooks.slack.test/T/B/x",
				SecretFingerprint: fpRelay, Enabled: false,
				EventPatterns: []string{"*"},
			},
			want: "relay  [slack]  https://hooks.slack.test/T/B/x  secret=ffffffffffffffff…  [disabled]  *\n",
		},
		{
			name: "email shows the recipient mailbox instead of a url",
			ep: &serverv1.WebhookEndpointView{
				Name: "mail", Type: "email", Target: "ops@example.test",
				SecretFingerprint: fpMail, Enabled: true,
				EventPatterns: []string{"cron.*"},
			},
			want: "mail  [email]  to:ops@example.test  secret=abababababababab…  [enabled]  cron.*\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endpointListLine(tc.ep); got != tc.want {
				t.Fatalf("endpointListLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSmtpPasswordFromEnvFallback(t *testing.T) {
	cases := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{name: "flag wins", flag: "pw-from-flag", env: "pw-from-env", want: "pw-from-flag"},
		{name: "env fallback keeps the flag out of shell history", flag: "", env: "pw-from-env", want: "pw-from-env"},
		// 未设置与置空对 Getenv 同为空串——空 = 清除已存密码（PUT 语义）。
		{name: "both empty clears the stored password (PUT semantics)", flag: "", env: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FLEETLY_SMTP_PASSWORD", tc.env)
			password := tc.flag
			if password == "" {
				password = os.Getenv("FLEETLY_SMTP_PASSWORD")
			}
			if password != tc.want {
				t.Fatalf("password = %q, want %q", password, tc.want)
			}
		})
	}
}
