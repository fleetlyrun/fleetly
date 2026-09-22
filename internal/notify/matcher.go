// Package notify 是通知投递器（E6 观测专项设计 §5.2，W5-S4；lynx
// service，进程内事件消费者）：按 seq 游标轮询 events 表（EventsSince，
// WatchEvents 同路径）→ 订阅模式匹配 → 每端点 POST JSON + HMAC 签名 →
// 失败退避重试 + 台账。
//
// 红线（设计 §5.2）：**通知自身零事件**——投递失败不 emitted 事件（防自
// 激励环），失败可见面 = webhook_deliveries 台账 + system status
// notifications 组件红 + Console 卡；订阅 `*` 不会把自己套进回环。
package notify

import (
	"regexp"
	"strings"
)

// compilePattern 把订阅 glob 模式编译为锚定 regexp：`*` 匹配任意序列
// （含 `.`——deployment.* 覆盖 deployment.queued 与 deployment.a.b 两级），
// 其余字符逐段 QuoteMeta 转义（state 层白名单是第一道闸，转义是第二道
// ——双保险后模式集合「永远可安全渲染」是存储不变量）。
func compilePattern(pattern string) (*regexp.Regexp, error) {
	parts := strings.Split(pattern, "*")
	var b strings.Builder
	b.WriteString("^")
	for i, seg := range parts {
		if seg != "" {
			b.WriteString(regexp.QuoteMeta(seg))
		}
		// 段间通配：n 个 `*` 产生 n+1 段，`*` 恰在各段之间；无 `*` 的
		// 模式单段直出 = 精确匹配（锚定封口，不误吞前后缀）。
		if i < len(parts)-1 {
			b.WriteString(".*")
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// EndpointMatcher 是一个端点的已编译订阅模式集（构造期编译一次，消费期
// 纯内存匹配——每事件 × 每端点都不重复解析模式）。
type EndpointMatcher struct {
	Patterns []string
	res      []*regexp.Regexp
}

// NewEndpointMatcher 编译模式集（patterns 应已过 state.ValidateWebhookPatterns
// 白名单；编译失败只可能是绕过存储写入通道的脏数据——报错不静默）。
func NewEndpointMatcher(patterns []string) (*EndpointMatcher, error) {
	m := &EndpointMatcher{Patterns: patterns}
	for _, p := range patterns {
		re, err := compilePattern(p)
		if err != nil {
			return nil, err
		}
		m.res = append(m.res, re)
	}
	return m, nil
}

// Match 报告事件名是否命中任一订阅模式（OR 语义——模式列表是订阅集）。
func (m *EndpointMatcher) Match(eventName string) bool {
	for _, re := range m.res {
		if re.MatchString(eventName) {
			return true
		}
	}
	return false
}
