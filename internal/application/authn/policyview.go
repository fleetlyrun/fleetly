package authn

// policy.Set 的规范树形视图（grpcapi v0.4.0 集成）：fleetly 声明面只有两
// 形态——PUBLIC（policy: { anonymous: true }）与 SERVER（policy:
// { require: [scope(res:op)] }）。View 把树识别回执法面可消费的 Rule，
// 非两形态即构造失败（fail-closed：proto 想声明第三形态必须先扩本文件，
// 不存在静默降级通道）。
//
// 这是全仓唯一的策略投影——声明在 proto（policy.proto 注解）、收集在
// assembly（policy.Build + protopolicy）、识别在此、执行在 guard。授权
// 执行不走库 Decider：fleetly 的拒绝契约（apperr 信封 + token 名/建议
// 消息 + PUBLIC 面无效凭证降级匿名与身份解析熔接）是平台主权面，换
// Decider 会改写三面（CLI/REST/golden）用户可见错误体——采纳与否另立
// 批次裁决，不在依赖升级批夹带。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lynx-go/grpcapi/policy"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
)

// MethodRule 是单个 RPC 的授权视图：Public=true（匿名面）或 Scope 非空
// （SERVER 面最低 scope 门）。两者互斥、必居其一——viewOf 保证。
type MethodRule struct {
	Method string
	Public bool
	Scope  *identity.ScopeRule
}

// ViewRules 把策略集全量投影为规范视图（按方法名排序，守卫与断言消费；
// 任一方法树形非规范即整体失败——fail-closed）。
func ViewRules(set *policy.Set) ([]MethodRule, error) {
	ops := set.Operations()
	out := make([]MethodRule, 0, len(ops))
	for _, op := range ops {
		rule, err := viewOf(op)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out, nil
}

// viewOf 识别单方法策略树。fleetly 规范两形态：
//
//	PUBLIC  { anonymous: true }
//	SERVER  { require: [{ kind: "scope" values: ["resource:op"] }] }
//
// 服务级 SERVER 绊线（default_policy: { authenticated: true }）解析出的
// authenticated 树形在此失败——新方法缺方法注解 = 启动期红，不留运行期
// 才暴露的静默面。
func viewOf(op policy.Operation) (MethodRule, error) {
	p := op.Policy
	if p.Anonymous && len(p.Require) == 0 && len(p.AnyOf) == 0 && len(p.Forbid) == 0 {
		return MethodRule{Method: op.ID, Public: true}, nil
	}
	if !p.Anonymous && len(p.AnyOf) == 0 && len(p.Forbid) == 0 && len(p.Require) == 1 {
		req := p.Require[0]
		if req.Kind == "scope" && len(req.Values) == 1 {
			res, act, found := strings.Cut(req.Values[0], ":")
			if found && act != "" && validAction(act) && res != "" {
				return MethodRule{Method: op.ID, Scope: &identity.ScopeRule{Resource: res, Op: act}}, nil
			}
		}
	}
	return MethodRule{}, fmt.Errorf(
		"method %s: policy tree is not a fleetly canonical shape (PUBLIC anonymous / SERVER single scope); extend authn.viewOf before declaring new shapes in proto",
		op.ID)
}

// validAction 判定 scope 方向值（与 identity.ParseScope 的 action 词表同源）。
func validAction(action string) bool {
	switch action {
	case identity.OpRead, identity.OpWrite, identity.OpAdmin:
		return true
	default:
		return false
	}
}
