package k3s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

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
	sec, err := cli.CoreV1().Secrets(nsName).Get(ctx, secretObjectName("db-pass"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), sec.Data["value"])
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
	_, err = cli.CoreV1().Secrets(nsName).Get(ctx, secretObjectName("db-pass"), metav1.GetOptions{})
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
	assert.Equal(t, "api-web.fleetly-shop.svc:8080", eps[0].Addr)
	assert.Equal(t, "api-web.fleetly-shop.svc:9090", eps[1].Addr)
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

func errNotFound(err error) bool { return apierrors.IsNotFound(err) }
