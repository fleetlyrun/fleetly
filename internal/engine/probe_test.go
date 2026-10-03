package engine

// 探针全解析 IR 的策略单源测试（架构评审第二轮候选 7）：http 端口回退
// 链与 exec 方言归一在投影期一次解析——Provider 只做原语映射（原策略
// 测试随实现从 swarm translate 迁来，链值逐字一致）。

import (
	"testing"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// http 探针端口回落序（与原 swarm Provider 内实现逐值一致——策略回归
// 引擎，值零漂移）：tcp_port 优先（spec oneof 下与 http_path 互斥，此档
// 防直接构造 IR 的输入）> 进程声明首端口 > 8080（无任何声明的诚实缺省）。
func TestProbeHTTPPortFallbackOrder(t *testing.T) {
	ports := []capability.WorkloadPort{{Port: 3000, Protocol: capability.ProtocolHTTP}}
	assert.Equal(t, int32(9090), probeHTTPPort(9090, ports), "explicit tcp_port wins")
	assert.Equal(t, int32(3000), probeHTTPPort(0, ports), "declared first port fallback")
	assert.Equal(t, int32(8080), probeHTTPPort(0, nil), "no declaration -> 8080")
}

// spec → IR：http 探针带声明端口解析；tcp/exec 探针不解析 HTTPPort。
func TestResolvedHealthcheckBySpecForm(t *testing.T) {
	ports := []capability.WorkloadPort{{Port: 3000, Protocol: capability.ProtocolHTTP}}

	http := resolvedHealthcheck(&specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: "/healthz"},
	}, ports)
	require.NotNil(t, http)
	assert.Equal(t, "/healthz", http.HTTPPath)
	assert.Equal(t, int32(3000), http.HTTPPort)

	declared := resolvedHealthcheck(&specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_HttpPath{HttpPath: "/healthz"},
	}, nil)
	assert.Equal(t, int32(8080), declared.HTTPPort, "no port declaration -> honest 8080 default")

	tcp := resolvedHealthcheck(&specv1.HealthcheckSpec{
		Probe: &specv1.HealthcheckSpec_TcpPort{TcpPort: 5432},
	}, ports)
	assert.Equal(t, int32(5432), tcp.TCPPort)
	assert.Equal(t, int32(0), tcp.HTTPPort, "non-http probes resolve no HTTP port")
}

// exec 方言归一：proto 面可能直写的 docker 方言前缀在投影期展开为干净
// argv（CMD-SHELL 载荷经 sh -c 整体承载——spec 归一层 compose 面同款
// 语义）；无前缀直通。
func TestNormalizeProbeExecDialects(t *testing.T) {
	assert.Equal(t, []string{"curl", "-f", "localhost"},
		normalizeProbeExec([]string{"CMD", "curl", "-f", "localhost"}))
	assert.Equal(t, []string{"sh", "-c", "wget -q localhost || exit 1"},
		normalizeProbeExec([]string{"CMD-SHELL", "wget -q localhost || exit 1"}))
	assert.Equal(t, []string{"pg_isready", "-h", "127.0.0.1"},
		normalizeProbeExec([]string{"pg_isready", "-h", "127.0.0.1"}))
	assert.Nil(t, normalizeProbeExec(nil))
}
