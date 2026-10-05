package swarm

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func TestServiceNameFormula(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	assert.Equal(t, "fleetly-acme-shop-web-web", workloadServiceName(ns, capability.Workload{Process: "web"}))
}

// Task 域命名（ADR-0025 决策 4/7）：Run 载体按 run id 命名，域内天然唯一。
func TestRunServiceNameFormula(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01JTASK"}
	w := capability.Workload{ID: "01JRUN"}
	assert.Equal(t, "fleetly-run-01jrun", workloadServiceName(ns, w))
}

// Database 域命名（ADR-0029）：载体按 database 行 ID 命名；标记与选择器
// 携带 database 轴、不带空 labelApp（互斥主体）。
func TestDatabaseDomainNamingAndLabels(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", Database: "01JDB01"}
	w := capability.Workload{ID: "01JDB01", Process: "postgres"}
	assert.Equal(t, "fleetly-db-01jdb01", workloadServiceName(ns, w))
	labels := workloadLabels(ns, w, capability.Generation(3))
	assert.Equal(t, "01jdb01", labels[labelDatabase])
	assert.NotContains(t, labels, labelApp, "database domain must not carry an empty app label")
	selector := nsSelector(ns)
	for k, v := range selector {
		assert.Equal(t, v, labels[k], "selector key %s", k)
	}
}

func TestServiceNameTruncationStable(t *testing.T) {
	ns := capability.NamespaceRef{
		Team:    strings.Repeat("t", 30),
		Project: strings.Repeat("p", 30),
		App:     strings.Repeat("a", 20),
	}
	name := workloadServiceName(ns, capability.Workload{Process: "web"})
	assert.LessOrEqual(t, len(name), 63, "swarm DNS label limit")
	// 同输入稳定；不同 process 可区分（截断段 + 哈希后缀）。
	assert.Equal(t, name, workloadServiceName(ns, capability.Workload{Process: "web"}))
	assert.NotEqual(t, name, workloadServiceName(ns, capability.Workload{Process: "worker"}))
}

func TestSanitizeNamePart(t *testing.T) {
	assert.Equal(t, "shop-staging", sanitizeNamePart("Shop_Staging"))
	assert.Equal(t, "a-b", sanitizeNamePart("--a??b--"))
	assert.Equal(t, namePrefix, sanitizeNamePart("___"))
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

// 探针方言：全解析 IR（HTTPPort 恒显式——端口回退链策略在 engine 投影
// 单源，见 internal/engine resolvedHealthcheck；本测试只钉原语映射）。
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
			HTTPPort: 8080,
			Interval: 5e9, // 5s
		},
		Resources: &capability.Resources{CPUMillis: 500, MemoryMB: 256},
		Networks:  []string{"default"},
	}
	spec := toServiceSpec(ns, w, capability.Generation(2), map[string]secretCarrier{ //nolint:gosec // 载体名样本（非凭据值）
		"api-token": {id: "secid01", name: "fleetly-sec-api-token-ab12cd34"},
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

	// 探针方言：http → CMD-SHELL wget（busybox 兼容形态）打 IR 显式端口
	//（端口由 engine 解析，Provider 不推导）。
	require.NotNil(t, cs.Healthcheck)
	assert.Equal(t, "CMD-SHELL", cs.Healthcheck.Test[0])
	assert.Equal(t, "wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1", cs.Healthcheck.Test[1])

	// 网络按名引用。
	require.Len(t, spec.TaskTemplate.Networks, 1)
	// 平台网络名映射为载体名（Provider 私有公式；平台永不解析）。
	assert.Equal(t, "fleetly-net-shop-default", spec.TaskTemplate.Networks[0].Target)
	// Secret 文件注入：载体引用（id+名双发——swarmkit validateSecretRefsSpec
	// 要求）+ /run/secrets/<平台名>，值不进 env/label。
	require.Len(t, spec.TaskTemplate.ContainerSpec.Secrets, 1)
	assert.Equal(t, "fleetly-sec-api-token-ab12cd34", spec.TaskTemplate.ContainerSpec.Secrets[0].SecretName)
	assert.Equal(t, "secid01", spec.TaskTemplate.ContainerSpec.Secrets[0].SecretID, "raw API must carry the resolved secret id (docker CLI resolves client-side)")
	assert.Equal(t, "api-token", spec.TaskTemplate.ContainerSpec.Secrets[0].File.Name)
}

// deterministicFixture 是守卫 E 的负载样本：刻意覆盖全部 map 来源字段
// （env、≥2 secret 载体）与切片来源字段（≥2 networks、≥2 volumes、
// ≥2 ports），任何一处遍历序泄漏都会让逐字节对照翻红。
func deterministicFixture() (capability.NamespaceRef, capability.Workload, capability.Generation, map[string]secretCarrier) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	w := capability.Workload{
		ID:      "wl_01H",
		Process: "web",
		Image:   "ghcr.io/acme/web@sha256:abc",
		Command: []string{"/app/server"},
		Env:     map[string]string{"ZED": "26", "ALPHA": "1", "MID": "13"},
		Ports: []capability.WorkloadPort{
			{Port: 8080, Protocol: capability.ProtocolH2C},
			{Port: 5432, Protocol: capability.ProtocolTCP},
		},
		Replicas: 3,
		Healthcheck: &capability.Healthcheck{
			HTTPPath: "/healthz",
			HTTPPort: 8080,
			Interval: 5e9, // 5s
		},
		Resources: &capability.Resources{CPUMillis: 500, MemoryMB: 256},
		Volumes: []capability.VolumeMount{
			{VolumeID: "01VOLA", Target: "/data"},
			{VolumeID: "01VOLB", Target: "/cache", ReadOnly: true},
		},
		Networks: []string{"default", "internal"},
		// 生命周期加宽面（ADR-0025 决策 1/6）：StopGrace/Addressing 进
		// 确定性 fixture——别名翻译若引入遍历序泄漏在此翻红。
		StopGrace: 15e9, // 15s
		Addressing: []capability.Address{
			{Name: "task-01japp"},
			{Name: "run-01jrun"},
		},
	}
	// ≥2 个不同 secret 载体（P1-14 的触发面：map → ContainerSpec.Secrets）。
	carriers := map[string]secretCarrier{ //nolint:gosec // 载体名样本（非凭据值）
		"z-token":   {id: "secidzz", name: "fleetly-sec-z-token-99aa88bb"},
		"a-token":   {id: "secidaa", name: "fleetly-sec-a-token-11bb22cc"},
		"m-key.pem": {id: "secidmm", name: "fleetly-sec-m-key-pem-55dd66ee"},
	}
	return ns, w, capability.Generation(42), carriers
}

