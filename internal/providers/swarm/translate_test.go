package swarm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func base64DecodeString(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}

func TestServiceNameFormula(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	assert.Equal(t, "fleetly-acme-shop-web-web", serviceName(ns, "web"))
}

func TestServiceNameTruncationStable(t *testing.T) {
	ns := capability.NamespaceRef{
		Team:    strings.Repeat("t", 30),
		Project: strings.Repeat("p", 30),
		App:     strings.Repeat("a", 20),
	}
	name := serviceName(ns, "web")
	assert.LessOrEqual(t, len(name), 63, "swarm DNS label limit")
	// 同输入稳定；不同 process 可区分（截断段 + 哈希后缀）。
	assert.Equal(t, name, serviceName(ns, "web"))
	assert.NotEqual(t, name, serviceName(ns, "worker"))
}

func TestSanitizeNamePart(t *testing.T) {
	assert.Equal(t, "shop-staging", sanitizeNamePart("Shop_Staging"))
	assert.Equal(t, "a-b", sanitizeNamePart("--a??b--"))
	assert.Equal(t, namePrefix, sanitizeNamePart("___"))
}

func TestPortsLabelRoundTrip(t *testing.T) {
	ports := []capability.WorkloadPort{
		{Port: 5432, Protocol: capability.ProtocolTCP},
		{Port: 8080, Protocol: capability.ProtocolHTTP},
		{Port: 9090, Protocol: capability.ProtocolH2C},
	}
	back := parsePortsLabel(portsLabelValue(ports))
	assert.ElementsMatch(t, ports, back)
}

func TestWorkloadLabels(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	w := capability.Workload{ID: "wl_01H", Process: "web", Image: "nginx:1"}
	labels := workloadLabels(ns, w, capability.Generation(7))
	assert.Equal(t, "true", labels[labelManaged])
	assert.Equal(t, "wl_01H", labels[labelWorkload])
	assert.Equal(t, "7", labels[labelGeneration])

	// ns 选择器与标记集一致（列表过滤即命中）。
	selector := nsSelector(ns)
	for k, v := range selector {
		assert.Equal(t, v, labels[k], "selector key %s", k)
	}
}

func TestToServiceSpec(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	w := capability.Workload{
		ID:       "wl_01H",
		Process:  "web",
		Image:    "ghcr.io/acme/web@sha256:abc",
		Command:  []string{"/app/server"},
		Env:      map[string]string{"B": "2", "A": "1"},
		Ports:    []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolH2C}},
		Replicas: 3,
		Healthcheck: &capability.Healthcheck{
			HTTPPath: "/healthz",
			Interval: 5e9, // 5s
		},
		Resources: &capability.Resources{CPUMillis: 500, MemoryMB: 256},
		Networks:  []string{"default"},
	}
	spec := toServiceSpec(ns, w, capability.Generation(2), map[string]string{ //nolint:gosec // 载体名样本（非凭据值）
		"api-token": "fleetly-sec-api-token-ab12cd34",
	})

	assert.Equal(t, "fleetly-acme-shop-web-web", spec.Name)
	assert.Equal(t, uint64(3), *spec.Mode.Replicated.Replicas)

	cs := spec.TaskTemplate.ContainerSpec
	assert.Equal(t, w.Image, cs.Image)
	// env 排序稳定（幂等 diff）。
	assert.Equal(t, []string{"A=1", "B=2"}, cs.Env)

	// 资源上限映射：500 毫核 → 0.5 CPU。
	assert.Equal(t, int64(500_000_000), spec.TaskTemplate.Resources.Limits.NanoCPUs)
	assert.Equal(t, int64(256*1024*1024), spec.TaskTemplate.Resources.Limits.MemoryBytes)

	// 探针方言：http → CMD-SHELL wget。
	require.NotNil(t, cs.Healthcheck)
	assert.Equal(t, "CMD-SHELL", cs.Healthcheck.Test[0])
	assert.Contains(t, cs.Healthcheck.Test[1], "/healthz")

	// 网络按名引用。
	require.Len(t, spec.TaskTemplate.Networks, 1)
	// 平台网络名映射为载体名（Provider 私有公式；平台永不解析）。
	assert.Equal(t, "fleetly-net-shop-default", spec.TaskTemplate.Networks[0].Target)
	// Secret 文件注入：载体引用 + /run/secrets/<平台名>，值不进 env/label。
	require.Len(t, spec.TaskTemplate.ContainerSpec.Secrets, 1)
	assert.Equal(t, "fleetly-sec-api-token-ab12cd34", spec.TaskTemplate.ContainerSpec.Secrets[0].SecretName)
	assert.Equal(t, "api-token", spec.TaskTemplate.ContainerSpec.Secrets[0].File.Name)
}

func TestPlacementConstraintsLabelFormula(t *testing.T) {
	// 约束走节点 label 公式（真机坑：不用 hostname/ID 直引用）。
	got := placementConstraints(capability.Placement{NodeIDs: []string{"node_01H"}})
	assert.Equal(t, []string{"node.labels.fleetly.node.id==node_01H"}, got)
	assert.Nil(t, placementConstraints(capability.Placement{}))
}

