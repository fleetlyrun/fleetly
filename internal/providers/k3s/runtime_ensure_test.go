package k3s

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// newFakeProvider 构造 fake clientset 支撑的 Provider（hermetic——零真
// apiserver；fakedaemon_test.go 同款纪律）。
func newFakeProvider(objs ...runtime.Object) (*Provider, *fake.Clientset) {
	cli := fake.NewClientset(objs...)
	p := &Provider{cli: cli, apiServer: "https://k3s-lab:6443"}
	return p, cli
}

func appNS() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
}

// Ensure 全链：Namespace/PVC/Secret/载体/Service 建立 + 材料挂载 + egress
// netpol 收敛。
func TestEnsureCreatesObjects(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27", Replicas: 1,
		Ports:          []capability.WorkloadPort{{Port: 8080}},
		Volumes:        []capability.VolumeMount{{VolumeID: "01V", Target: "/data"}},
		Addressing:     []capability.Address{{Name: "api.web"}},
		EgressNetworks: []string{"isolated"},
	}}
	mats := capability.Materials{SecretFiles: map[string][]byte{"db-pass": []byte("secret")}}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 5, mats))

	nsName := "fleetly-shop"
	// Namespace。
	_, err := cli.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	require.NoError(t, err)
	// Deployment + 标记。
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "5", d.Labels[labelGeneration])
	assert.Equal(t, "true", d.Labels[labelEgress])
	// PVC（Volume 声明先行）。
	_, err = cli.CoreV1().PersistentVolumeClaims(nsName).Get(ctx, pvcName("01V"), metav1.GetOptions{})
	require.NoError(t, err)
	// Secret 材料（值不落载体 label/明文 env——ADR-0014）。
	sec, err := cli.CoreV1().Secrets(nsName).Get(ctx, domainSecretObjectName(appNS(), "db-pass"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), sec.Data[secretDataKey])
	// 材料注入形态：projected 卷挂 /run/secrets，value 键投影为文件
	// <平台名>（docker secrets 语义对齐——裸 Secret 卷的两级目录形态会
	// 让模板 _FILE env 指到目录即崩，e2e db 段 CrashLoop 实证）。
	var pv *corev1.ProjectedVolumeSource
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.Name == secretsVolumeName {
			pv = v.Projected
		}
	}
	require.NotNil(t, pv, "materials must land as a single projected volume")
	require.Len(t, pv.Sources, 1)
	assert.Equal(t, domainSecretObjectName(appNS(), "db-pass"), pv.Sources[0].Secret.Name)
	require.Len(t, pv.Sources[0].Secret.Items, 1)
	assert.Equal(t, secretDataKey, pv.Sources[0].Secret.Items[0].Key)
	assert.Equal(t, "db-pass", pv.Sources[0].Secret.Items[0].Path)
	found := false
	for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.MountPath == "/run/secrets" {
			found = true
		}
	}
	assert.True(t, found)
	// Addressing Service。
	svc, err := cli.CoreV1().Services(nsName).Get(ctx, "api-web", metav1.GetOptions{})
	require.NoError(t, err)
	assert.EqualValues(t, 8080, svc.Spec.Ports[0].Port)
	// egress netpol（egress 载体在场即建）。
	_, err = cli.NetworkingV1().NetworkPolicies(nsName).Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	require.NoError(t, err)
}

// egress netpol 收敛的另一边：期望集不再含 egress 载体 → policy 撤除。
func TestEnsureRemovesNetpolWhenNoEgressCarriers(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27",
		EgressNetworks: []string{"isolated"},
	}}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 1, capability.Materials{}))
	_, err := cli.NetworkingV1().NetworkPolicies(nsName).Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	require.NoError(t, err)

	ws[0].EgressNetworks = nil
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 2, capability.Materials{}))
	_, err = cli.NetworkingV1().NetworkPolicies(nsName).Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	assert.True(t, errNotFound(err), "netpol must be removed when no egress carriers remain")
}

// 域内收敛：stale 载体随 Ensure 移除；同 Generation 重放安全。
func TestEnsureConvergesStaleCarriers(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	// 先落两个进程。
	ws := []capability.Workload{
		{ID: "w1", Process: "api", Image: "nginx:1.27"},
		{ID: "w2", Process: "worker", Image: "nginx:1.27"},
	}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 1, capability.Materials{}))
	_, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-worker", metav1.GetOptions{})
	require.NoError(t, err)

	// 期望集收窄为单进程：worker 被移除。
	require.NoError(t, p.Ensure(ctx, appNS(), ws[:1], 1, capability.Materials{}))
	_, err = cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-worker", metav1.GetOptions{})
	assert.True(t, errNotFound(err), "stale carrier must be removed by domain convergence")

	// 载体名碰撞前置拒绝（swarm B9 同款）。
	collide := []capability.Workload{
		{ID: "w1", Process: "api"},
		{ID: "w9", Process: "api"},
	}
	err = p.Ensure(ctx, appNS(), collide, 1, capability.Materials{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "carrier name collision")
}

