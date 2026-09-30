package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// B2 回归（N0 修复批）：compose 声明面补齐——卷挂载、secret 引用、
// http/tcp 探针扩展键、探针节律、顶层 volumes 空声明。
func TestComposeDeclarativeSurface(t *testing.T) {
	doc := ComposeDoc{
		"volumes": map[string]any{"data": nil},
		"services": map[string]any{
			"web": map[string]any{
				"image":   "nginx:1.27",
				"volumes": []any{"data:/var/www"},
				"secrets": []any{"api-token", map[string]any{"source": "db-password"}},
				"healthcheck": map[string]any{
					"http_path": "/healthz",
					"interval":  "10s",
					"timeout":   "3s",
					"retries":   5,
				},
			},
			"db": map[string]any{
				"image":       "postgres:16",
				"healthcheck": map[string]any{"tcp_port": 5432, "start_period": "20s"},
			},
		},
	}
	spec, err := NormalizeCompose(doc, "app-1", "prj-1")
	require.NoError(t, err)

	var web, db *specv1.ProcessSpec
	for _, p := range spec.GetProcesses() {
		switch p.GetName() {
		case "web":
			web = p
		case "db":
			db = p
		}
	}
	require.NotNil(t, web)
	require.NotNil(t, db)

	// 卷挂载 → VolumeAttachment。
	require.Len(t, web.GetVolumes(), 1)
	assert.Equal(t, "data", web.GetVolumes()[0].GetVolumeId())
	assert.Equal(t, "/var/www", web.GetVolumes()[0].GetTarget())

	// secret 引用 → secret_refs（短语法与 {source:} 同型）。
	assert.Equal(t, []string{"api-token", "db-password"}, web.GetSecretRefs())

	// http 探针（fleetly 扩展键）+ 节律。
	hc := web.GetHealthcheck()
	require.NotNil(t, hc)
	assert.Equal(t, "/healthz", hc.GetHttpPath())
	assert.Equal(t, "10s", hc.GetInterval().AsDuration().String())
	assert.Equal(t, "3s", hc.GetTimeout().AsDuration().String())
	assert.Equal(t, int32(5), hc.GetRetries())

	// tcp 探针 + start_period。
	dbhc := db.GetHealthcheck()
	require.NotNil(t, dbhc)
	assert.Equal(t, int32(5432), dbhc.GetTcpPort())
	assert.Equal(t, "20s", dbhc.GetStartPeriod().AsDuration().String())
}

// 探针三选一 + 受管/未知键精确拒绝。
func TestComposeProbeRejections(t *testing.T) {
	cases := []struct {
		name string
		hc   map[string]any
		want string
	}{
		{"both probes", map[string]any{"http_path": "/h", "tcp_port": 80}, "one of test, http_path or tcp_port"},
		{"probe and test", map[string]any{"http_path": "/h", "test": []any{"CMD", "true"}}, "one of test, http_path or tcp_port"},
		{"disable managed", map[string]any{"tcp_port": 80, "disable": true}, "managed by the platform"},
		{"unknown key", map[string]any{"tcp_port": 80, "bad_key": 1}, "unknown healthcheck field"},
		{"no probe", map[string]any{"interval": "5s"}, "requires one of test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ComposeDoc{"services": map[string]any{"web": map[string]any{
				"image": "nginx", "healthcheck": tc.hc,
			}}}
			_, err := NormalizeCompose(doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 卷/secret 声明的非法形态精确拒绝。
func TestComposeVolumeSecretRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  ComposeDoc
		want string
	}{
		{"relative target", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{"data:var/www"},
		}}}, "absolute target"},
		{"long syntax", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "volumes": []any{map[string]any{"source": "data"}},
		}}}, "short syntax"},
		{"secret target remap", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "secrets": []any{map[string]any{"source": "s", "target": "renamed"}},
		}}}, "target remapping"},
		{"top-level driver opts", ComposeDoc{
			"volumes":  map[string]any{"data": map[string]any{"driver": "local"}},
			"services": map[string]any{"web": map[string]any{"image": "nginx"}},
		}, "driver options"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeCompose(tc.doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// 直投形态探针声明（B2 API 面）：http/tcp 各一 + 互斥在校验层（API 面测试）。
func TestImageDeployProbeDeclaration(t *testing.T) {
	s, err := ImageDeploy("a", "p", "nginx:1.27", "", &specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: "/healthz"}, Retries: 3,
	})
	require.NoError(t, err)
	require.Len(t, s.GetProcesses(), 1)
	assert.Equal(t, "/healthz", s.GetProcesses()[0].GetHealthcheck().GetHttpPath())

	s, err = ImageDeploy("a", "p", "nginx:1.27", "worker", &specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: 5432},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(5432), s.GetProcesses()[0].GetHealthcheck().GetTcpPort())
}
