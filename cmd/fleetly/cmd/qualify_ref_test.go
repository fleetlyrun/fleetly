package cmd

// qualifyRef 单测（v0.3 W2-S4 CLI 参数面同步）：裸名在 CLI 上下文（team+
// project 齐备）内补全为三段限定形；限定形/缺上下文/空引用原样返回——
// 解析语义权威在服务端，客户端补全只是消歧便利。

import "testing"

func TestQualifyRef(t *testing.T) {
	rc := resolvedContext{Team: "acme", Project: "prod"}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare app gets qualified", "web", "acme/prod/web"},
		{"bare database gets qualified", "pg1", "acme/prod/pg1"},
		{"qualified app passes through", "acme/prod/web", "acme/prod/web"},
		{"two-segment project ref passes through", "acme/prod", "acme/prod"},
		{"id-like refs pass through", "01HQZ0abcdefghjkmnpqrstvwx", "01HQZ0abcdefghjkmnpqrstvwx"},
		{"whitespace trimmed", "  web  ", "acme/prod/web"},
	}
	for _, tc := range cases {
		if got := qualifyRef(rc, tc.in); got != tc.want {
			t.Errorf("%s: qualifyRef(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// 缺上下文：任一轴缺失即原样返回（服务端按可见域解析）。
	for _, rc := range []resolvedContext{
		{}, {Team: "acme"}, {Project: "prod"},
	} {
		if got := qualifyRef(rc, "web"); got != "web" {
			t.Errorf("missing context: qualifyRef = %q, want bare %q", got, "web")
		}
	}
}
