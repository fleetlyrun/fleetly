package entityid

import (
	"errors"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"web-m2x8", true},
		{"web-m2x8-9f3k", true},
		{"web-m2x8-r42", true},
		{"aud-123456", true},
		{"node-9f3k", true},
		{"0-x7f2", true},      // 段可纯数字
		{"web-m2x8", true},    //
		{"Web-m2x8", false},   // 大写不收
		{"webm2x8", false},    // 无连字符（无 base-suffix 结构）
		{"-web-m2x8", false},  // 首连字符
		{"web-m2x8-", false},  // 尾连字符
		{"web--m2x8", false},  // 连续连字符
		{"", false},           //
		{"web_m2x8", false},   // 下划线不收
		{"web-m2x!", false},   // 非法字符
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
		{"Web", false},       // 大写不收
		{"-web", false},      // 首连字符
		{"web-", false},      // 尾连字符
		{"", false},          //
		{"a_b", false},       // 下划线
		{strings.Repeat("a", MaxNameLen), true},
		{strings.Repeat("a", MaxNameLen+1), false},
	}
	for _, c := range cases {
		if got := ValidName(c.name); got != c.want {
			t.Errorf("ValidName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMint(t *testing.T) {
	t.Run("自由铸造命中首候选", func(t *testing.T) {
		id, err := Mint("web", func(string) (bool, error) { return false, nil })
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "web-") || len(id) != len("web-")+SuffixLen {
			t.Fatalf("Mint = %q，形态不符", id)
		}
		for _, r := range strings.TrimPrefix(id, "web-") {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("Mint = %q，suffix 字符 %q 越出 Crockford 表", id, r)
			}
		}
	})
	t.Run("撞名重试", func(t *testing.T) {
		calls := 0
		id, err := Mint("web", func(string) (bool, error) {
			calls++
			return calls <= 3, nil // 前三候选被占
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 4 {
			t.Fatalf("第 %d 次才应命中，实为 %d", 4, calls)
		}
		if !strings.HasPrefix(id, "web-") {
			t.Fatalf("Mint = %q", id)
		}
	})
	t.Run("tombstone 命中参与查重", func(t *testing.T) {
		id, err := Mint("web", func(id string) (bool, error) {
			return id == "web-aaaa", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if id == "web-aaaa" {
			t.Fatal("已删除（tombstone）id 被重铸——never-reuse 破约")
		}
	})
	t.Run("exists 错误透传", func(t *testing.T) {
		sentinel := errors.New("boom")
		if _, err := Mint("web", func(string) (bool, error) { return false, sentinel }); !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want %v", err, sentinel)
		}
	})
	t.Run("重试耗尽", func(t *testing.T) {
		if _, err := Mint("web", func(string) (bool, error) { return true, nil }); !errors.Is(err, ErrMintExhausted) {
			t.Fatalf("err = %v, want ErrMintExhausted", err)
		}
	})
	t.Run("base 非法拒铸", func(t *testing.T) {
		for _, base := range []string{"", "Web", "web-", "a_b", strings.Repeat("a", MaxNameLen+1)} {
			if _, err := Mint(base, func(string) (bool, error) { return false, nil }); err == nil {
				t.Errorf("Mint(%q) 应拒铸", base)
			}
		}
	})
	t.Run("结果全形合法", func(t *testing.T) {
		for _, base := range []string{"web", "tok", "node"} {
			id, err := Mint(base, func(string) (bool, error) { return false, nil })
			if err != nil {
				t.Fatal(err)
			}
			if !Valid(id) {
				t.Fatalf("Mint(%q) = %q 不满 Valid", base, id)
			}
		}
	})
}

func TestDeriveDeterministic(t *testing.T) {
	if got, want := Revision("web-m2x8", 42), "web-m2x8-r42"; got != want {
		t.Errorf("Revision = %q, want %q", got, want)
	}
	if got, want := Audit(7), "aud-7"; got != want {
		t.Errorf("Audit = %q, want %q", got, want)
	}
	if !Valid(Revision("web-m2x8", 42)) || !Valid(Audit(7)) {
		t.Error("派生 id 应满 Valid 全形")
	}
}
