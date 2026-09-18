package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	"github.com/edgesets/edgefleet/internal/apperr"
	"github.com/edgesets/edgefleet/internal/compose"
)

// errChanges 是 plan/diff「有变化」的哨兵：exitCodeFor 映射为退出码 2
// （架构 §2.4 三态退出码：0=无变化 / 2=有变化 / 1=错误）。这是成功语义
// 而非错误，renderCLIError 对其只出提示行。
var errChanges = errors.New("changes detected")

// renderCLIError 渲染动词错误（stderr 单点）：有变化 → 提示行；apperr →
// 信封四件套（code/message + 注册表默认 suggestion/docs）；其余原样。
func renderCLIError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errChanges) {
		return "changes detected (exit 2)"
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		var b strings.Builder
		fmt.Fprintf(&b, "%s", ae.Error())
		// 信封附加信息经 Envelope() 读出（suggestion/docs 为注册表默认带
		// 出；context 为校验路径等结构化上下文）——apperr 不需要为 CLI 增
		// 加访问器。
		env := ae.Envelope()
		for _, k := range sortedKeys(env.GetContext()) {
			fmt.Fprintf(&b, "\n  %s: %s", k, env.GetContext()[k])
		}
		if env.GetSuggestion() != "" {
			fmt.Fprintf(&b, "\n  suggestion: %s", env.GetSuggestion())
		}
		if env.GetDocs() != "" {
			fmt.Fprintf(&b, "\n  docs: %s", env.GetDocs())
		}
		return b.String()
	}
	var usage *commands.UsageError
	if errors.As(err, &usage) {
		return err.Error()
	}
	return err.Error()
}

// sortStrings 是字典序原地排序（std sort 的本地别名）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// sortedKeys 是字典序键排序（proto map 上下文的稳定渲染）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// validateResult 是 validate --json 的输出形态。
type validateResult struct {
	Valid    bool              `json:"valid"`
	Name     string            `json:"name"`
	SpecHash string            `json:"spec_hash"`
	Services int               `json:"services"`
	Volumes  int               `json:"volumes"`
	Warnings []compose.Warning `json:"warnings,omitempty"`
}

// writeValidateHuman 渲染 validate 的人读输出。
func writeValidateHuman(w *strings.Builder, r validateResult, warnings []compose.Warning) {
	fmt.Fprintf(w, "%s: valid (spec_hash %.12s, %d services, %d volumes)\n", r.Name, r.SpecHash, r.Services, r.Volumes)
	writeWarnings(w, warnings)
}

// writeWarnings 渲染非阻断警告（注册码或 Kind 标识）。
func writeWarnings(w *strings.Builder, warnings []compose.Warning) {
	for _, warn := range warnings {
		label := warn.Code
		if label == "" {
			label = warn.Kind
		}
		if warn.Service != "" {
			fmt.Fprintf(w, "warning [%s] %s: %s\n", label, warn.Service, warn.Message)
		} else {
			fmt.Fprintf(w, "warning [%s] %s\n", label, warn.Message)
		}
	}
}

// marshalIndentJSON 是 --json 输出的统一编码（两空格缩进 + 尾换行）。
func marshalIndentJSON(v any) ([]byte, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
