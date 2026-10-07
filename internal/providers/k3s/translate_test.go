package k3s

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 命名公式：四域载体名 + GenerationScoped 代次后缀 + 超长截断（哈希兜底）。
func TestWorkloadName(t *testing.T) {
	appNS := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	assert.Equal(t, "fleetly-web-api", workloadName(appNS, capability.Workload{ID: "w1", Process: "api"}, 7))
	scoped := capability.Workload{ID: "w1", Process: "api", GenerationScoped: true, Generation: 3}
	assert.Equal(t, "fleetly-web-api-g3", workloadName(appNS, scoped, 7))
	taskNS := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01T"}
	assert.Equal(t, "fleetly-run-01r", workloadName(taskNS, capability.Workload{ID: "01R"}, 1))
	dbNS := capability.NamespaceRef{Team: "acme", Project: "shop", Database: "01D"}
	assert.Equal(t, "fleetly-db-01d", workloadName(dbNS, capability.Workload{ID: "01D"}, 1))
	browseNS := capability.NamespaceRef{Team: "acme", Project: "shop", Browse: "01B"}
	assert.Equal(t, "fleetly-browse-01b", workloadName(browseNS, capability.Workload{ID: "01B"}, 1))
	// 特殊字符净化 + 超长截断（63 上限，截断保前缀 + 8 hex 后缀）。
	long := capability.Workload{ID: "w", Process: strings.Repeat("p", 80)}
	name := workloadName(appNS, long, 1)
	require.LessOrEqual(t, len(name), dnsLabelLimit)
	assert.True(t, strings.HasPrefix(name, "fleetly-web-"))
}

func TestNamespaceName(t *testing.T) {
	assert.Equal(t, "fleetly-shop", namespaceName(capability.NamespaceRef{Project: "shop"}))
	assert.Equal(t, "fleetly-shop-corp", namespaceName(capability.NamespaceRef{Team: "acme", Project: "Shop_Corp"}))
	assert.Equal(t, systemNamespace, namespaceName(capability.NamespaceRef{}))
}

// 归属标记：域主体互斥 + egress/addressing 标记合入 + generation 覆写。
func TestWorkloadLabels(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	w := capability.Workload{
		ID: "w1", Process: "api", Generation: 9,
		EgressNetworks: []string{"isolated"},
		Addressing:     []capability.Address{{Name: "api.web"}},
	}
	lb := workloadLabels(ns, w, 7)
	assert.Equal(t, "9", lb[labelGeneration]) // 逐载体覆写优先
	assert.Equal(t, "web", lb[labelApp])
	assert.Empty(t, lb[labelTask])
	assert.Equal(t, "true", lb[labelEgress])
	assert.Equal(t, "true", lb[addressingLabelKey("api.web")])

	// 无 egress 网络的载体不带隔离标记（per-carrier 粒度锚）。
	clean := capability.Workload{ID: "w2", Process: "worker"}
	lb2 := workloadLabels(ns, clean, 7)
	assert.NotContains(t, lb2, labelEgress)
}

