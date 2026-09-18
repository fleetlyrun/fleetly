package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestNamesMatchDesignDocs 验收 5：命名函数输出与设计文档字符串逐一相等
// （表驱动，文档原文钉死——state-model §2.4 对象命名 + architecture §2.4
// 服务命名与网络行）。任何一侧改动都必须先改文档再改此处。
func TestNamesMatchDesignDocs(t *testing.T) {
	const hashInput = "super-secret-value"
	sum := sha256.Sum256([]byte(hashInput))
	wantHash8 := hex.EncodeToString(sum[:])[:8]

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			// architecture §2.4：Swarm 服务名 `fleetly-<app>-<service>`。
			name: "service name",
			got:  must(t, func() (string, error) { return ServiceName("my-api", "web") }),
			want: "fleetly-my-api-web",
		},
		{
			// architecture §2.4：secret 名 `fleetly-<app>-<name>-<hash8>`。
			name: "secret name",
			got:  must(t, func() (string, error) { return SecretName("my-api", "database_url", wantHash8) }),
			want: "fleetly-my-api-database_url-" + wantHash8,
		},
		{
			// state-model §2.4：卷无 label，命名约定 `fleetly-<app>-<key>-<appid8>`。
			name: "volume name",
			got:  must(t, func() (string, error) { return VolumeName("my-api", "data", "01JABCDEFGH") }),
			want: "fleetly-my-api-data-01JABCDE",
		},
		{
			// 每 app 专属 overlay（保守补全名，见 NetworkName 注释）。
			name: "network name",
			got:  must(t, func() (string, error) { return NetworkName("my-api") }),
			want: "fleetly-my-api-net",
		},
		{
			// 服务别名 = compose 服务名（app 内短名互访）。
			name: "network alias",
			got:  must(t, func() (string, error) { return NetworkAlias("web") }),
			want: "web",
		},
		{
			name: "hash8 = sha256(content)[:8]",
			got:  Hash8(hashInput),
			want: wantHash8,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q（设计文档原文）", tc.got, tc.want)
			}
		})
	}
}

// TestHash8RotationIsNewName 验收 2 的命名侧：值轮换即换名换引用（架构
// §2.4 密钥行——引用参与 desired-hash，轮换天然触发重部署）。
func TestHash8RotationIsNewName(t *testing.T) {
	old := must(t, func() (string, error) { return SecretName("app", "k", Hash8("value-v1")) })
	rotated := must(t, func() (string, error) { return SecretName("app", "k", Hash8("value-v2")) })
	if old == rotated {
		t.Fatalf("rotated secret name unchanged: %s", old)
	}
	if !strings.HasPrefix(old, "fleetly-app-k-") || !strings.HasPrefix(rotated, "fleetly-app-k-") {
		t.Fatalf("name prefix broken: %s / %s", old, rotated)
	}
}

// TestServiceLabelsMinimalSet 验收 1 的 label 构造器：最小集四键
// （state-model §2.4 Service 行），值逐一对照。
func TestServiceLabelsMinimalSet(t *testing.T) {
	labels, err := ServiceLabels("my-api", "web", "dep_01")
	if err != nil {
		t.Fatalf("ServiceLabels: %v", err)
	}
	want := map[string]string{
		state.LabelManaged:    state.ManagedLabelValue,
		state.LabelApp:        "my-api",
		state.LabelProcess:    "web",
		state.LabelDeployment: "dep_01",
	}
	if len(labels) != len(want) {
		t.Fatalf("label count = %d, want %d (最小集)", len(labels), len(want))
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}

	// 稳定序列化：SortedServiceLabels 键字典序。
	sorted := SortedServiceLabels(labels)
	if len(sorted) != 4 || sorted[0][0] > sorted[1][0] || sorted[1][0] > sorted[2][0] || sorted[2][0] > sorted[3][0] {
		t.Fatalf("sorted labels not ordered: %v", sorted)
	}

	// 容器 label 仅 fleetly.app。
	cl, err := ContainerLabels("my-api")
	if err != nil || len(cl) != 1 || cl[state.LabelApp] != "my-api" {
		t.Fatalf("container labels = %v err=%v", cl, err)
	}
}

// TestNameValidation 非法成分拒绝（空串、越界字符、分隔层级注入）。
func TestNameValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() (string, error)
	}{
		{"empty app", func() (string, error) { return ServiceName("", "web") }},
		{"empty service", func() (string, error) { return ServiceName("app", "") }},
		{"slash injection", func() (string, error) { return ServiceName("a/b", "web") }},
		{"colon injection", func() (string, error) { return ServiceName("app", "we:b") }},
		{"space", func() (string, error) { return VolumeName("app", "da ta", "01JABCDEFGH") }},
		{"short appid", func() (string, error) { return VolumeName("app", "data", "01JA") }},
		{"empty hash8", func() (string, error) { return SecretName("app", "k", "") }},
		{"uppercase hash8", func() (string, error) { return SecretName("app", "k", "ABCDEF12") }},
		{"empty deployment", func() (string, error) { _, err := ServiceLabels("app", "web", ""); return "", err }},
	} {
		if _, err := tc.fn(); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

// must 是表驱动夹具的构造 helper（失败即测试失败）。
func must(t *testing.T, fn func() (string, error)) string {
	t.Helper()
	got, err := fn()
	if err != nil {
		t.Fatalf("construct name: %v", err)
	}
	return got
}
