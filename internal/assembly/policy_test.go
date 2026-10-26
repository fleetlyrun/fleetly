package assembly

// PolicySet 构造面测试：词表与 proto 注解的咬合（fail-closed 断言在
// policy.Build 构造期爆掉；本测试钉住 identity 面的关键策略投影——ViewRules
// 规范树形视图，见 authn/policyview.go）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
)

func TestPolicySetCoversIdentity(t *testing.T) {
	set, err := NewPolicySet()
	if err != nil {
		t.Fatalf("NewPolicySet: %v", err)
	}
	rules, err := authn.ViewRules(set)
	if err != nil {
		t.Fatalf("ViewRules: %v", err)
	}
	byMethod := make(map[string]authn.MethodRule, len(rules))
	for _, r := range rules {
		byMethod[r.Method] = r
	}
	cases := []struct {
		method string
		public bool
		scope  *identity.ScopeRule
	}{
		{"/fleetly.identity.v1.UsersService/WhoAmI", true, nil},
		{"/fleetly.identity.v1.UsersService/CreateUser", false, &identity.ScopeRule{Resource: "users", Op: identity.OpWrite}},
		{"/fleetly.identity.v1.TokensService/RevokeToken", false, &identity.ScopeRule{Resource: "tokens", Op: identity.OpAdmin}},
		{"/fleetly.identity.v1.InvitationsService/AcceptInvitation", true, nil},
		{"/fleetly.identity.v1.AuditQueryService/ListAudit", false, &identity.ScopeRule{Resource: "audit", Op: identity.OpRead}},
		{"/fleetly.system.v1.SystemService/GetStatus", true, nil},
		{"/fleetly.structure.v1.ProjectsService/CreateProject", false, &identity.ScopeRule{Resource: "projects", Op: identity.OpWrite}},
	}
	for _, tc := range cases {
		got, ok := byMethod[tc.method]
		if !ok {
			t.Fatalf("method %s missing from policy set", tc.method)
		}
		if got.Public != tc.public {
			t.Fatalf("%s: public = %v, want %v", tc.method, got.Public, tc.public)
		}
		if tc.scope == nil {
			if got.Scope != nil {
				t.Fatalf("%s: unexpected scope rule %+v", tc.method, got.Scope)
			}
			continue
		}
		if got.Scope == nil || *got.Scope != *tc.scope {
			t.Fatalf("%s: scope rule = %+v, want %+v", tc.method, got.Scope, tc.scope)
		}
	}
}

func TestScopeResourcesCoverVocabulary(t *testing.T) {
	// 词表每资源至少被一个方法引用（死 scope 由 Build 断言把守；本测试
	// 反向钉 identity 六资源确实在词表内——登记遗漏即刻红）。
	resources := ScopeResources()
	want := map[string]bool{
		"users": false, "teams": false, "roles": false,
		"tokens": false, "invitations": false, "audit": false,
	}
	for _, r := range resources {
		if _, ok := want[r]; ok {
			want[r] = true
		}
	}
	for r, seen := range want {
		if !seen {
			t.Fatalf("identity resource %q missing from scopeResources", r)
		}
	}
}
