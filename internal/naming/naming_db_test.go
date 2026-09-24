package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestDbNamesMatchDesignDoc E4 数据库托管 × rbac-teams §4.3 库行：库族命名
// 函数输出与设计文档字符串逐一相等（表驱动，managed-databases §5.4 +
// rbac-teams §4.3 文档原文钉死——服务/网络/secret 三段化，卷公式不变）。
// 任何一侧改动都必须先改文档再改此处。
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
			// rbac-teams §4.3 库行：`fleetly-db-<team>-<prj>-<name>-<service>`。
			name: "db service name",
			got:  must(t, func() (string, error) { return DBServiceName("acme", "prod", "pg-prod", "postgres") }),
			want: "fleetly-db-acme-prod-pg-prod-postgres",
		},
		{
			// rbac-teams §4.3 库行：`fleetly-db-<team>-<prj>-<name>-net`。
			name: "db network name",
			got:  must(t, func() (string, error) { return DBNetworkName("acme", "prod", "pg-prod") }),
			want: "fleetly-db-acme-prod-pg-prod-net",
		},
		{
			// rbac-teams §4.3 库卷行「不变」：`fleetly-db-<name>-<key>-<id8>`。
			name: "db volume name (v0.3 formula unchanged)",
			got:  must(t, func() (string, error) { return DBVolumeName("pg-prod", "data", "01JABCDEFGH") }),
			want: "fleetly-db-pg-prod-data-01JABCDE",
		},
		{
			// 库 secret 三段化：`fleetly-db-<team>-<prj>-<name>-<secret>-<hash8>`。
			name: "db secret name",
			got:  must(t, func() (string, error) { return DBSecretName("acme", "prod", "pg-prod", "password", wantHash8) }),
			want: "fleetly-db-acme-prod-pg-prod-password-" + wantHash8,
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

// TestDbServiceNamePredicate IsDbServiceName 前缀识别（引擎对账豁免面；
// 前缀族 v0.3 不变）：库族（fleetly-db-，含三段与旧两段成员）识别为真；
// app 族与 cron 族服务名不误判。
func TestDbServiceNamePredicate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"db service (three-segment)", "fleetly-db-acme-prod-pg-prod-postgres", true},
		{"db service (bare)", "fleetly-db-pg-prod-postgres", true},
		{"db bare prefix", "fleetly-db-", true},
		{"app service", "fleetly-acme-prod-my-api-web", false},
		{"cron job", "fleetly-cron-acme-prod-my-api-web-01JABCDE", false},
		{"db prefix inside name", "x-fleetly-db-pg", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := IsDbServiceName(tc.in); got != tc.want {
			t.Errorf("IsDbServiceName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestDbJobNameAndPredicate DBJobName / IsDBJobName（S5 一次性 job 前缀纪
// 律）：独立 fleetly-dbjob- 前缀族（**不**落在 fleetly-db- 服务族内——
// cron 孤儿清扫/引擎对账/收敛拍/日志管线按前缀与 label 边界豁免瞬时 job，
// 前缀不独立会被误伤）；ulid8 尾缀使重叠触发命名天然不冲突。v0.3：dbjob
// 族不在 rbac-teams §4.3 三段化清单——瞬时对象 + ulid8 天然互异，公式不变。
func TestDbJobNameAndPredicate(t *testing.T) {
	name, err := DBJobName("pg-prod", "backup", "01JABCDEFGH")
	if err != nil {
		t.Fatalf("DBJobName: %v", err)
	}
	if name != "fleetly-dbjob-pg-prod-backup-01JABCDE" {
		t.Errorf("DBJobName = %q, want the dbjob prefix family form", name)
	}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"db job", name, true},
		{"verify job", "fleetly-dbjob-pg-prod-verify-01JABCDE", true},
		{"db service (must NOT match — different family)", "fleetly-db-acme-prod-pg-prod-postgres", false},
		{"cron job", "fleetly-cron-acme-prod-my-api-web-01JABCDE", false},
		{"prefix inside name", "x-fleetly-dbjob-a", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		if got := IsDBJobName(tc.in); got != tc.want {
			t.Errorf("IsDBJobName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"empty instance", func() (string, error) { return DBJobName("", "backup", "01JABCDEFGH") }},
		{"empty purpose", func() (string, error) { return DBJobName("pg-prod", "", "01JABCDEFGH") }},
		{"short run id", func() (string, error) { return DBJobName("pg-prod", "backup", "01JA") }},
	} {
		if _, err := tc.fn(); err == nil {
			t.Errorf("%s: expected error", tc.name)
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
		{"empty team", func() (string, error) { return DBServiceName("", "prod", "pg", "postgres") }},
		{"empty prj", func() (string, error) { return DBNetworkName("acme", "", "pg") }},
		{"empty instance", func() (string, error) { return DBServiceName("acme", "prod", "", "postgres") }},
		{"empty service", func() (string, error) { return DBServiceName("acme", "prod", "pg-prod", "") }},
		{"slash injection", func() (string, error) { return DBServiceName("a/b", "prod", "pg", "postgres") }},
		{"colon injection", func() (string, error) { return DBNetworkName("acme", "prod", "pg:prod") }},
		{"space in key", func() (string, error) { return DBVolumeName("pg-prod", "da ta", "01JABCDEFGH") }},
		{"short instance id", func() (string, error) { return DBVolumeName("pg-prod", "data", "01JA") }},
		{"empty hash8", func() (string, error) { return DBSecretName("acme", "prod", "pg-prod", "password", "") }},
		{"uppercase hash8", func() (string, error) { return DBSecretName("acme", "prod", "pg-prod", "password", "ABCDEF12") }},
	} {
		if _, err := tc.fn(); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

// TestDbSecretNameRotationIsNewName 值轮换即换名换引用（库 secret 与 app
// secret 同纪律：hash8 参与命名，desired-hash 随引用变更触发重部署）。
func TestDbSecretNameRotationIsNewName(t *testing.T) {
	old := must(t, func() (string, error) { return DBSecretName("acme", "prod", "pg-prod", "password", Hash8("value-v1")) })
	rotated := must(t, func() (string, error) { return DBSecretName("acme", "prod", "pg-prod", "password", Hash8("value-v2")) })
	if old == rotated {
		t.Fatalf("rotated db secret name unchanged: %s", old)
	}
	if !IsDbServiceName(old) || !IsDbServiceName(rotated) {
		t.Fatalf("db secret names must carry the fleetly-db- prefix: %s / %s", old, rotated)
	}
}

// TestSameDbNameAcrossProjects D-W0-4 二修的核心性质：两个项目各有同名库
// 实例，服务/网络/secret 命名零冲突（team·prj 段承载）；库卷名族不含
// team/prj 段、id8 尾缀防撞（零卷迁移的前提）。
func TestSameDbNameAcrossProjects(t *testing.T) {
	a, err := DBServiceName("alpha", "prod", "pg", "postgres")
	if err != nil {
		t.Fatalf("db service name alpha/prod: %v", err)
	}
	b, err := DBServiceName("beta", "prod", "pg", "postgres")
	if err != nil {
		t.Fatalf("db service name beta/prod: %v", err)
	}
	if a == b {
		t.Fatalf("same-named db instances in different projects collide: %s", a)
	}
	va, _ := DBVolumeName("pg", "data", "01JAAAAAAAAA")
	vb, _ := DBVolumeName("pg", "data", "01JBBBBBBBBB")
	if va == vb {
		t.Fatalf("db volume names collide across instances: %s", va)
	}
	if strings.Contains(va, "alpha") {
		t.Fatalf("db volume formula must stay team/prj-free: %s", va)
	}
}