// Deployment 全字段映射：探针/资源/卷/材料/nodeSelector/Recreate 争用面。
func TestToDeployment(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	w := capability.Workload{
		ID: "w1", Process: "api", Image: "nginx:1.27",
		Command:  []string{"/bin/app", "--flag"},
		Env:      map[string]string{"B": "2", "A": "1"},
		Ports:    []capability.WorkloadPort{{Port: 8080, Protocol: capability.ProtocolHTTP}},
		Replicas: 2,
		Healthcheck: &capability.Healthcheck{
			HTTPPath: "/healthz", HTTPPort: 8080,
			Interval: 5 * time.Second, Retries: 3,
		},
		Resources: &capability.Resources{CPUMillis: 500, MemoryMB: 256},
		Placement: capability.Placement{NodeIDs: []string{"01NODE"}},
		Volumes:   []capability.VolumeMount{{VolumeID: "01V", Target: "/data"}},
		StopGrace: 30 * time.Second,
	}
	d := toDeployment(ns, w, 7, map[string]string{"db-pass": "fleetly-mat-db-pass"}, nil) //nolint:gosec // 测试载荷：材料名→对象名映射断言，非凭证本体
	assert.Equal(t, "fleetly-web-api", d.Name)
	require.Len(t, d.Spec.Template.Spec.Containers, 1)
	c := d.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "nginx:1.27", c.Image)
	assert.Equal(t, []string{"/bin/app", "--flag"}, c.Command)
	// env 排序稳定（幂等 diff）。
	require.Len(t, c.Env, 2)
	assert.Equal(t, "A", c.Env[0].Name)
	// readiness 探针（httpGet 原语映射；不配 liveness——平台语义自治）。
	require.NotNil(t, c.ReadinessProbe)
	require.NotNil(t, c.ReadinessProbe.HTTPGet)
	assert.Equal(t, "/healthz", c.ReadinessProbe.HTTPGet.Path)
	assert.Nil(t, c.LivenessProbe)
	// 资源上限。
	require.NotNil(t, c.Resources.Limits)
	assert.Equal(t, "500m", c.Resources.Limits.Cpu().String())
	assert.Equal(t, "256Mi", c.Resources.Limits.Memory().String())
	// 卷挂载 + PVC 引用（名公式）+ 材料 projected 卷（PVC 卷在前、
	// projected 卷在后——sortedKeys 稳定序）。
	require.Len(t, d.Spec.Template.Spec.Volumes, 2)
	assert.Equal(t, pvcName("01V"), d.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
	pv := d.Spec.Template.Spec.Volumes[1].Projected
	require.NotNil(t, pv, "materials must land as a single projected volume")
	require.Len(t, pv.Sources, 1)
	assert.Equal(t, "fleetly-mat-db-pass", pv.Sources[0].Secret.Name)
	require.Len(t, pv.Sources[0].Secret.Items, 1)
	assert.Equal(t, "value", pv.Sources[0].Secret.Items[0].Key)
	assert.Equal(t, "db-pass", pv.Sources[0].Secret.Items[0].Path, "value key projects as the platform-named file (docker secrets parity)")
	// 材料挂载点 /run/secrets（单挂载点；文件 <名> 由 items 投影）。
	found := false
	for _, m := range c.VolumeMounts {
		if m.MountPath == "/run/secrets" {
			found = true
		}
	}
	assert.True(t, found, "secret materials must mount at /run/secrets")
	// 钉住节点 selector（平台节点 ID 锚）。
	assert.Equal(t, "01NODE", d.Spec.Template.Spec.NodeSelector[labelNodeID])
	// 挂卷负载 = Recreate（单实例争用面）。
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, d.Spec.Strategy.Type)
	require.NotNil(t, d.Spec.Replicas)
	assert.EqualValues(t, 2, *d.Spec.Replicas)
}

// 无争用负载不显式设置策略（k8s 服务端缺省物化 RollingUpdate）；hostPort
// 发布同样 Recreate。
func TestDeploymentStrategy(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	plain := toDeployment(ns, capability.Workload{ID: "w1", Process: "api"}, 1, nil, nil)
	assert.Empty(t, plain.Spec.Strategy.Type, "no-strategy carriers defer to k8s default (RollingUpdate)")
	hostPub := toDeployment(ns, capability.Workload{
		ID: "w2", Process: "edge",
		Publish: []capability.PortPublish{{PublishedPort: 80, TargetPort: 8080, Mode: capability.PublishModeHost}},
	}, 1, nil, nil)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, hostPub.Spec.Strategy.Type)
}

