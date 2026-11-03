package entityid

import (
	"strings"
	"testing"
	"time"
)

// fixedTime 是铸造测试的固定时钟：确定性锚（时间经参数入域）。
var fixedTime = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func TestMintShape(t *testing.T) {
	restore := SwapForTest(zeroEntropy{})
	defer restore()
	id := Mint(fixedTime)
	if len(id) != 26 {
		t.Fatalf("Mint 长度 = %d, want 26（%q）", len(id), id)
	}
	if !ulidPattern.MatchString(id) {
		t.Errorf("Mint = %q 应满小写 crockford 形", id)
	}
	if !Valid(id) {
		t.Errorf("Mint = %q 应满 Valid", id)
	}
	// 固定时钟 + 零熵 ⇒ 全可复现（golden 钉串的前提）。
	if again := Mint(fixedTime); again != id {
		t.Errorf("固定源下 Mint 不确定：%q vs %q", id, again)
	}
}

func TestMintUnique(t *testing.T) {
	restore := SwapForTest(&counterEntropy{})
	defer restore()
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := Mint(fixedTime)
		if seen[id] {
			t.Fatalf("Mint 撞号：%q", id)
		}
		seen[id] = true
	}
}

func TestValid(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		// 新形：裸 ULID（crockford 小写，无连字符——i/l/o/u 出律）。
		{"01jbx8w2v4nq7r3t5y9a6d8hkc", true},
		{"7zzzzzzzzzzzzzzzzzzzzzzzzz", true},    // z 在 crockford 集内
		{"01jbx8w2v4nq7r3t5y9a6d8hki", false},   // i 出律
		{"01jbx8w2v4nq7r3t5y9a6d8hkl", false},   // l 出律
		{"01jbx8w2v4nq7r3t5y9a6d8hko", false},   // o 出律
		{"01jbx8w2v4nq7r3t5y9a6d8hku", false},   // u 出律
		{"01jbx8w2v4nq7r3t5y9a6d8hkc-d3", true}, // 派生形（ULID 父）
		{"01jbx8w2v4nq7r3t5y9a6d8hkc-r7", true}, // revision 派生形
		// 旧名基形：快照恢复共存面合法（铸造不再产出）。
		{"web-42", true},
		{"web-42-88", true},
		{"web-42-r7", true},
		{"aud-8492", true},
		{"node-3", true},
		{"nightly-backup-33", true},
		{"0-x7f2", true}, // 段可纯数字
		// 拒收面。
		{"Web-42", false},                             // 大写不收
		{"01JBX8W2V4NQ7R3T5Y9A6D8HKC", false},         // 大写 ULID 不收（全仓小写律）
		{"web42", false},                              // 无连字符且非 26 位（既非新形也非旧形）
		{"01jbx8w2v4nq7r3t5y9a6d8hk", false},          // 25 位非 ULID 长度
		{"01jbx8w2v4nq7r3t5y9a6d8hkcc", false},        // 27 位非 ULID 长度
		{"-web-42", false},                            // 首连字符
		{"web-42-", false},                            // 尾连字符
		{"web--42", false},                            // 连续连字符
		{"", false},                                   //
		{"web_42", false},                             // 下划线不收
		{"web-4!2", false},                            // 非法字符
		{"web-" + strings.Repeat("a", MaxLen), false}, // 超长
		{"01jbx8w2v4nq7r3t5y9a6d8hkc" + strings.Repeat("a", MaxLen), false}, // 超长（ULID 前缀 + 垃圾尾）
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
	if got, want := Revision("01jbx8w2v4nq7r3t5y9a6d8hkc", 7), "01jbx8w2v4nq7r3t5y9a6d8hkc-r7"; got != want {
		t.Errorf("Revision = %q, want %q", got, want)
	}
	if got, want := Derive("01jbx8w2v4nq7r3t5y9a6d8hkc", "d", 3), "01jbx8w2v4nq7r3t5y9a6d8hkc-d3"; got != want {
		t.Errorf("Derive = %q, want %q", got, want)
	}
	if !Valid(Revision("01jbx8w2v4nq7r3t5y9a6d8hkc", 7)) {
		t.Error("派生 id 应满 Valid 全形")
	}
}

// zeroEntropy 是恒零熵（固定源下可复现性验证用）。
type zeroEntropy struct{}

func (zeroEntropy) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// counterEntropy 是计数熵：10 字节大端计数器逐铸造递增（唯一性验证用；
// 朴素 byte 计数会在 256 次后回绕撞号）。
type counterEntropy struct{ n uint64 }

func (c *counterEntropy) Read(p []byte) (int, error) {
	c.n++
	for i := 0; i < len(p) && i < 8; i++ {
		p[len(p)-1-i] = byte(c.n >> (8 * i)) //nolint:gosec // G115 误报：测试计数熵的有意截断
	}
	return len(p), nil
}
