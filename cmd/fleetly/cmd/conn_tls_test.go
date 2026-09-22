package cmd

// V2-8（E7 同批）CLI TLS 旗标测试：--tls/--tls-insecure 互斥、FLEETLY_TLS
// env 三值解析（true|insecure|非法值）、旗标 > env 优先级、缺省明文（零
// 旗标零 env = nil 配置——存量兼容的解析面保证）、-h 面出现新旗标。

import (
	"crypto/tls"
	"strings"
	"testing"
)

// tlsConfigOf 把 resolveTLS 结果按「是否 TLS / 是否跳过校验」归一，便于表
// 断言（nil = 明文）。
type tlsShape struct {
	tls      bool
	insecure bool
}

func shapeOf(t *testing.T, f *connFlags) tlsShape {
	t.Helper()
	cfg, err := f.resolveTLS()
	if err != nil {
		t.Fatalf("resolveTLS: %v", err)
	}
	if cfg == nil {
		return tlsShape{}
	}
	return tlsShape{tls: true, insecure: cfg.InsecureSkipVerify}
}

// TestResolveTLSFlagsMutualExclusion 两旗标同传 = 可行动错误（不静默降级）。
func TestResolveTLSFlagsMutualExclusion(t *testing.T) {
	f := &connFlags{tlsFlag: true, tlsInsecureFlag: true}
	cfg, err := f.resolveTLS()
	if err == nil {
		t.Fatal("--tls together with --tls-insecure must fail")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("mutual exclusion error lacks actionable text: %v", err)
	}
	if cfg != nil {
		t.Fatal("mutual exclusion must not return a config")
	}
}

// TestResolveTLSEnvironment FLEETLY_TLS 解析：true → 校验、insecure → 跳过、
// 空串 → 明文、非法值 → 报错（大小写/空白宽容）。
func TestResolveTLSEnvironment(t *testing.T) {
	cases := []struct {
		env     string
		want    tlsShape
		wantErr bool
	}{
		{env: "", want: tlsShape{}},
		{env: "true", want: tlsShape{tls: true}},
		{env: "TRUE", want: tlsShape{tls: true}},
		{env: " insecure ", want: tlsShape{tls: true, insecure: true}},
		{env: "maybe", wantErr: true},
	}
	for _, tt := range cases {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv("FLEETLY_TLS", tt.env)
			f := &connFlags{}
			_, err := f.resolveTLS()
			if (err != nil) != tt.wantErr {
				t.Fatalf("env %q: err=%v, wantErr=%v", tt.env, err, tt.wantErr)
			}
			if !tt.wantErr && shapeOf(t, f) != tt.want {
				t.Fatalf("env %q: shape=%v, want %v", tt.env, shapeOf(t, f), tt.want)
			}
		})
	}
	// 非法 env 的错误可行动（列出合法值）。
	t.Setenv("FLEETLY_TLS", "maybe")
	_, err := (&connFlags{}).resolveTLS()
	if err == nil || !strings.Contains(err.Error(), "true, insecure") {
		t.Fatalf("invalid env error lacks supported values: %v", err)
	}
}

// TestResolveTLSFlagOverridesEnv 显式旗标优先于 env（与 --addr/--token 同
// 款纪律）；env 置 insecure 而旗标 --tls 时按旗标（校验）。
func TestResolveTLSFlagOverridesEnv(t *testing.T) {
	t.Setenv("FLEETLY_TLS", "insecure")
	got := shapeOf(t, &connFlags{tlsFlag: true})
	if got != (tlsShape{tls: true}) {
		t.Fatalf("--tls over env insecure: %v, want verified TLS", got)
	}
	// env 置 true 而旗标 --tls-insecure 时按旗标（跳过校验）。
	t.Setenv("FLEETLY_TLS", "true")
	got = shapeOf(t, &connFlags{tlsInsecureFlag: true})
	if got != (tlsShape{tls: true, insecure: true}) {
		t.Fatalf("--tls-insecure over env true: %v, want insecure TLS", got)
	}
}

// TestResolveTLSDefaultPlaintext 零旗标 + 零 env = nil 配置（明文拨号，存
// 量兼容的解析面回归）。
func TestResolveTLSDefaultPlaintext(t *testing.T) {
	t.Setenv("FLEETLY_TLS", "")
	got := shapeOf(t, &connFlags{})
	if got != (tlsShape{}) {
		t.Fatalf("default shape = %v, want plaintext", got)
	}
}

// TestResolveTLSClientConfigQuality --tls 产出的客户端配置：MinVersion ≥
// TLS1.2；--tls-insecure 产出的配置是显式跳过校验形态。
func TestResolveTLSClientConfigQuality(t *testing.T) {
	verified, err := (&connFlags{tlsFlag: true}).resolveTLS()
	if err != nil {
		t.Fatalf("--tls: %v", err)
	}
	if verified.MinVersion < tls.VersionTLS12 {
		t.Fatalf("--tls MinVersion = %x, want >= TLS1.2", verified.MinVersion)
	}
	if verified.InsecureSkipVerify {
		t.Fatal("--tls must verify certificates")
	}
	insecure, err := (&connFlags{tlsInsecureFlag: true}).resolveTLS()
	if err != nil {
		t.Fatalf("--tls-insecure: %v", err)
	}
	if !insecure.InsecureSkipVerify {
		t.Fatal("--tls-insecure must skip verification (explicit opt-out semantics)")
	}
}

// TestTLSFlagsInVerbHelp 动词帮助面出现两个新旗标且互斥语义可见（conn
// flags 全动词继承的注册面回归）。
func TestTLSFlagsInVerbHelp(t *testing.T) {
	code, out, _ := runCLI(t, "apps", "list", "-h")
	if code != 0 {
		t.Fatalf("apps list -h: code=%d", code)
	}
	for _, want := range []string{"-tls", "-tls-insecure", "FLEETLY_TLS"} {
		if !strings.Contains(out, want) {
			t.Errorf("verb help missing %q:\n%s", want, out)
		}
	}
}
