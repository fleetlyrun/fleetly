package composeintake

// command 面的 $ 预逃逸（R03-M5）：compose 插值把 $$ 还原为 $，而 command
// 是运行时 shell 语境（$ 语义属 shell）——加载前按「连续 $ 串」补转义位，
// 让官方消转后的 IR 与 compose 规范一致。独立成文件让 intake.go 的受理
// 执法面保持可读（sizeguard 文件预算）。

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// escapeCommandDollars 预逃逸 command 面（services[].command 与
// x-fleetly-pre-deploy-jobs[].command）的 $：官方插值 $$→$ 还原原文
// ——command 是运行时 shell 语境，$ 语义属 shell（真机咬出：脚本循环变量
// $n 被 fail-closed 插值当未声明孔硬拒；dokploy 系模板仓的 command 脚本
// 普遍不逃逸，模板库使命要求原样承接）。平台变量经进程 env 在运行时可见
// （ADR-0072 合成面），无需插值烘焙。非 YAML 原样交还（canonical 错误由
// 官方管线给出）。
//
// 转义口径按 compose 规范逐「连续 $ 串」处理（R03-M5）：n 个连续 $ 经官方
// 消转的产物是 ceil(n/2) 个 $（$$→$、$$$→$$）——因此只有奇数串需补一个
// 转义位，偶数串是用户已写的转义对（原样交给官方消转）。旧实现无差别
// 加倍把用户已写的 $$ 二次转义回 $$（迁移文件静默执行错误命令）。
func escapeCommandDollars(body []byte) []byte {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return body
	}
	var escape func(v any) any
	escape = func(v any) any {
		switch t := v.(type) {
		case string:
			return escapeDollarRuns(t)
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				out[i] = escape(item)
			}
			return out
		}
		return v
	}
	if services, ok := doc["services"].(map[string]any); ok {
		for _, raw := range services {
			if svc, ok := raw.(map[string]any); ok {
				if cmd, present := svc["command"]; present {
					svc["command"] = escape(cmd)
				}
			}
		}
	}
	if jobs, ok := doc["x-fleetly-pre-deploy-jobs"].([]any); ok {
		for _, raw := range jobs {
			if job, ok := raw.(map[string]any); ok {
				if cmd, present := job["command"]; present {
					job["command"] = escape(cmd)
				}
			}
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// escapeDollarRuns 对字符串里每一段连续 $ 只补奇数串（见 escapeCommandDollars
// 的口径推导）。非 $ 字节逐字透传。
func escapeDollarRuns(s string) string {
	if !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '$' {
			j++
		}
		n := j - i
		if n%2 == 1 {
			n++ // 奇数串补一个：官方消转后得 ceil(原串长/2)
		}
		b.WriteString(strings.Repeat("$", n))
		i = j
	}
	return b.String()
}
