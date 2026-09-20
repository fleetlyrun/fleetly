package ingress

import (
	"testing"
	"time"
)

// TestConfigNormalizeDefaults 钉住 Normalize 的全量缺省形态（E1-1 新增
// config_tls_addr 缺省 0.0.0.0:8423——设计 §2.4；v0.1 既有缺省逐项回归，
// 缺省不得漂移）。
func TestConfigNormalizeDefaults(t *testing.T) {
	cfg := Config{}.Normalize()

	if cfg.TraefikImage != DefaultTraefikImage {
		t.Errorf("TraefikImage default = %q, want %q", cfg.TraefikImage, DefaultTraefikImage)
	}
	if cfg.HTTPPort != 80 {
		t.Errorf("HTTPPort default = %d, want 80", cfg.HTTPPort)
	}
	if cfg.HTTPSPort != 443 {
		t.Errorf("HTTPSPort default = %d, want 443", cfg.HTTPSPort)
	}
	if cfg.ConfigAddr != "0.0.0.0:8422" {
		t.Errorf("ConfigAddr default = %q, want %q", cfg.ConfigAddr, "0.0.0.0:8422")
	}
	if cfg.ConfigTLSAddr != DefaultConfigTLSAddr {
		t.Errorf("ConfigTLSAddr default = %q, want %q", cfg.ConfigTLSAddr, DefaultConfigTLSAddr)
	}
	if cfg.ConfigTLSAddr != "0.0.0.0:8423" {
		t.Errorf("ConfigTLSAddr default = %q, want 0.0.0.0:8423 (design section 2.4)", cfg.ConfigTLSAddr)
	}
	if cfg.TokenFile != "fleetly-ingress.token" {
		t.Errorf("TokenFile default = %q, want fleetly-ingress.token", cfg.TokenFile)
	}
	if cfg.CertDir != "fleetly-certs" {
		t.Errorf("CertDir default = %q, want fleetly-certs", cfg.CertDir)
	}
	if cfg.RenewBefore != DefaultRenewBefore {
		t.Errorf("RenewBefore default = %s, want %s", cfg.RenewBefore, DefaultRenewBefore)
	}
	if cfg.RenewScanInterval != DefaultRenewScanInterval {
		t.Errorf("RenewScanInterval default = %s, want %s", cfg.RenewScanInterval, DefaultRenewScanInterval)
	}
	if cfg.PollInterval != 2*time.Second {
		t.Errorf("PollInterval default = %s, want 2s", cfg.PollInterval)
	}
	if !cfg.ACME.ACMEEnabled() {
		t.Error("ACME enabled default = false, want true")
	}
}

// TestConfigNormalizeExplicitValuesKept 钉住显式值不被缺省覆盖（含新增的
// config_tls_addr——两套配置端点地址可独立覆盖，互不串写）。
func TestConfigNormalizeExplicitValuesKept(t *testing.T) {
	cfg := Config{
		ConfigAddr:    "127.0.0.1:18422",
		ConfigTLSAddr: "127.0.0.1:18423",
	}.Normalize()

	if cfg.ConfigAddr != "127.0.0.1:18422" {
		t.Errorf("ConfigAddr explicit = %q, want 127.0.0.1:18422", cfg.ConfigAddr)
	}
	if cfg.ConfigTLSAddr != "127.0.0.1:18423" {
		t.Errorf("ConfigTLSAddr explicit = %q, want 127.0.0.1:18423", cfg.ConfigTLSAddr)
	}
}

// TestDefaultConfigTLSAddrIsLiteral8423 直接钉常量字面值：设计 §2.4 的
// 端口分面表以 8423 为契约数字（安装报告、join 向导放行清单都引用它），
// 静默改值会撕跨三处引用。
func TestDefaultConfigTLSAddrIsLiteral8423(t *testing.T) {
	if DefaultConfigTLSAddr != "0.0.0.0:8423" {
		t.Errorf("DefaultConfigTLSAddr = %q, want literal 0.0.0.0:8423", DefaultConfigTLSAddr)
	}
}
