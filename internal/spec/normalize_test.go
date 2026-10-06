package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func TestImageDeployNormalizes(t *testing.T) {
	spec, err := ImageDeploy("app-1", "prj-1", "nginx:1.27", "", nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int32(SchemaVersion), spec.GetSchemaVersion())
	assert.Equal(t, "app-1", spec.GetApp().GetId())
	assert.Equal(t, "nginx:1.27", spec.GetSource().GetImage().GetRef())
	require.Len(t, spec.GetProcesses(), 1)
	assert.Equal(t, "web", spec.GetProcesses()[0].GetName())
	assert.Equal(t, "nginx:1.27", spec.GetProcesses()[0].GetImage())
}

func TestNormalizeComposeSubset(t *testing.T) {
	doc := ComposeDoc{
		"services": map[string]any{
			"web": map[string]any{
				"image":       "nginx:1.27",
				"command":     "nginx -g daemon off",
				"environment": map[string]any{"MODE": "prod"},
				"ports":       []any{"8080", "8081/h2c", "5432/tcp"},
				"deploy":      map[string]any{"replicas": 2, "resources": map[string]any{"limits": map[string]any{"cpus": "1.5", "memory": "512m"}}},
				"networks":    []any{"default"},
			},
			"worker": map[string]any{
				"image":   "busybox:1.37",
				"command": "sh -c 'while true; do sleep 1; done'",
			},
		},
	}
	spec, err := NormalizeCompose(doc, "app-1", "prj-1")
	require.NoError(t, err)
	require.Len(t, spec.GetProcesses(), 2)

	// 服务来自 map，进程序不保证——按名取，不赌迭代顺序。
	var web *specv1.ProcessSpec
	for _, p := range spec.GetProcesses() {
		if p.GetName() == "web" {
			web = p
		}
	}
	require.NotNil(t, web, "process web must exist")
	assert.Equal(t, int64(2), web.GetReplicas())
	assert.Equal(t, int64(1500), web.GetResources().GetCpuMillis())
	assert.Equal(t, int64(512), web.GetResources().GetMemoryMb())
	require.Len(t, web.GetPorts(), 3)
	assert.Equal(t, specv1.Protocol_PROTOCOL_HTTP, web.GetPorts()[0].GetProtocol())
	assert.Equal(t, specv1.Protocol_PROTOCOL_H2C, web.GetPorts()[1].GetProtocol())
	assert.Equal(t, specv1.Protocol_PROTOCOL_TCP, web.GetPorts()[2].GetProtocol())
	assert.Equal(t, map[string]string{"MODE": "prod"}, web.GetEnv())
}

// 受管字段显式拒绝（精确理由）；未知字段提示白名单。
func TestNormalizeComposeRejections(t *testing.T) {
	cases := []struct {
		name string
		doc  ComposeDoc
		want string
	}{
		{"managed restart_policy", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "deploy": map[string]any{"replicas": 1, "restart_policy": map[string]any{"condition": "any"}},
		}}}, "managed by the platform"},
		{"unknown service field", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"image": "nginx", "privileged": true,
		}}}, "unsupported field"},
		{"unknown top-level", ComposeDoc{"version": "3", "services": map[string]any{
			"web": map[string]any{"image": "nginx"},
		}}, "unsupported compose field"},
		{"no image", ComposeDoc{"services": map[string]any{"web": map[string]any{
			"build": ".",
		}}}, "unsupported field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeCompose(tc.doc, "a", "p")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// compose 扩展键 deploy.strategy（ADR-0048 决策 1）：blue-green 人类词形
// → 枚举；缺省不落字段（零值 = rolling——存量冻结体零漂移）；值域外
// 拒绝且指名 deploy.strategy 字段与合法值。
func TestNormalizeComposeDeployStrategy(t *testing.T) {
	docFor := func(strategy any) ComposeDoc {
		svc := map[string]any{"image": "nginx:1.27"}
		if strategy != nil {
			svc["deploy"] = map[string]any{"strategy": strategy}
		}
		return ComposeDoc{"services": map[string]any{"web": svc}}
	}

	s, err := NormalizeCompose(docFor("blue-green"), "app-1", "prj-1")
	require.NoError(t, err)
	require.Len(t, s.GetProcesses(), 1)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN, s.GetProcesses()[0].GetStrategy())

	s, err = NormalizeCompose(docFor("rolling"), "app-1", "prj-1")
	require.NoError(t, err)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_ROLLING, s.GetProcesses()[0].GetStrategy())

	// 缺省 = 零值（UNSPECIFIED 归一 rolling）：无策略的 compose 冻结体
	// 逐字节与存量形态一致——零漂移锚。
	s, err = NormalizeCompose(docFor(nil), "app-1", "prj-1")
	require.NoError(t, err)
	assert.Equal(t, specv1.DeployStrategy_DEPLOY_STRATEGY_UNSPECIFIED, s.GetProcesses()[0].GetStrategy())

	_, err = NormalizeCompose(docFor("immediate"), "app-1", "prj-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy.strategy")
	assert.Contains(t, err.Error(), `strategy must be rolling or blue-green (got "immediate")`)
}

// compose 端口声明的 Route-facing 两半边（F3.5 裁决 + 2026-10-06 staging
// 走查修复）：声明 ports 而 networks 未列 default 时补挂项目 default 网
// （Proxy 可达性半边——image/upload 形态的 portDeclNetworks 同语义）；
// 显式 networks 原样保留（default 已在则不重复）；未声明端口零挂网
// （ADR-0034 无网络即无 DNS 面诚实语义不变）。
func TestNormalizeComposePortsAttachDefaultNetwork(t *testing.T) {
	doc := func(svc map[string]any) ComposeDoc {
		base := map[string]any{"image": "traefik/whoami:v1.10"}
		for k, v := range svc {
			base[k] = v
		}
		return ComposeDoc{"services": map[string]any{"web": base}}
	}

	s, err := NormalizeCompose(doc(map[string]any{"ports": []any{"80"}}), "app-1", "prj-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, s.GetProcesses()[0].GetNetworks(), "a port declaration implies the project default network (proxy reachability half)")

	s, err = NormalizeCompose(doc(map[string]any{"ports": []any{"80"}, "networks": []any{"default", "internal"}}), "app-1", "prj-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"default", "internal"}, s.GetProcesses()[0].GetNetworks(), "an explicit network list is preserved verbatim (no duplicate default)")

	s, err = NormalizeCompose(doc(nil), "app-1", "prj-1")
	require.NoError(t, err)
	assert.Empty(t, s.GetProcesses()[0].GetNetworks(), "without a declared port the no-network honesty semantics stay unchanged")
}
