package identity

// 域内核 hermetic 测试：scope 语言（解析/蕴含/转译）、token 材质、内置
// 角色随词表构造。

import (
	"strings"
	"testing"
)

var vocab = []string{
	"projects", "apps", "deployments", "secrets", "users", "tokens", "audit",
}

func TestParseScope(t *testing.T) {
	cases := []struct {
		raw      string
		want     Scope
		wantErr  bool
		errParts []string
	}{
		{raw: "projects:write", want: Scope{Resource: "projects", Action: "write"}},
		{raw: "audit:read", want: Scope{Resource: "audit", Action: "read"}},
		{raw: "projects", wantErr: true, errParts: []string{"want resource:action"}},
		{raw: "projects:own", wantErr: true, errParts: []string{"unknown action"}},
		{raw: "nope:read", wantErr: true, errParts: []string{"not in the scope vocabulary"}},
		{raw: ":read", wantErr: true, errParts: []string{"want resource:action"}},
	}
	for _, tc := range cases {
		got, err := ParseScope(tc.raw, vocab)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseScope(%q): want error, got %+v", tc.raw, got)
			}
			for _, part := range tc.errParts {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("ParseScope(%q) error %q missing %q", tc.raw, err, part)
				}
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseScope(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseScope(%q) = %+v, want %+v", tc.raw, got, tc.want)
		}
		if got.String() != tc.raw {
			t.Fatalf("String() = %q, want %q", got.String(), tc.raw)
		}
	}
}

func TestScopeSetExpandsImplications(t *testing.T) {
	set, err := ScopeSet([]Scope{
		{Resource: "deployments", Action: "admin"},
		{Resource: "secrets", Action: "write"},
	})
	if err != nil {
		t.Fatalf("ScopeSet: %v", err)
	}
	// admin ⇒ write ⇒ read。
	for _, rule := range []ScopeRule{
		{Resource: "deployments", Op: OpAdmin},
		{Resource: "deployments", Op: OpWrite},
		{Resource: "deployments", Op: OpRead},
		{Resource: "secrets", Op: OpWrite},
		{Resource: "secrets", Op: OpRead},
	} {
		if !set.Satisfies(rule.Resource, rule.Op) {
			t.Fatalf("expanded set %#v should satisfy %#v", set, rule)
		}
	}
	// write 不上蕴含 admin；未授权资源不满足。
	for _, rule := range []ScopeRule{
		{Resource: "secrets", Op: OpAdmin},
		{Resource: "apps", Op: OpRead},
	} {
		if set.Satisfies(rule.Resource, rule.Op) {
			t.Fatalf("set must not satisfy %#v", rule)
		}
	}
	// 通配符恒真（owner 全权形态）。
	star, err := ScopeSet([]Scope{{Resource: "*", Action: "*"}})
	if err != nil {
		t.Fatalf("ScopeSet(star): %v", err)
	}
	if !star.Satisfies("users", OpAdmin) {
		t.Fatal("star scope must satisfy any rule")
	}
}

func TestExpandColonForm(t *testing.T) {
	got := Expand([]Scope{{Resource: "apps", Action: "write"}})
	want := []string{"apps:read", "apps:write"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Expand = %v, want %v", got, want)
	}
}

func TestTokenMaterial(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if !strings.HasPrefix(tok.Secret, "flt_") {
		t.Fatalf("secret %q missing flt_ prefix", tok.Secret)
	}
	if len(tok.Secret) != len(TokenPrefix)+43 {
		t.Fatalf("secret length = %d, want %d (256-bit entropy)", len(tok.Secret), len(TokenPrefix)+43)
	}
	if tok.Prefix != tok.Secret[:12] {
		t.Fatalf("prefix %q != first 12 of secret", tok.Prefix)
	}
	if tok.SHA256 != HashToken(tok.Secret) {
		t.Fatal("SHA256 mismatch with HashToken")
	}
	if len(tok.SHA256) != 64 {
		t.Fatalf("sha256 length = %d, want 64", len(tok.SHA256))
	}
	if TokenKind(tok.Secret) != "platform" {
		t.Fatal("TokenKind should classify platform")
	}

	inv, err := NewInvitation()
	if err != nil {
		t.Fatalf("NewInvitation: %v", err)
	}
	if !strings.HasPrefix(inv.Secret, "fltinv_") {
		t.Fatalf("invitation secret %q missing fltinv_ prefix", inv.Secret)
	}
	if TokenKind(inv.Secret) != "invitation" {
		t.Fatal("TokenKind should classify invitation")
	}
	if TokenKind("ghp_xxx") != "unknown" {
		t.Fatal("TokenKind should classify unknown")
	}

	// 材质随机性（两次生成不撞）。
	other, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken(2): %v", err)
	}
	if other.Secret == tok.Secret || other.SHA256 == tok.SHA256 {
		t.Fatal("two generations must differ")
	}
}

func TestBuiltinRoles(t *testing.T) {
	roles := BuiltinRoles(vocab)
	byID := map[string]BuiltinRole{}
	for _, r := range roles {
		byID[r.ID] = r
	}
	if len(roles) != 3 {
		t.Fatalf("builtin roles = %d, want 3", len(roles))
	}

	// owner = *。
	owner, err := ScopeSet(byID[RoleOwnerID].Scopes)
	if err != nil {
		t.Fatalf("owner ScopeSet: %v", err)
	}
	if !owner.Satisfies("anything", OpAdmin) {
		t.Fatal("owner must satisfy any rule")
	}

	// admin = 全资源 :admin、audit 只 read。
	admin := byID[RoleAdminID].Scopes
	adminSet, err := ScopeSet(admin)
	if err != nil {
		t.Fatalf("admin ScopeSet: %v", err)
	}
	for _, res := range vocab {
		want := OpWrite
		if res == "audit" {
			want = OpRead
		}
		if !adminSet.Satisfies(res, want) {
			t.Fatalf("admin must cover %s %s", res, want)
		}
	}
	if adminSet.Satisfies("audit", OpAdmin) {
		t.Fatal("admin must not carry audit:admin (audit is read-only surface)")
	}

	// member = 全资源 read + 部署/材料 write；无 identity/管理面。
	memberSet, err := ScopeSet(byID[RoleMemberID].Scopes)
	if err != nil {
		t.Fatalf("member ScopeSet: %v", err)
	}
	if !memberSet.Satisfies("projects", OpRead) {
		t.Fatal("member must read everything")
	}
	if !memberSet.Satisfies("deployments", OpWrite) {
		t.Fatal("member must write deployments")
	}
	for _, rule := range []ScopeRule{
		{Resource: "projects", Op: OpWrite},
		{Resource: "users", Op: OpWrite},
		{Resource: "tokens", Op: OpRead},
		{Resource: "audit", Op: OpRead},
	} {
		if memberSet.Satisfies(rule.Resource, rule.Op) {
			t.Fatalf("member must not satisfy %#v", rule)
		}
	}
}
