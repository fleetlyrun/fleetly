package assembly

// PolicySet 构造面测试：词表与 proto 注解的咬合（fail-closed 断言在
// authz.Build 构造期爆掉；本测试钉住 identity 面的关键策略投影）。

import (
	"testing"

	"github.com/lynx-go/grpcapi/authz"
)

func TestPolicySetCoversIdentity(t *testing.T) {
	set, err := NewPolicySet()
	if err != nil {
		t.Fatalf("NewPolicySet: %v", err)
	}
	cases := []struct {
		method string
		access authz.AccessLevel
		scope  *authz.ScopeRule
	}{
		{"/fleetly.identity.v1.UsersService/WhoAmI", authz.AccessPublic, nil},
		{"/fleetly.identity.v1.UsersService/CreateUser", authz.AccessServer, &authz.ScopeRule{Resource: "users", Op: authz.ScopeWrite}},
		{"/fleetly.identity.v1.TokensService/RevokeToken", authz.AccessServer, &authz.ScopeRule{Resource: "tokens", Op: authz.ScopeAdmin}},
		{"/fleetly.identity.v1.InvitationsService/AcceptInvitation", authz.AccessPublic, nil},
		{"/fleetly.identity.v1.AuditQueryService/ListAudit", authz.AccessServer, &authz.ScopeRule{Resource: "audit", Op: authz.ScopeRead}},
		{"/fleetly.system.v1.SystemService/GetStatus", authz.AccessPublic, nil},
		{"/fleetly.structure.v1.ProjectsService/CreateProject", authz.AccessServer, &authz.ScopeRule{Resource: "projects", Op: authz.ScopeWrite}},
	}
	for _, tc := range cases {
		got, ok := set.Get(tc.method)
		if !ok {
			t.Fatalf("method %s missing from policy set", tc.method)
		}
		if got.Access != tc.access {
			t.Fatalf("%s: access = %s, want %s", tc.method, got.Access, tc.access)
		}
		gotRule := set.ScopeRule(tc.method)
		if tc.scope == nil {
			if gotRule != nil {
				t.Fatalf("%s: unexpected scope rule %+v", tc.method, gotRule)
			}
			continue
		}
		if gotRule == nil || *gotRule != *tc.scope {
			t.Fatalf("%s: scope rule = %+v, want %+v", tc.method, gotRule, tc.scope)
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
