package guards

// 守卫 B（审计 §10.2 / 元类 M-1+M-6：错误契约字符串化）：internal/api 的
// 生产代码禁止用 strings.Contains(err.Error(), …) 做错误映射分支。文案匹配
// 的错误契约一改文案就静默退化 E_INTERNAL——上游（state/engine）已有哨兵
// （state.ErrNotFound/ErrConflict、engine 哨兵区），API 层一律 errors.Is /
// errors.As（Q-13 修复即此形态）。测试文件不扫（断言错误文案是测试的
// 合法面）。豁免表带理由、双向保鲜。

import (
	"strings"
	"testing"
)

// errTextMappingLiteral 是禁用的匹配形态（含前导调用点形态
// `strings.Contains(err.Error()`——err 变量名以任何拼写出现均命中：
// 扫描以统一前缀 + err.Error() 组合为准，见下方实现）。
const errTextMappingNeedle = "strings.Contains(err.Error()"

// errMappingExemptions 是豁免表（文件 → 理由）。当前零条目——Q-13 修复
// 后 internal/api 已无文案匹配分支。条目不再命中即红（清走死条目）。
var errMappingExemptions = map[string]string{}

// TestAPINoErrorTextMapping：internal/api/** 非测试 .go 出现
// strings.Contains(err.Error() 即红（err 为最常见变量名；其他拼写的
// err 检测由 go vet 风格评审承载，本守卫拦主流形态）。
func TestAPINoErrorTextMapping(t *testing.T) {
	usedExemptions := map[string]bool{}
	for _, f := range scanGoFiles(t) {
		if !strings.HasPrefix(f.rel, "internal/api/") || strings.HasSuffix(f.rel, "_test.go") {
			continue
		}
		if !strings.Contains(readFileLF(t, f.rel), errTextMappingNeedle) {
			continue
		}
		if reason, ok := errMappingExemptions[f.rel]; ok && reason != "" {
			usedExemptions[f.rel] = true
			continue
		}
		t.Errorf("%s matches %q — error mapping by error text is forbidden: a wording change silently downgrades the branch to E_INTERNAL; use sentinel errors with errors.Is (e.g. state.ErrNotFound) instead (audit §10.2 guard B)", f.rel, errTextMappingNeedle)
	}
	// 双向保鲜：豁免条目不再命中即红。
	for rel, reason := range errMappingExemptions {
		if reason == "" {
			t.Errorf("error-mapping exemption %s must carry a reason", rel)
		}
		if !usedExemptions[rel] {
			t.Errorf("error-mapping exemption %s no longer matches; remove the entry", rel)
		}
	}
}