// B1 回归（N0 修复批）：跨域网络引用按引用自身的域解析载体名——受管
// Edge（系统域）挂项目网，载体名绝不可用 workload 自己的域拼接。
func TestNetworkRefsResolveUnderTheirOwnNamespace(t *testing.T) {
	systemNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "edge"}
	w := capability.Workload{
		ID:      "fleetly-edge-traefik",
		Process: "traefik",
		Image:   "traefik:v3.5.4",
		NetworkRefs: []capability.NetworkRef{
			{Namespace: capability.NamespaceRef{Team: "default", Project: "01JD0PROJ00000000000000000"}, Name: "default"},
			{Namespace: capability.NamespaceRef{Team: "default", Project: "01JD0PROJ00000000000000000"}, Name: "internal"},
		},
	}
	spec := toServiceSpec(systemNS, w, capability.Generation(1), nil)
	require.Len(t, spec.TaskTemplate.Networks, 2)
	// sanitizeNamePart 小写化（swarm 名词表约束）。
	assert.Equal(t, "fleetly-net-01jd0proj00000000000000000-default", spec.TaskTemplate.Networks[0].Target,
		"carrier name must resolve under the referenced project, not the workload's own namespace")
	assert.Equal(t, "fleetly-net-01jd0proj00000000000000000-internal", spec.TaskTemplate.Networks[1].Target)
}

func TestImageRegistryHost(t *testing.T) {
	assert.Equal(t, "ghcr.io", imageRegistryHost("ghcr.io/acme/web:1"))
	// host:port 形态整段为键（凭证表的地址形态）。
	assert.Equal(t, "registry.example.com:5000", imageRegistryHost("registry.example.com:5000/team/app@sha256:x"))
	assert.Equal(t, "localhost:5000", imageRegistryHost("localhost:5000/app"))
	assert.Equal(t, "docker.io", imageRegistryHost("nginx:1.27"))
	assert.Equal(t, "docker.io", imageRegistryHost("library/nginx:1.27"))
}

func TestEncodeRegistryAuth(t *testing.T) {
	enc, err := encodeRegistryAuth(capability.RegistryCredential{
		Server: "ghcr.io", Username: "u", Secret: "s",
	})
	require.NoError(t, err)
	assert.NotContains(t, enc, "ghcr.io") // base64 不明文
	decoded, err := base64DecodeString(enc)
	require.NoError(t, err)
	assert.Contains(t, decoded, `"username":"u"`)
	assert.Contains(t, decoded, `"serveraddress":"ghcr.io"`)
}

// TestRegistryAuthNeverEntersCarrierSpec 守卫 ADR-0014 / F0.18：私有镜像
// 凭证只经 EncodedRegistryAuth 通道随 ServiceCreate/Update 下发（swarmkit
// 加密分发到节点），绝不进载体 label / 明文 env / spec 任何角落（旧 DT-2
// 真机 404 教训）。双侧断言：spec 全量序列化无凭证踪迹 + 专用通道编码
// 健全（否则守卫退化为“凭证根本没分发”）。
func TestRegistryAuthNeverEntersCarrierSpec(t *testing.T) {
	ns := capability.NamespaceRef{Team: "default", Project: "shop", App: "web"}
	w := capability.Workload{
		ID: "wl_01H", Process: "web",
		Image:    "registry.example.com:5000/acme/web:1",
		Env:      map[string]string{"MODE": "prod"},
		Replicas: 1,
	}
	cred := capability.RegistryCredential{
		Server: "registry.example.com:5000", Username: "pull-bot-7f3d", Secret: "wombat-quarrel-4f7d",
	}
	materials := capability.Materials{RegistryAuth: map[string]capability.RegistryCredential{
		"registry.example.com:5000": cred,
	}}

	spec := toServiceSpec(ns, w, capability.Generation(3), nil)
	blob, err := json.Marshal(spec)
	require.NoError(t, err)

	encoded, err := encodeRegistryAuth(cred)
	require.NoError(t, err)

	// serveraddress 是镜像引用的公开部分（本就在 image 里），不作 needle；
	// 用户名/口令/base64 编码体三件必须零踪迹。
	for _, needle := range []string{cred.Username, cred.Secret, encoded} {
		assert.NotContains(t, string(blob), needle,
			"registry credential material must never appear anywhere in the carrier spec")
	}
	// 声明 env 原样透传、无“顺手”注入（泄漏的历史形态）。
	assert.Equal(t, []string{"MODE=prod"}, spec.TaskTemplate.ContainerSpec.Env)

	// 专用通道健全：按镜像 host 命中凭证 → base64 JSON；未命中 → 匿名。
	p := &Provider{}
	got, err := p.registryAuthFor(context.Background(), w.Image, materials)
	require.NoError(t, err)
	assert.Equal(t, encoded, got)
	anon, err := p.registryAuthFor(context.Background(), "nginx:1.27", materials)
	require.NoError(t, err)
	assert.Empty(t, anon, "no matching host must mean anonymous pull (empty auth header)")
}
