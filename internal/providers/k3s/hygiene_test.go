package k3s

// Hygiene 单测（ADR-0053 决策 3）：孤儿判据（引用集含缩容到零的
// Deployment template / 宽限窗 / managed 过滤）、字典序与 maxDelete 限流、
// PVC 面诚实 no-op；附带材料值轮换（ensureSecrets/ensureImagePullSecrets
// 的 create-or-update）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// managedSecret 是受管 Secret 夹具（age 控制宽限窗判据）。
func managedSecret(ns, name string, age time.Duration) runtime.Object {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			Labels:            map[string]string{labelManaged: "true"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{secretDataKey: []byte("v")},
	}
}

// referencingDeployment 是引用某 Secret 的 Deployment（副本可为 0——
// 缩容到零仍是现役引用的锚）。
func referencingDeployment(ns, name, secretName string, replicas int32) runtime.Object {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         "c",
						Image:        "nginx",
						VolumeMounts: []corev1.VolumeMount{{Name: "mat", MountPath: "/run/secrets"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "mat",
						VolumeSource: corev1.VolumeSource{
							Projected: &corev1.ProjectedVolumeSource{
								Sources: []corev1.VolumeProjection{{
									Secret: &corev1.SecretProjection{
										LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
									},
								}},
							},
						},
					}},
				},
			},
		},
	}
}

func TestSweepOrphanSecretsCriteria(t *testing.T) {
	p, cli := newFakeProvider(
		// 孤儿：老 + 无引用 → 删。
		managedSecret("fleetly-shop", "fleetly-mat-a-db-pass", 3*time.Hour),
		// 现役：被缩容到零的 Deployment template 引用 → 留。
		managedSecret("fleetly-shop", "fleetly-mat-b-db-pass", 3*time.Hour),
		referencingDeployment("fleetly-shop", "d-b", "fleetly-mat-b-db-pass", 0),
		// 宽限窗内：无引用 → 留。
		managedSecret("fleetly-shop", "fleetly-mat-c-db-pass", 10*time.Minute),
		// 非 managed：无引用 → 不碰。
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "fleetly-shop",
				Name:              "user-secret",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-72 * time.Hour)),
				// 无 fleetly.managed 标签（utility 材料同形态）
			},
		},
	)
	deleted, err := p.SweepOrphanSecrets(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	ctx := context.Background()
	_, err = cli.CoreV1().Secrets("fleetly-shop").Get(ctx, "fleetly-mat-a-db-pass", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "unreferenced aged secret must be swept")
	for _, keep := range []string{"fleetly-mat-b-db-pass", "fleetly-mat-c-db-pass", "user-secret"} {
		_, err := cli.CoreV1().Secrets("fleetly-shop").Get(ctx, keep, metav1.GetOptions{})
		assert.NoError(t, err, "%s must be kept", keep)
	}
}

