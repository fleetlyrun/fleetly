package notify

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 消息渲染（observability 设计 §8.2，D-W4-5）：事件→消息的数据面沿既有
// delivery 载荷（patterns 匹配后的事件投影），渲染按通道类型分叉——
//
//	webhook：既有 JSON 载荷逐字不变（对外契约，§5.2 原文，不经本文件）；
//	slack：单行头 `[fleetly] <event name> — <subject>` + 详情字段若干行
//	       （key: value），整体作为 `{"text": ...}` 的值；
//	email：纯文本（不做 HTML）——主题 `[fleetly] <event name>`，正文 =
//	       事件字段键值对（逐行 key: value）。
//
// 渲染是纯函数：同样的事件在同样通道渲染出的文本恒定（e2e 断言与单测
// golden 都依赖这一点）。

// RenderSlackText 渲染 Slack Incoming Webhook 的 text 字段：头行 + 详情行。
func RenderSlackText(p Payload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[fleetly] %s — %s\n", p.Name, p.Subject)
	for _, line := range payloadDetailLines(p) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderEmailSubject 渲染邮件主题（RFC 5322 Subject 值；纯 ASCII 安全形态
// ——事件名词表是 [a-z0-9_.]，不产生编码需求）。
func RenderEmailSubject(p Payload) string {
	return "[fleetly] " + p.Name
}

// RenderEmailBody 渲染纯文本正文：事件字段键值对逐行（不做 HTML）。
func RenderEmailBody(p Payload) string {
	lines := []string{
		"name: " + p.Name,
		"subject: " + p.Subject,
	}
	lines = append(lines, payloadDetailLines(p)...)
	return strings.Join(lines, "\n")
}

// payloadDetailLines 是两通道共享的详情字段行（seq/at/payload——name 与
// subject 已在头行/独立行出现，不重复；at 渲染 RFC3339 UTC 人读形态）。
// payload 展开为紧凑 JSON（顶层键排序保证恒定渲染）。
func payloadDetailLines(p Payload) []string {
	lines := []string{
		fmt.Sprintf("seq: %d", p.Seq),
		"at: " + time.Unix(p.At, 0).UTC().Format(time.RFC3339),
	}
	if len(p.Payload) > 0 && string(p.Payload) != "{}" {
		lines = append(lines, "payload: "+flattenPayload(p.Payload))
	}
	return lines
}

// flattenPayload 把 payload JSON 展开为 `key: value` 行集（顶层键排序；
// 嵌套值保持紧凑 JSON 原样——渲染层不做深度变换，事件面已脱敏）。
func flattenPayload(raw json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := json.Marshal(obj[k])
		if err != nil {
			parts = append(parts, k+": <unrenderable>")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", k, v))
	}
	return strings.Join(parts, "\n")
}
