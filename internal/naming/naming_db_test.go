package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestDbNamesMatchDesignDoc E4 数据库托管：库族命名函数输出与设计文档
// 字符串逐一相等（表驱动，managed-databases §5.4 文档原文钉死——新增
// 函数非改既有公式，与 app 名族解耦）。任何一侧改动都必须先改文档再改
// 此处。
func TestDbNamesMatchDesignDoc(t *testing.T) {
	const hashInput = "super-secret-value"
	sum := sha256.Sum256([]byte(hashInput))
	wantHash8 := hex.EncodeToString(sum[:])[:8]

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			// §5.4：`DBServiceName = fleetly-db-<name>-<service>`。
			name: "db service name",
			got:  must(t, func() (string, error) { return DBServiceName("pg-prod", "postgres") }),
			want: "fleetly-db-pg-prod-postgres",
		},
		{
			// §5.4：`DBNetworkName = fleetly-db-<name>-net`。
			name: "db network name",
			got:  must(t, func() (string, error) { return DBNetworkName("pg-prod") }),
			want: "fleetly-db-pg-prod-net",
		},
		{
			// §5.4：`DBVolumeName = fleetly-db-<name>-<key>-<id8>`。
			name: "db volume name",
			got:  must(t, func() (string, error) { return DBVolumeName("pg-prod", "data", "01JABCDEFGH") }),
			want: "fleetly-db-pg-prod-data-01JABCDE",
		},
		{
			// §5.4：`DBSecretName = fleetly-db-<name>-<secret>-<hash8>`。
			name: "db secret name",
			got:  must(t, func() (string, error) { return DBSecretName("pg-prod", "password", wantHash8) }),
			want: "fleetly-db-pg-prod-password-" + wantHash8,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q (design doc verbatim)", tc.got, tc.want)
			}
		})
	}
}

// TestDbServiceNamePredicate IsDbServiceName 前缀识别（引擎对账豁免面）：
// 库族（fleetly-db-）识别为真；app 族与 cron 族服务名不误判。
func TestDbServiceNamePredicate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"db service", "fleetly-db-pg-prod-postgres", true},
		{"db bare prefix", "fleetly-db-", true},
		{"app service", "fleetly-my-api-web", false},
		{"cron job", "fleetly-cron-my-api-web-01JABCDE", false},
		{"db prefix inside name", "x-fleetly-db-pg", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := IsDbServiceName(tc.in); got != tc.want {
			t.Errorf("IsDbServiceName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDbNameValidation 库族命名的非法成分拒绝（复用 validateComponent
// 词表：空串、越界字符、分隔层级注入、短 ID、非法 hash8）。
func TestDbNameValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"empty instance", func() (string, error) { return DBServiceName("", "postgres") }},
		{"empty service", func() (string, error) { return DBServiceName("pg-prod", "") }},
		{"slash injection", func() (string, error) { return DBServiceName("a/b", "postgres") }},
		{"colon injection", func() (string, error) { return DBNetworkName("pg:prod") }},
		{"space in key", func() (string, error) { return DBVolumeName("pg-prod", "da ta", "01JABCDEFGH") }},
		{"short instance id", func() (string, error) { return DBVolumeName("pg-prod", "data", "01JA") }},
		{"empty hash8", func() (string, error) { return DBSecretName("pg-prod", "password", "") }},
		{"uppercase hash8", func() (string, error) { return DBSecretName("pg-prod", "password", "ABCDEF12") }},
	} {
		if _, err := tc.fn(); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

// TestDbSecretNameRotationIsNewName 值轮换即换名换引用（库 secret 与 app
// secret 同纪律：hash8 参与命名，desired-hash 随引用变更触发重部署）。
func TestDbSecretNameRotationIsNewName(t *testing.T) {
	old := must(t, func() (string, error) { return DBSecretName("pg-prod", "password", Hash8("value-v1")) })
	rotated := must(t, func() (string, error) { return DBSecretName("pg-prod", "password", Hash8("value-v2")) })
	if old == rotated {
		t.Fatalf("rotated db secret name unchanged: %s", old)
	}
	if !IsDbServiceName(old) || !IsDbServiceName(rotated) {
		t.Fatalf("db secret names must carry the fleetly-db- prefix: %s / %s", old, rotated)
	}
}