// TestToServiceSpecDeterministic 守卫 E（批 0）：同一输入连续调用
// toServiceSpec，json.Marshal 结果必须逐字节相等——swarm update 以 spec
// 变更为准，map 遍历序泄漏（env/secrets/将来任何 map 来源字段）会把幂等
// 重放变成假变更、触发无意义的滚动替换（P1-14 的泛化守卫）。
func TestToServiceSpecDeterministic(t *testing.T) {
	ns, w, gen, carriers := deterministicFixture()

	marshal := func() []byte {
		b, err := json.Marshal(toServiceSpec(ns, w, gen, carriers))
		require.NoError(t, err)
		return b
	}
	first, second := marshal(), marshal()
	assert.True(t, bytes.Equal(first, second),
		"toServiceSpec must be byte-for-byte deterministic: first=%s second=%s", first, second)

	// 多 secret 重放变体（P1-14 直接回归面）：同输入第三次及后续调用
	// 仍逐字节相等——map 内部序在多次遍历间漂移也不得渗进 spec。
	for i := 0; i < 20; i++ {
		again := marshal()
		assert.True(t, bytes.Equal(first, again),
			"replay #%d drifted: first=%s again=%s", i+3, first, again)
	}

	// 排序结果本身符合先例（platformName 升序），钉死排序键的选择。
	spec := toServiceSpec(ns, w, gen, carriers)
	names := make([]string, 0, len(spec.TaskTemplate.ContainerSpec.Secrets))
	for _, s := range spec.TaskTemplate.ContainerSpec.Secrets {
		names = append(names, s.File.Name)
	}
	assert.Equal(t, []string{"a-token", "m-key.pem", "z-token"}, names)
}

// 只读挂载透传（N0.1 P2-3）：compose 短语法 :ro → VolumeAttachment
// read_only → swarm mount.ReadOnly。
func TestVolumeReadOnlyMountTranslation(t *testing.T) {
	w := capability.Workload{ID: "wl_01H", Process: "web", Image: "nginx:1",
		Volumes: []capability.VolumeMount{{VolumeID: "01VOL", Target: "/data", ReadOnly: true}}}
	spec := toServiceSpec(capability.NamespaceRef{Team: "t", Project: "p", App: "a"}, w, capability.Generation(1), nil)
	require.Len(t, spec.TaskTemplate.ContainerSpec.Mounts, 1)
	assert.Equal(t, "/data", spec.TaskTemplate.ContainerSpec.Mounts[0].Target)
	assert.True(t, spec.TaskTemplate.ContainerSpec.Mounts[0].ReadOnly)
}