func TestSweepOrphanSecretsMaxDeleteAndOrder(t *testing.T) {
	// 三孤儿 + maxDelete=2：namespace/名字典序删前两个。
	p, cli := newFakeProvider(
		managedSecret("fleetly-shop", "fleetly-mat-z", 3*time.Hour),
		managedSecret("fleetly-shop", "fleetly-mat-a", 3*time.Hour),
		managedSecret("fleetly-other", "fleetly-mat-x", 3*time.Hour),
	)
	deleted, err := p.SweepOrphanSecrets(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	ctx := context.Background()
	_, err = cli.CoreV1().Secrets("fleetly-shop").Get(ctx, "fleetly-mat-a", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "lexicographically first must be swept")
	_, err = cli.CoreV1().Secrets("fleetly-other").Get(ctx, "fleetly-mat-x", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "namespace order precedes name order")
	_, err = cli.CoreV1().Secrets("fleetly-shop").Get(ctx, "fleetly-mat-z", metav1.GetOptions{})
	assert.NoError(t, err, "budget exhausted: remaining orphan stays for next sweep")
}

func TestSweepOrphanSecretsZeroBudget(t *testing.T) {
	p, _ := newFakeProvider(managedSecret("fleetly-shop", "fleetly-mat-a", 3*time.Hour))
	deleted, err := p.SweepOrphanSecrets(context.Background(), 0)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

func TestSweepOrphanVolumesNoop(t *testing.T) {
	p, _ := newFakeProvider()
	deleted, err := p.SweepOrphanVolumes(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, deleted, "k8s volume sweep must be an honest no-op")
}

// TestEnsureSecretsRotatesValue：值变 → 更新；值同 → 零写（幂等重放）。
func TestEnsureSecretsRotatesValue(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	ns := appNS()

	m1, err := p.ensureSecrets(ctx, ns, "fleetly-shop", map[string][]byte{"db-pass": []byte("old")})
	require.NoError(t, err)
	objName := m1["db-pass"]

	// 值变：更新到达载体。
	_, err = p.ensureSecrets(ctx, ns, "fleetly-shop", map[string][]byte{"db-pass": []byte("new")})
	require.NoError(t, err)
	sec, err := cli.CoreV1().Secrets("fleetly-shop").Get(ctx, objName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new", string(sec.Data[secretDataKey]), "rotated value must reach the carrier secret")

	// 值同：零写。
	updates := 0
	cli.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})
	_, err = p.ensureSecrets(ctx, ns, "fleetly-shop", map[string][]byte{"db-pass": []byte("new")})
	require.NoError(t, err)
	assert.Zero(t, updates, "same value replay must not write")
}

// TestEnsureImagePullSecretsRotatesValue：registry 凭证轮换同面。
func TestEnsureImagePullSecretsRotatesValue(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	old := map[string]capability.RegistryCredential{"registry.example:5000": {Server: "registry.example:5000", Username: "u", Secret: "old"}}
	_, err := p.ensureImagePullSecrets(ctx, "fleetly-shop", old)
	require.NoError(t, err)
	cur := map[string]capability.RegistryCredential{"registry.example:5000": {Server: "registry.example:5000", Username: "u", Secret: "new"}}
	names, err := p.ensureImagePullSecrets(ctx, "fleetly-shop", cur)
	require.NoError(t, err)
	require.Len(t, names, 1)
	sec, err := cli.CoreV1().Secrets("fleetly-shop").Get(ctx, names[0], metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, string(sec.Data[corev1.DockerConfigJsonKey]), `"new"`, "rotated registry credential must reach the pull secret")
}

// putGrant 是 grant 夹具的显式落位通道（接收方 ns；toPeerGrantPolicy 不设
// ns——与 netisolate 测试同款经 Create 路径放置）。
func putGrant(t *testing.T, cli *fake.Clientset, recvNS string, pol *networkingv1.NetworkPolicy) {
	t.Helper()
	_, err := cli.NetworkingV1().NetworkPolicies(recvNS).Create(context.Background(), pol, metav1.CreateOptions{})
	require.NoError(t, err)
}

// TestSweepOrphanPeerGrantsCriteria：判据矩阵（ADR-0055 决策 3）——owner
// ns 缺失删 / 双缺（无活 pod 持 key ∧ 无成员 policy）删 / 活 pod 持 key 保留
// / 成员 policy 在场（声明方意图锚，如缩容到零）保留 / 未知选择器形状不碰。
func TestSweepOrphanPeerGrantsCriteria(t *testing.T) {
	recvNS := "fleetly-partner"
	shopKey := netLabelKey("shop", "default")
	goneKey := netLabelKey("gone", "default")
	idleKey := netLabelKey("idler", "default")
	intentKey := netLabelKey("intent", "default")
	weirdKey := netLabelKey("weird", "other")
	p, cli := newFakeProvider()
	ctx := context.Background()
	putGrant(t, cli, recvNS, toPeerGrantPolicy("gone", "web", goneKey))
	putGrant(t, cli, recvNS, toPeerGrantPolicy("idler", "web", idleKey))
	putGrant(t, cli, recvNS, toPeerGrantPolicy("shop", "web", shopKey))
	putGrant(t, cli, recvNS, toPeerGrantPolicy("intent", "web", intentKey))
	putGrant(t, cli, recvNS, &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "fleetly-peer-weird",
			Labels: map[string]string{
				labelManaged:    "true",
				labelPeerOwner:  "weird",
				labelPeerDomain: "web",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{
				weirdKey: "true",
				"other":  "true",
			}},
		},
	})
	// 现役①的声明方活 pod（持 key——任意相位均在场）。
	seedMemberPod(t, p, "fleetly-shop", "shop-pod", shopKey)
	// 现役②的意图锚：声明方 ns 的成员 policy 在场（无活 pod）。
	_, err := cli.NetworkingV1().NetworkPolicies("fleetly-intent").Create(ctx, toNetIsolationPolicy(intentKey, ""), metav1.CreateOptions{})
	require.NoError(t, err)

	deleted, err := p.SweepOrphanPeerGrants(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	_, err = cli.NetworkingV1().NetworkPolicies(recvNS).Get(ctx, peerGrantName("gone", "web", goneKey), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "owner-namespace-missing grant must be swept")
	_, err = cli.NetworkingV1().NetworkPolicies(recvNS).Get(ctx, peerGrantName("idler", "web", idleKey), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "dead-intent grant (no pods, no member policy) must be swept")
	for _, keep := range []string{
		peerGrantName("shop", "web", shopKey),
		peerGrantName("intent", "web", intentKey),
		"fleetly-peer-weird",
	} {
		_, err := cli.NetworkingV1().NetworkPolicies(recvNS).Get(ctx, keep, metav1.GetOptions{})
		assert.NoError(t, err, "%s must be kept", keep)
	}
}

// TestSweepOrphanPeerGrantsBudgetAndOrder：字典序 + 预算限流 + 零预算。
// 全部夹具是孤儿（声明方 ns 全缺）——按接收方 ns/名字典序删前 N。
func TestSweepOrphanPeerGrantsBudgetAndOrder(t *testing.T) {
	k1 := netLabelKey("alpha", "default")
	k2 := netLabelKey("beta", "default")
	k3 := netLabelKey("gamma", "default")
	p, cli := newFakeProvider()
	ctx := context.Background()
	// 同一接收方 ns 的三个孤儿 grant：字典序 = peerGrantName 哈希名排序。
	putGrant(t, cli, "fleetly-recv", toPeerGrantPolicy("alpha", "web", k1))
	putGrant(t, cli, "fleetly-recv", toPeerGrantPolicy("beta", "web", k2))
	putGrant(t, cli, "fleetly-recv", toPeerGrantPolicy("gamma", "web", k3))

	deleted, err := p.SweepOrphanPeerGrants(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	grants, err := cli.NetworkingV1().NetworkPolicies("fleetly-recv").List(ctx, metav1.ListOptions{LabelSelector: labelPeerOwner})
	require.NoError(t, err)
	require.Len(t, grants.Items, 1, "budget exhausted: remaining orphan stays for next sweep")

	deleted, err = p.SweepOrphanPeerGrants(ctx, 0)
	require.NoError(t, err)
	assert.Zero(t, deleted, "zero budget must be a no-op")
	grants, err = cli.NetworkingV1().NetworkPolicies("fleetly-recv").List(ctx, metav1.ListOptions{LabelSelector: labelPeerOwner})
	require.NoError(t, err)
	assert.Len(t, grants.Items, 1)

	deleted, err = p.SweepOrphanPeerGrants(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "remaining orphan swept on the next full-budget pass")
}
