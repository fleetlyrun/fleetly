// Package identity 是 Identity & Access 上下文的域内核（领域模型 §2）：
// Scope 语言（colon 形态与蕴含展开）、Token 材质（前缀化随机串 + sha256）、
// 内置角色定义。不 import 任何 internal 包——域纯度与 model/spec 同级
//（守卫见 internal/guards）。
//
// Scope 双形态裁决（本批）：CONTEXT.md 冻结 `resource:action`（colon）为
// 存储/展示/审计的唯一形态；lynx authz 库的 ScopeSet 语言是 `resource.op`
//（dot，colon 被 fail-closed 拒绝）。两界各自尊重——本包持有 colon 侧的
// 解析/校验/蕴含，DotForm 单点转译喂库，词汇真源不动。
package identity

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lynx-go/grpcapi/authz"
)

// Scope 是授权单元（`resource:action`；action ∈ read|write|admin）。
type Scope struct {
	Resource string
	Action   string
}

// String 返回 colon 规范形态。
func (s Scope) String() string { return s.Resource + ":" + s.Action }

// ParseScope 解析并校验单个 colon 形态 scope（资源须在词表内）。
func ParseScope(raw string, resources []string) (Scope, error) {
	res, action, found := strings.Cut(raw, ":")
	if !found || res == "" || action == "" {
		return Scope{}, fmt.Errorf("scope %q: want resource:action", raw)
	}
	if action != "read" && action != "write" && action != "admin" {
		return Scope{}, fmt.Errorf("scope %q: unknown action %q (must be read, write or admin)", raw, action)
	}
	if raw == "*:*" {
		return Scope{}, fmt.Errorf("scope %q: reserved", raw)
	}
	for _, r := range resources {
		if r == res {
			return Scope{Resource: res, Action: action}, nil
		}
	}
	return Scope{}, fmt.Errorf("scope %q: resource %q is not in the scope vocabulary", raw, res)
}

// ParseScopes 解析 scope 集合（全有或全无；去重保序）。
func ParseScopes(raw []string, resources []string) ([]Scope, error) {
	seen := make(map[string]struct{}, len(raw))
	out := make([]Scope, 0, len(raw))
	for _, s := range raw {
		if s == "*" {
			// `*`（owner 全权）单独放行——展开时映射为库的通配 token。
			if _, dup := seen["*"]; dup {
				continue
			}
			seen["*"] = struct{}{}
			out = append(out, Scope{Resource: "*", Action: "*"})
			continue
		}
		p, err := ParseScope(s, resources)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[p.String()]; dup {
			continue
		}
		seen[p.String()] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

// ScopeStrings 把 scope 集合转回 colon 字符串数组（存储/展示形态）。
func ScopeStrings(scopes []Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, s.String())
	}
	return out
}

// expandAction 阶梯：admin ⇒ write ⇒ read（CONTEXT.md Scope 词条：write
// 蕴含 read；admin 同理下蕴含）。
func expandAction(action string) []string {
	switch action {
	case "admin":
		return []string{"admin", "write", "read"}
	case "write":
		return []string{"write", "read"}
	default:
		return []string{"read"}
	}
}

// DotForm 把 colon 形态 scope 集合转译为库的 dot 形态 ScopeSet，并展开
// 蕴含（库的 Satisfies 是精确匹配，蕴含必须在此物化）。这是唯一转译点。
func DotForm(scopes []Scope) (authz.ScopeSet, error) {
	tokens := make([]string, 0, len(scopes)*3)
	for _, s := range scopes {
		if s.Resource == "*" {
			tokens = append(tokens, "*")
			continue
		}
		for _, action := range expandAction(s.Action) {
			tokens = append(tokens, s.Resource+"."+action)
		}
	}
	return authz.ParseScopeSet(tokens)
}

// Expand 把 scope 集合展开为蕴含完备的 colon 形态集合（排序去重；展示与
// 校验用——如 WhoAmI 的 scopes 字段保持未展开原样，本函数供断言/测试）。
func Expand(scopes []Scope) []string {
	set := map[string]struct{}{}
	for _, s := range scopes {
		if s.Resource == "*" {
			set["*"] = struct{}{}
			continue
		}
		for _, action := range expandAction(s.Action) {
			set[s.Resource+":"+action] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
