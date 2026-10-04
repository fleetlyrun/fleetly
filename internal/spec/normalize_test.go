package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func TestImageDeployNormalizes(t *testing.T) {
	spec, err := ImageDeploy("app-1", "prj-1", "nginx:1.27", "", nil, nil)
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
