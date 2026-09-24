package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// W4 通道扩展测试（observability 设计 §8，D-W4-4，W4-S3）：通道词表与
// 字段组合形状（webhook/slack → url 有 target 空；email → target 有 url
// 空）、存量缺省回填（type 空串归一 webhook）、update 换通道的最终形态
// 校验。台账/游标零变化（设计 §8.4 红线沿用）——本文件的写路径只落审计行。

func TestWebhookChannelCreateShapes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 缺省 type = webhook（proto3 零值语义；迁移 DEFAULT 同口径）。
	web, err := st.CreateWebhookEndpoint(ctx, WebhookEndpointWrite{
		Name: "ops", URL: "http://127.0.0.1:8899/hook",
		SecretCipher: "c", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"deployment.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create webhook endpoint: %v", err)
	}
	if web.Type != WebhookChannelWebhook || web.Target != "" {
		t.Fatalf("default channel: type=%q target=%q, want webhook/empty", web.Type, web.Target)
	}

	// slack：url 复用（Incoming Webhook URL），target 恒空。
	slack, err := st.CreateWebhookEndpoint(ctx, WebhookEndpointWrite{
		Name: "relay", Type: WebhookChannelSlack, URL: "https://hooks.slack.test/T1/B2/xyz",
		SecretCipher: "c", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create slack endpoint: %v", err)
	}
	if slack.Type != WebhookChannelSlack || slack.URL == "" || slack.Target != "" {
		t.Fatalf("slack channel shape: %+v", slack)
	}

	// email：target 必填、url 必须为空。
	mail, err := st.CreateWebhookEndpoint(ctx, WebhookEndpointWrite{
		Name: "mail", Type: WebhookChannelEmail, Target: "ops@example.test",
		SecretCipher: "c", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"cron.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create email endpoint: %v", err)
	}
	if mail.Type != WebhookChannelEmail || mail.Target != "ops@example.test" || mail.URL != "" {
		t.Fatalf("email channel shape: %+v", mail)
	}

	// 非法组合：webhook 带 target；email 带 url；email target 非法；词表外
	// type。
	bad := []WebhookEndpointWrite{
		{Name: "b1", Target: "x@example.test", SecretCipher: "c", SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}},
		{Name: "b2", Type: WebhookChannelSlack, URL: "http://x.test", Target: "x@example.test", SecretCipher: "c", SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}},
		{Name: "b3", Type: WebhookChannelEmail, URL: "http://x.test", SecretCipher: "c", SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}},
		{Name: "b4", Type: WebhookChannelEmail, Target: "not a mailbox", SecretCipher: "c", SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}},
		{Name: "b5", Type: "irc", URL: "http://x.test", SecretCipher: "c", SecretFingerprint: "0123456789abcdef", EventPatterns: []string{"*"}},
	}
	for _, w := range bad {
		if _, err := st.CreateWebhookEndpoint(ctx, w); err == nil {
			t.Fatalf("create %s must be rejected (type=%q url=%q target=%q)", w.Name, w.Type, w.URL, w.Target)
		}
	}

	// 审计 diff 带 type 词表值（非敏感事实）。
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	seenType := false
	for _, a := range audits {
		if a.Action == "webhook.created" && strings.Contains(a.DiffSummary, `"slack"`) {
			seenType = true
		}
	}
	if !seenType {
		t.Fatal("webhook.created audit diff must record the channel type")
	}
}

func TestWebhookChannelUpdateShapes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ep, err := st.CreateWebhookEndpoint(ctx, WebhookEndpointWrite{
		Name: "ops", URL: "http://127.0.0.1:8899/hook",
		SecretCipher: "c", SecretFingerprint: "0123456789abcdef",
		EventPatterns: []string{"deployment.*"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 单独给 webhook 端点加 target → 拒绝（组合形状）。
	target := "x@example.test"
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{Target: &target}); err == nil {
		t.Fatal("adding target to a webhook endpoint must be rejected")
	}

	// 单独把 url 置空 → 拒绝（会失去投递地址）。
	empty := ""
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{URL: &empty}); err == nil {
		t.Fatal("clearing url alone must be rejected")
	}

	// 同一次 update 换通道（url 清空 + target 设置）→ 通过；最终形态落库。
	typ := WebhookChannelEmail
	updated, err := st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{
		Type: &typ, URL: &empty, Target: &target,
	})
	if err != nil {
		t.Fatalf("channel switch update: %v", err)
	}
	if updated.Type != WebhookChannelEmail || updated.URL != "" || updated.Target != target {
		t.Fatalf("after switch: %+v", updated)
	}

	// email → slack 换回（target 清空 + url 设置）。
	typ = WebhookChannelSlack
	url := "https://hooks.slack.test/T1/B2/xyz"
	emptyTarget := ""
	updated, err = st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{
		Type: &typ, Target: &emptyTarget, URL: &url,
	})
	if err != nil {
		t.Fatalf("switch back to slack: %v", err)
	}
	if updated.Type != WebhookChannelSlack || updated.Target != "" || updated.URL != url {
		t.Fatalf("after switch back: %+v", updated)
	}

	// 通道不变、只改 target：webhook 端点 → 拒绝（形状不符）。
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{Target: &target}); err == nil {
		t.Fatal("target on a slack endpoint must be rejected")
	}

	// 词表外 type 拒绝。
	bad := "irc"
	if _, err := st.UpdateWebhookEndpoint(ctx, ep.ID, WebhookEndpointUpdate{Type: &bad}); err == nil {
		t.Fatal("unknown channel type must be rejected")
	}

	// 不存在的端点 → ErrWebhookNotFound。
	if _, err := st.UpdateWebhookEndpoint(ctx, "01NOPE", WebhookEndpointUpdate{Target: &target}); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("missing endpoint: err = %v, want ErrWebhookNotFound", err)
	}
}