// Remove 拆域内载体与 Service；PVC/Secret 是数据与材料面不随域拆。
func TestRemoveKeepsDataPlane(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27",
		Volumes:    []capability.VolumeMount{{VolumeID: "01V", Target: "/data"}},
		Addressing: []capability.Address{{Name: "api.web"}},
	}}
	mats := capability.Materials{SecretFiles: map[string][]byte{"db-pass": []byte("s")}}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 1, mats))
	require.NoError(t, p.Remove(ctx, appNS()))

	_, err := cli.AppsV1().Deployments(nsName).Get(ctx, "fleetly-web-api", metav1.GetOptions{})
	assert.True(t, errNotFound(err))
	_, err = cli.CoreV1().Services(nsName).Get(ctx, "api-web", metav1.GetOptions{})
	assert.True(t, errNotFound(err))
	// PVC 与 Secret 残留（数据处置是显式动作——场景 3 语义）。
	_, err = cli.CoreV1().PersistentVolumeClaims(nsName).Get(ctx, pvcName("01V"), metav1.GetOptions{})
	assert.NoError(t, err)
	_, err = cli.CoreV1().Secrets(nsName).Get(ctx, domainSecretObjectName(appNS(), "db-pass"), metav1.GetOptions{})
	assert.NoError(t, err)
}

// one-shot Run 域：RestartNever → 独立 Pod；重放不重建（AlreadyExists 幂等）。
func TestEnsureOneShotPod(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := capability.NamespaceRef{Team: "acme", Project: "shop", Task: "01T"}
	ws := []capability.Workload{{
		ID: "01R", Process: "run", Image: "busybox:1.37", Restart: capability.RestartNever,
		Addressing: []capability.Address{{Name: "task-01T"}, {Name: "run-01R"}},
	}}
	require.NoError(t, p.Ensure(ctx, ns, ws, 1, capability.Materials{}))
	// 独立 Pod（无 controller owner）。
	pod, err := cli.CoreV1().Pods("fleetly-shop").Get(ctx, "fleetly-run-01r", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	// 池级 + per-Run 双 Service（池级 selector 选全部 run）。
	_, err = cli.CoreV1().Services("fleetly-shop").Get(ctx, "task-01t", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = cli.CoreV1().Services("fleetly-shop").Get(ctx, "run-01r", metav1.GetOptions{})
	require.NoError(t, err)
	// 重放幂等。
	require.NoError(t, p.Ensure(ctx, ns, ws, 1, capability.Materials{}))
}

// Addresses：期望集成员的 Addressing DNS × 声明端口（期望集端口注入）。
func TestAddresses(t *testing.T) {
	p, _ := newFakeProvider()
	ws := []capability.Workload{{
		ID: "w1", Process: "api",
		Ports:      []capability.WorkloadPort{{Port: 8080}, {Port: 9090}},
		Addressing: []capability.Address{{Name: "api.web"}},
	}}
	eps, err := p.Addresses(context.Background(), appNS(), ws)
	require.NoError(t, err)
	require.Len(t, eps, 2)
	// Addr 裸主机名、端口在 Port 字段分立（swarm 契约同构——消费面拼
	// scheme://Addr:Port，端口折进 Addr 即双端口 URL，traefik precheck 即拒）。
	assert.Equal(t, "api-web.fleetly-shop.svc", eps[0].Addr)
	assert.EqualValues(t, 8080, eps[0].Port)
	assert.Equal(t, "api-web.fleetly-shop.svc", eps[1].Addr)
	assert.EqualValues(t, 9090, eps[1].Port)
}

// InspectWorkloads：Deployment spec 还原（gen/workload id/镜像/命令/副本）。
func TestInspectWorkloads(t *testing.T) {
	p, _ := newFakeProvider()
	ctx := context.Background()
	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27", Command: []string{"/app"},
	}}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 4, capability.Materials{}))
	obs, err := p.InspectWorkloads(ctx, appNS())
	require.NoError(t, err)
	require.Len(t, obs, 1)
	assert.Equal(t, "w1", obs[0].WorkloadID)
	assert.EqualValues(t, 4, obs[0].Generation)
	assert.Equal(t, "nginx:1.27", obs[0].Image)
	assert.Equal(t, []string{"/app"}, obs[0].Command)
	assert.EqualValues(t, 1, obs[0].Replicas)
}

// pod 状态映射：one-shot 终态（Succeeded/Failed + 退出码）、长运行
// ready/not-ready、CrashLoopBackOff。
func TestPodWorkloadState(t *testing.T) {
	succ := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	st, code, _ := podWorkloadState(succ)
	assert.Equal(t, capability.WorkloadCompleted, st)
	require.NotNil(t, code)
	assert.Zero(t, *code)

	fail := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}
	st, code, _ = podWorkloadState(fail)
	assert.Equal(t, capability.WorkloadFailed, st)
	require.NotNil(t, code)
	assert.NotZero(t, *code)

	pending := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
	st, _, _ = podWorkloadState(pending)
	assert.Equal(t, capability.WorkloadPending, st)
}