// TestRolloutOrderByVolumePresence（staging pgvector WAL 事故回归钉，
// 2026-10-03）：挂卷负载滚动必须 stop-first——start-first 的新任务与单副本
// 卷钉住互斥，swarm 超时硬杀旧任务即数据损坏；无卷负载维持 start-first。
// StopGrace → StopGracePeriod 的透传同批钉死（零值不设=编排器缺省 10s）。
func TestRolloutOrderByVolumePresence(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", Database: "01JDB01"}

	stateful := capability.Workload{
		ID: "01JDB01", Process: "postgres", Image: "postgres:17",
		StopGrace: 60e9, // 60s
		Volumes:   []capability.VolumeMount{{VolumeID: "pg", Target: "/var/lib/postgresql/data"}},
	}
	spec := toServiceSpec(ns, stateful, capability.Generation(1), nil)
	require.NotNil(t, spec.UpdateConfig)
	assert.Equal(t, swarm.UpdateOrderStopFirst, spec.UpdateConfig.Order,
		"volume-backed workloads must roll stop-first (start-first races the single-writer volume and gets the old task hard-killed)")
	require.NotNil(t, spec.TaskTemplate.ContainerSpec.StopGracePeriod)
	assert.Equal(t, 60*time.Second, *spec.TaskTemplate.ContainerSpec.StopGracePeriod)

	stateless := capability.Workload{ID: "wl_01H", Process: "web", Image: "nginx:1"}
	spec = toServiceSpec(ns, stateless, capability.Generation(1), nil)
	require.NotNil(t, spec.UpdateConfig)
	assert.Equal(t, swarm.UpdateOrderStartFirst, spec.UpdateConfig.Order)
	assert.Nil(t, spec.TaskTemplate.ContainerSpec.StopGracePeriod, "zero StopGrace must stay unset (engine default, not provider-invented)")
}

// TestRolloutOrderHostPublishForcesStopFirst（staging cadvisor 首启滚动
// 卡死回归钉，2026-10-04，runbook 记录·四/N2 评审 P2-5）：host 模式发布的
// 负载滚动必须 stop-first——宿主端口直绑在节点上排他，start-first 的新旧
// task 同节点共存必争位（旧 task 占 host 8080 + 新 task 绑不上 = 滚动卡死，
// 曾需手工 docker service rm 解锁）。判定不依赖 Volumes/Global 形态：host
// 发布本身承载排他性。
func TestRolloutOrderHostPublishForcesStopFirst(t *testing.T) {
	ns := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "metrics"}

	// cadvisor 同款形态：全局 + host 发布 8080 + 无卷 + 零宽限。
	globalCollector := capability.Workload{
		ID: "fleetly-metrics-cadvisor", Process: "cadvisor", Image: "gcr.io/cadvisor/cadvisor:v0.55.1",
		Global: true, Replicas: 1,
		Publish: []capability.PortPublish{{PublishedPort: 8080, TargetPort: 8080, Mode: capability.PublishModeHost}},
	}
	spec := toServiceSpec(ns, globalCollector, capability.Generation(1), nil)
	require.NotNil(t, spec.UpdateConfig)
	assert.Equal(t, swarm.UpdateOrderStopFirst, spec.UpdateConfig.Order,
		"host-published workloads must roll stop-first (start-first co-locates old+new tasks fighting for the same node port; global form guarantees the co-location)")

	// replicated 形态同样强制：排他性来自 host 直绑，不来自全局调度
	// （单节点集群上 replicated 单副本滚动同样同节点共存）。
	replicated := capability.Workload{
		ID: "wl_01H", Process: "agent", Image: "nginx:1", Replicas: 1,
		Publish: []capability.PortPublish{{PublishedPort: 9100, TargetPort: 9100, Mode: capability.PublishModeHost}},
	}
	spec = toServiceSpec(ns, replicated, capability.Generation(1), nil)
	require.NotNil(t, spec.UpdateConfig)
	assert.Equal(t, swarm.UpdateOrderStopFirst, spec.UpdateConfig.Order,
		"host publish alone must force stop-first regardless of global/replicated form")

	// mesh 发布（缺省模式）不受影响：无节点级端口争用面，维持 start-first。
	mesh := capability.Workload{
		ID: "fleetly-metrics-victoriametrics", Process: "victoriametrics", Image: "victoriametrics/victoria-metrics:v1.152.0",
		Replicas: 1,
		Publish:  []capability.PortPublish{{PublishedPort: 8428, TargetPort: 8428}},
	}
	spec = toServiceSpec(ns, mesh, capability.Generation(1), nil)
	require.NotNil(t, spec.UpdateConfig)
	assert.Equal(t, swarm.UpdateOrderStartFirst, spec.UpdateConfig.Order)
}

func TestPlacementConstraintsLabelFormula(t *testing.T) {
	// 约束走节点 label 公式（真机坑：不用 hostname/ID 直引用）。
	got := placementConstraints(capability.Placement{NodeIDs: []string{"node_01H"}})
	assert.Equal(t, []string{"node.labels.fleetly.node.id==node_01H"}, got)
	assert.Nil(t, placementConstraints(capability.Placement{}))
}

// B1 回归（N0 修复批）：跨域网络引用按引用自身的域解析载体名——受管
// Proxy（系统域）挂项目网，载体名绝不可用 workload 自己的域拼接。
func TestNetworkRefsResolveUnderTheirOwnNamespace(t *testing.T) {
	systemNS := capability.NamespaceRef{Team: "fleetly", Project: "system", App: "proxy"}
	w := capability.Workload{
		ID:      "fleetly-proxy-traefik",
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

// TestImageRegistryHost / TestEncodeRegistryAuth 已随单源收口迁至
// internal/capability/registryauth_test.go（2026-10-03 架构评审候选 5：
// engine/swarm/builders 三面共用规则的单点锚定）。

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

	encoded, err := capability.EncodeRegistryAuth(cred)
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
