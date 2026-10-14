package entityid

import (
	"strings"
	"testing"
)

func TestJoin(t *testing.T) {
	cases := []struct {
		base string
		seq  int64
		want string
	}{
		{"web", 42, "web-42"},
		{"torchwood", 7, "torchwood-7"},
		{"nightly-backup", 1234567, "nightly-backup-1234567"},
		{"web-m2x8", 88, "web-m2x8-88"}, // 派生链：base 是父 id
		{"tok", 15, "tok-15"},
	}
	for _, c := range cases {
		if got := Join(c.base, c.seq); got != c.want {
			t.Errorf("Join(%q, %d) = %q, want %q", c.base, c.seq, got, c.want)
		}
		if !Valid(Join(c.base, c.seq)) {
			t.Errorf("Join(%q, %d) 应满 Valid", c.base, c.seq)
		}
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"web-42", true},
		{"web-42-88", true},
		{"web-42-r7", true},
		{"aud-8492", true},
		{"node-3", true},
		{"nightly-backup-33", true},
		{"0-x7f2", true},   // 段可纯数字
		{"Web-42", false},  // 大写不收
		{"web42", false},   // 无连字符（无 base-suffix 结构）
		{"-web-42", false}, // 首连字符
		{"web-42-", false}, // 尾连字符
		{"web--42", false}, // 连续连字符
		{"", false},        //
		{"web_42", false},  // 下划线不收
		{"web-4!2", false}, // 非法字符
		{"web-" + strings.Repeat("a", MaxLen), false}, // 超长
	}
	for _, c := range cases {
		if got := Valid(c.id); got != c.want {
			t.Errorf("Valid(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestValidName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"web", true},
		{"web-app", true},
		{"a", true},
		{"0abc", true},
		{"Web", false},  // 大写不收
		{"-web", false}, // 首连字符
		{"web-", false}, // 尾连字符
		{"", false},     //
		{"a_b", false},  // 下划线
		{strings.Repeat("a", MaxNameLen), true},
		{strings.Repeat("a", MaxNameLen+1), false},
	}
	for _, c := range cases {
		if got := ValidName(c.name); got != c.want {
			t.Errorf("ValidName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDeriveDeterministic(t *testing.T) {
	if got, want := Revision("web-42", 7), "web-42-r7"; got != want {
		t.Errorf("Revision = %q, want %q", got, want)
	}
	if got, want := Audit(8492), "aud-8492"; got != want {
		t.Errorf("Audit = %q, want %q", got, want)
	}
	if !Valid(Revision("web-42", 7)) || !Valid(Audit(8492)) {
		t.Error("派生 id 应满 Valid 全形")
	}
}