// 无声明端口的工作负载 → headless Service（e2e 实证回归：portless 普通
// Service 撞 k8s 硬校验 "spec.ports: Required value" 整拍 Ensure 炸——
// v1.36 校验源明证零端口仅 headless/ExternalName 合法；headless 保名解析
// 语义，多副本无 VIP 轮询是 Notes 诚实边界）。
func TestEnsurePortlessWorkloadHeadlessService(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ws := []capability.Workload{
		{
			ID: "w1", Process: "worker", Image: "busybox:1.37",
			Addressing: []capability.Address{{Name: "worker.app"}},
		},
		{
			ID: "w2", Process: "api", Image: "nginx:1.27",
			Ports:      []capability.WorkloadPort{{Port: 8080}},
			Addressing: []capability.Address{{Name: "api.app"}},
		},
	}
	require.NoError(t, p.Ensure(ctx, appNS(), ws, 1, capability.Materials{}))

	// portless：headless（ClusterIP None）+ 零端口——合法形态。
	svc, err := cli.CoreV1().Services("fleetly-shop").Get(ctx, "worker-app", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.ClusterIPNone, svc.Spec.ClusterIP)
	assert.Empty(t, svc.Spec.Ports)
	// 有端口对照：普通 ClusterIP Service 携声明端口。
	svc, err = cli.CoreV1().Services("fleetly-shop").Get(ctx, "api-app", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, svc.Spec.ClusterIP)
	require.Len(t, svc.Spec.Ports, 1)
	assert.EqualValues(t, 8080, svc.Spec.Ports[0].Port)
}

// 工具 Pod 挂载落位（e2e 深挖终章回归：container 局部变量的 append 不进
// pod spec——零挂载形态下 passfile/输入文件全部不可见，备份恢复全断）。
func TestBuildUtilityPodMountsLand(t *testing.T) {
	ctx := context.Background()
	// 种一个带锚定 label 的节点（工具 Pod 钉住解析的前提——fake 无节点
	// 会让 buildUtilityPod 在 utilityNodeSelector 处失败）。
	seededNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "n1",
		Labels: map[string]string{labelNodeID: "01N1", "node-role.kubernetes.io/control-plane": ""},
	}}
	p, _ := newFakeProvider(seededNode)
	req := capability.UtilityRequest{
		ID:        "restore-x",
		Namespace: capability.NamespaceRef{Project: "shop"},
		Image:     "postgres:17-bookworm",
		Argv:      []string{"pg_restore"},
		Env:       map[string]string{"PGPASSFILE": "/run/secrets/database-backup-pgpass"}, //nolint:gosec // 环境变量名形似凭证串,实为路径声明
		SecretFiles: map[string][]byte{
			"database-backup-pgpass": []byte("db-x:5432:fleetly:fleetly:pw"), //nolint:gosec // 测试载荷,非凭证本体
		},
		Input: &capability.UtilityInput{
			Content: strings.NewReader("DUMP-BYTES"),
			// 与 dbtemplate.BackupInputPath 契约同值（providers 不 import
			// engine——字面量 + 守卫双向由 engine 侧测试承载）。
			Target: "/run/secrets/backup-input",
		},
	}
	pod, err := p.buildUtilityPod(ctx, req, "fleetly-shop", "fleetly-util-restore-x", "/var/lib/fleetly/utility/restore-x")
	require.NoError(t, err)
	require.Len(t, pod.Spec.Containers, 1)
	var mounted []string
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		mounted = append(mounted, m.MountPath)
	}
	assert.Contains(t, mounted, "/run/secrets", "materials+input projection must land on the container (not the dead local copy)")
	assert.Equal(t, "PGPASSFILE", pod.Spec.Containers[0].Env[0].Name)
	// 投影卷在场（材料 + 输入双 source）。
	found := false
	for _, v := range pod.Spec.Volumes {
		if v.Name == secretsVolumeName && v.Projected != nil {
			found = len(v.Projected.Sources) == 2
		}
	}
	assert.True(t, found, "materials and input project through one /run/secrets volume")
}

// putDeployment 冲突重试（e2e 实证回归：rollback 重放拍 Get→Update 窗口撞
// deployment controller 的 status 写（resourceVersion 抬升）即 409——
// retry.OnConflict 重读重试收敛）。
func TestPutDeploymentRetriesOnConflict(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	seed := toDeployment(appNS(), capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27"}, 1, nil, nil)
	seed.ResourceVersion = "10"
	_, err := cli.AppsV1().Deployments("fleetly-shop").Create(ctx, seed, metav1.CreateOptions{})
	require.NoError(t, err)

	conflicts := 0
	cli.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		conflicts++
		if conflicts == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"},
				seed.Name, errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	require.NoError(t, p.putDeployment(ctx, "fleetly-shop", seed))
	assert.GreaterOrEqual(t, conflicts, 2, "first 409 must be retried, not surfaced")
}

func errNotFound(err error) bool { return apierrors.IsNotFound(err) }