// Global → DaemonSet；one-shot RestartNever → 独立 Pod（Never）。
func TestCarrierTypes(t *testing.T) {
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	ds := toDaemonSet(ns, capability.Workload{ID: "w1", Process: "collect", Global: true}, 1, nil, nil)
	assert.Equal(t, "fleetly-web-collect", ds.Name)
	require.Len(t, ds.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, corev1.RestartPolicyAlways, ds.Spec.Template.Spec.RestartPolicy)
	pod := toOneShotPod(capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01T"}, capability.Workload{
		ID: "01R", Process: "run", Restart: capability.RestartNever,
	}, 1, nil, nil)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	assert.Equal(t, "true", pod.Labels[labelManaged])
	assert.Equal(t, "fleetly-run-01r", pod.Name)
}

// Addressing → Service：selector = addressing label；端口 = 声明端口。
func TestToService(t *testing.T) {
	w := capability.Workload{
		ID: "w1", Process: "api",
		Ports: []capability.WorkloadPort{{Port: 8080}},
	}
	svc := toService(capability.NamespaceRef{Team: "acme", Project: "shop"}, "api.web", addressingLabelKey("api.web"), w)
	assert.Equal(t, "api-web", svc.Name)
	assert.Equal(t, "true", svc.Spec.Selector[addressingLabelKey("api.web")])
	require.Len(t, svc.Spec.Ports, 1)
	assert.EqualValues(t, 8080, svc.Spec.Ports[0].Port)
}

// egress deny NetworkPolicy：per-carrier podSelector + 同 ns/DNS 放行集 +
// 其余出站拒（ADR-0052 决策 6 的静态形态锚）。
func TestToEgressNetpol(t *testing.T) {
	np := toEgressNetpol()
	assert.Equal(t, map[string]string{labelEgress: "true"}, np.Spec.PodSelector.MatchLabels)
	assert.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)
	require.Len(t, np.Spec.Egress, 2)
	// 规则一：同 Namespace 全通（空 podSelector = 命名空间内全部 pod）。
	require.NotNil(t, np.Spec.Egress[0].To[0].PodSelector)
	// 规则二：kube-system DNS（53 UDP/TCP）。
	dnsRule := np.Spec.Egress[1]
	require.NotNil(t, dnsRule.To[0].NamespaceSelector)
	assert.Equal(t, dnsNamespace, dnsRule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"])
	require.Len(t, dnsRule.Ports, 2)
}

// PVC：RWO + local-path 缺省 StorageClass（不显式指定）+ 名义请求量。
func TestToPVC(t *testing.T) {
	pvc := toPVC("01V")
	assert.Equal(t, "fleetly-vol-01v", pvc.Name)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, pvc.Spec.AccessModes)
	assert.Empty(t, pvc.Spec.StorageClassName, "default storage class (local-path) by omission")
	assert.Equal(t, defaultVolumeStorage, pvc.Spec.Resources.Requests.Storage().String())
}

// canonicalJSON：相同输入稳定（map 键排序内建）。
func TestCanonicalJSONStable(t *testing.T) {
	a := toDeployment(capability.NamespaceRef{Project: "p", App: "a"}, capability.Workload{ID: "w", Process: "x"}, 1, nil, nil)
	b := toDeployment(capability.NamespaceRef{Project: "p", App: "a"}, capability.Workload{ID: "w", Process: "x"}, 1, nil, nil)
	assert.Equal(t, canonicalJSON(a), canonicalJSON(b))
}

// Describe Notes 是能力发现面的诚实边界声明（架构 §10：与 swarm 弱隔离
// Notes 对照）——强隔离/全名折点/exec 缺席三锚入测，措辞漂移即红。
func TestDescribeNotesHonesty(t *testing.T) {
	p := &Provider{}
	notes := strings.Join(p.Describe().Notes, "\n")
	assert.Contains(t, notes, "egress:none is strong isolation")
	assert.Contains(t, notes, "fold dots to dashes")
	assert.Contains(t, notes, "exec subface is not implemented")
	assert.Contains(t, notes, "without declared ports resolve via headless services")
	assert.Equal(t, "k3s", p.Describe().Name)
	assert.Equal(t, capability.KindRuntime, p.Describe().Capability)
}
