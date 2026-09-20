package runtime

import (
	"path/filepath"
	"testing"
)

// E1-1 安装项与配置的多节点键位测试（E1 多节点设计 §2.2/§2.3/§2.4/§2.5）：
//   - join.token_rotate 缺省 auto（D-MN-1），manual 显式 opt-out，未知值
//     一律回落 auto（保守缺省 = 安全默认）；
//   - registry.auth_file 回落 <数据根>/fleetly-registry.auth（§2.5，与
//     ingress token 同形的 0600 凭据文件；数据根 = state 库同目录）；
//   - ingress.config_tls_addr 经 IngressSettings 透传给 ingress.Config
//     （缺省 0.0.0.0:8423 的回落单一事实源在 internal/ingress.Normalize；
//     装配期透传纪律与 ConfigAddr 同款——不经 Normalize 的空串会漂移）。
//   - ingress.base_domain 经 IngressSettings 透传（E1-3 接线：启用判定
//     在 Manager.ConfigTLSEnabled；本包不派生子域）。
//   - base_domain 是透传字符串（V2-7）：空 = 单节点 v0.1 形态。

func TestJoinTokenRotateDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty-defaults-to-auto", "", "auto"},
		{"auto-explicit", "auto", "auto"},
		{"manual-explicit", "manual", "manual"},
		{"manual-uppercase-falls-back", "MANUAL", "auto"},
		{"unknown-falls-back", "sometimes", "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := AppConfig{Join: JoinConfig{TokenRotate: tc.in}}
			if got := c.JoinTokenRotate(); got != tc.want {
				t.Errorf("JoinTokenRotate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRegistryAuthFileDefault(t *testing.T) {
	c := AppConfig{State: StateConfig{DBPath: "/var/lib/fleetly/fleetly.db"}}
	if got, want := c.RegistryAuthFile(), filepath.Join("/var/lib/fleetly", "fleetly-registry.auth"); got != want {
		t.Errorf("RegistryAuthFile() default = %q, want %q", got, want)
	}
}

func TestRegistryAuthFileExplicit(t *testing.T) {
	c := AppConfig{
		State:    StateConfig{DBPath: "/var/lib/fleetly/fleetly.db"},
		Registry: RegistryConfig{AuthFile: "/etc/fleetly/registry.auth"},
	}
	if got := c.RegistryAuthFile(); got != "/etc/fleetly/registry.auth" {
		t.Errorf("RegistryAuthFile() explicit = %q, want /etc/fleetly/registry.auth", got)
	}
}

func TestIngressSettingsPassesConfigTLSAddr(t *testing.T) {
	explicitCfg := AppConfig{Ingress: IngressConfig{ConfigTLSAddr: "10.0.0.5:18423"}}
	explicit := explicitCfg.IngressSettings()
	if explicit.ConfigTLSAddr != "10.0.0.5:18423" {
		t.Errorf("IngressSettings().ConfigTLSAddr explicit = %q, want 10.0.0.5:18423", explicit.ConfigTLSAddr)
	}

	emptyCfg := AppConfig{}
	empty := emptyCfg.IngressSettings()
	if empty.ConfigTLSAddr != "0.0.0.0:8423" {
		t.Errorf("IngressSettings().ConfigTLSAddr default = %q, want 0.0.0.0:8423 (ingress.Normalize fallback)", empty.ConfigTLSAddr)
	}
}

// TestIngressSettingsPassesBaseDomain base_domain 透传（E1-3）：非空原样
// 进入 ingress.Config（启用 8423 TLS 面 + 平台证书 duty 的判定输入）；空 =
// 单节点形态（ingress 侧零行为差异）。
func TestIngressSettingsPassesBaseDomain(t *testing.T) {
	setCfg := AppConfig{BaseDomain: "example.com"}
	set := setCfg.IngressSettings()
	if set.BaseDomain != "example.com" {
		t.Errorf("IngressSettings().BaseDomain = %q, want example.com", set.BaseDomain)
	}
	emptyCfg := AppConfig{}
	empty := emptyCfg.IngressSettings()
	if empty.BaseDomain != "" {
		t.Errorf("IngressSettings().BaseDomain empty = %q, want empty string", empty.BaseDomain)
	}
}
