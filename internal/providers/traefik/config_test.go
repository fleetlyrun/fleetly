package traefik

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 动态配置翻译 golden（h2c/http/tcp + TLS 标注；F0.15 验收：h2c 后端
// 路由可通——messageloop 形态）。
func TestDynamicConfigGolden(t *testing.T) {
	p, err := New("http://fleetlyd:9082/edge/config", "ops@example.com")
	require.NoError(t, err)
	require.NoError(t, p.PublishRoutes(context.Background(), []capability.Route{
		{Host: "shop.127.0.0.1.sslip.io", Path: "/", Process: "web", Port: 8080,
			Protocol: capability.ProtocolHTTP, TLS: "auto", BackendAddr: "10.0.0.2:8080"},
		{Host: "api.127.0.0.1.sslip.io", Path: "/v1", Process: "gateway", Port: 8081,
			Protocol: capability.ProtocolH2C, TLS: "auto", BackendAddr: "10.0.0.3:8081"},
		{Host: "db.127.0.0.1.sslip.io", Process: "pg", Port: 5432,
			Protocol: capability.ProtocolTCP, TLS: "none", BackendAddr: "10.0.0.4:5432"},
		{Host: "plain.localhost", Process: "web", Port: 3000,
			Protocol: capability.ProtocolHTTP, TLS: "none", BackendAddr: "10.0.0.5:3000"},
	}))
	got := string(p.ConfigSnapshot())

	golden := filepath.Join("testdata", "dynamic-config.json")
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750))
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/providers/traefik -update")
	assert.Equal(t, string(want), got)
}

// 未解析后端的 Route 拒绝发布（控制面强制全量：宁拒发不发半截配置）。
func TestUnresolvedBackendRejected(t *testing.T) {
	_, err := buildDynamicConfig([]capability.Route{{Host: "x.sslip.io", Process: "web", Port: 80}})
	assert.ErrorContains(t, err, "no resolved backend address")
}

// 受管形态声明完整性（80/443 发布 + ACME storage 卷）。
func TestManagedWorkloadDeclaration(t *testing.T) {
	p, err := New("http://fleetlyd:9082/edge/config", "")
	require.NoError(t, err)
	ws := p.ManagedWorkloads()
	require.Len(t, ws, 1)
	w := ws[0]
	assert.Equal(t, Image, w.Image)
	assert.Equal(t, []capability.PortPublish{
		{PublishedPort: 80, TargetPort: 80},
		{PublishedPort: 443, TargetPort: 443},
	}, w.Publish)
	assert.Equal(t, capability.NamespaceRef{Team: "fleetly", Project: "system", App: "edge"}, p.ManagedNamespace())
	assert.Contains(t, joinCommand(w.Command), "--certificatesresolvers.le.acme.httpchallenge.entrypoint=web")
}

func joinCommand(cmd []string) string {
	out := ""
	for _, c := range cmd {
		out += c + " "
	}
	return out
}
