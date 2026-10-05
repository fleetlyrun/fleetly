package guards

// 写面 scope fail-safe 门（ADR-0038 / P6 T2，与 idem 覆盖反扫同构）：proto
// 注解是 scope 声明表（authz.Build 收集，运行时真源）；本守卫在 CI 对账
// "动词 × 声明方向"——变更型动词（Create/Delete/Put/Deploy/...）标 READ 即
// 红（新 RPC 上线忘挂授权检查 = fail-open 静默洞，zane 三维修权同款防线）；
// 读型动词标 WRITE/ADMIN 同样红（声明表与动词漂移即误标）。动词表完备性
// 双向把守：未知动词（两侧都不命中）即红——新动词必须显式入表，杜绝
// "动词不在表里绕过分类"的静默通道。

import (
	"sort"
	"strings"
	"testing"

	"github.com/lynx-go/grpcapi/authz"

	"github.com/fleetlyrun/fleetly/internal/assembly"
)

// mutatingVerbs 是变更型动词前缀（op 必须 WRITE/ADMIN）。
var mutatingVerbs = []string{
	"Create", "Delete", "Put", "Deploy", "Rollback", "Revoke", "Enroll",
	"Drain", "Cordon", "Uncordon", "Scale", "Stop", "Cancel", "Renew",
	"Trigger", "Rotate", "Upload", "Set", "Lift", "Approve", "Accept",
	"Declare", "Rebuild",
}

// readVerbs 是读型动词前缀（op 必须 READ）。
var readVerbs = []string{
	"Get", "List", "Stream", "Wait", "Diff", "Explain", "WhoAmI",
	"Follow", "Watch", "Verify", "Query",
}

// scopeOpExemptions 是 FullMethod → 豁免理由（表与动词方向不一致的唯一
// 例外通道；不再命中即红——双向保鲜）。
var scopeOpExemptions = map[string]string{
	// 票据交换面：铸的是只读 SSE 短票，events:read 对应被交换的能力而非
	// 铸造动作本身（ADR-0026 EventSource 无自定义头）。
	"/fleetly.telemetry.v1.EventsService/IssueEventTicket": "mints a read-only SSE ticket; events:read matches the delegated capability, not the mint action",
	// 通道测试面（F2.5）：channels:write 对应通道配置权；测试载荷是配置权
	// 的诊断行使（channels:read 不足以承载——测试发的是出站流量）。
	"/fleetly.telemetry.v1.AlertingService/TestNotificationChannel": "sends one outbound test payload; channels:write matches the configuration authority being exercised (ADR-0041)",
}

// methodVerb 从 FullMethod 取动词段（"/pkg.Svc/CreateApp" → "CreateApp"）。
func methodVerb(full string) string {
	_, method, _ := strings.Cut(full, "/") // 跳过 "/pkg.Svc"
	if i := strings.LastIndex(method, "/"); i >= 0 {
		method = method[i+1:]
	}
	return method
}

func matchesVerb(verb string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(verb, p) {
			return true
		}
	}
	return false
}

// TestScopeDeclarationsMatchVerbs：SERVER 面全部 scope 声明 × 动词分类对账。
func TestScopeDeclarationsMatchVerbs(t *testing.T) {
	policy, err := assembly.NewPolicySet()
	if err != nil {
		t.Fatalf("policy build failed: %v", err)
	}
	methods := policy.Methods()
	if len(methods) < 40 {
		t.Fatalf("policy surface suspiciously small (%d methods) — guard is blind, fix the scan", len(methods))
	}

	usedExemptions := map[string]bool{}
	for _, p := range methods {
		if p.Access != authz.AccessServer || p.Scope == nil {
			continue // PUBLIC/PERMISSION 面与无 scope 方法不在本守卫域
		}
		verb := methodVerb(p.Method)
		if reason, ok := scopeOpExemptions[p.Method]; ok {
			if reason == "" {
				t.Errorf("scope op exemption %s must carry a reason", p.Method)
			}
			usedExemptions[p.Method] = true
			continue
		}
		switch {
		case matchesVerb(verb, mutatingVerbs):
			if p.Scope.Op == authz.ScopeRead {
				t.Errorf("mutating RPC %s declares scope %s:%s — a mutating verb behind a read scope is the fail-open hole this guard exists to close (ADR-0038); declare WRITE/ADMIN or add an exemption with a reason",
					p.Method, p.Scope.Resource, p.Scope.Op)
			}
		case matchesVerb(verb, readVerbs):
			if p.Scope.Op != authz.ScopeRead {
				t.Errorf("read-type RPC %s declares scope %s:%s — read verbs behind write/admin scopes are declaration drift; fix the annotation or add an exemption with a reason",
					p.Method, p.Scope.Resource, p.Scope.Op)
			}
		default:
			t.Errorf("RPC verb %q (%s) is in neither the mutating nor the read verb table — classify it consciously in scopeguard_test.go (unknown verbs must not bypass the scope-direction guard)",
				verb, p.Method)
		}
	}

	// 双向保鲜：豁免不再命中即红。
	stale := make([]string, 0, len(scopeOpExemptions))
	for m := range scopeOpExemptions {
		if !usedExemptions[m] {
			stale = append(stale, m)
		}
	}
	sort.Strings(stale)
	for _, m := range stale {
		t.Errorf("scope op exemption %s no longer matches a real SERVER-scoped method; remove the entry", m)
	}
}
