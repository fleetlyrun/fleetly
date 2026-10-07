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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
