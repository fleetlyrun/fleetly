package k3s

// RBAC 自举单测（ADR-0053 决策 4）：fresh 收敛全对象 / 已在位零写幂等 /
// 规则漂移即更新 / 表格逐行钉死最小权限集。fake clientset 无 token
// controller——token Secret 由夹具预填或断言等待窗超时面。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// tokenSecretFilled 是 controller 已填充的 token Secret 夹具（fake 无
// controller——在位形态直接预填）。
func tokenSecretFilled(token string) runtime.Object {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: systemNamespace,
			Name:      rbacTokenSecret,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: rbacServiceAccount,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
		Data: map[string][]byte{"token": []byte(token)},
	}
}

// desiredBinding 是期望形态的 ClusterRoleBinding 夹具。
func desiredBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rbacServiceAccount},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: rbacServiceAccount},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: rbacServiceAccount, Namespace: systemNamespace}},
	}
}

// countWrites 经 reactor 链统计写动作（create/update/delete/patch）。
func countWrites(cli *fake.Clientset) *int {
	n := new(int)
	cli.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		switch action.(type) {
		case k8stesting.CreateAction, k8stesting.UpdateAction, k8stesting.DeleteAction, k8stesting.PatchAction:
			*n++
		}
		return false, nil, nil // 只观察不拦截（默认 reaction 继续服务）
	})
	return n
}

// TestEnsureRBACFreshConverge：空集群上一次收敛全部对象并取回 token。
func TestEnsureRBACFreshConverge(t *testing.T) {
	cli := fake.NewClientset(tokenSecretFilled("tok-1"))
	got, err := ensureRBAC(context.Background(), cli, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "tok-1", got)

	ctx := context.Background()
	_, err = cli.CoreV1().Namespaces().Get(ctx, systemNamespace, metav1.GetOptions{})
	require.NoError(t, err, "system namespace must exist")
	_, err = cli.CoreV1().ServiceAccounts(systemNamespace).Get(ctx, rbacServiceAccount, metav1.GetOptions{})
	require.NoError(t, err, "service account must exist")
	role, err := cli.RbacV1().ClusterRoles().Get(ctx, rbacServiceAccount, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, desiredClusterRole().Rules, role.Rules, "cluster role rules must match the desired table")
	binding, err := cli.RbacV1().ClusterRoleBindings().Get(ctx, rbacServiceAccount, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, binding.Subjects, 1)
	assert.Equal(t, systemNamespace, binding.Subjects[0].Namespace)
	assert.Equal(t, rbacServiceAccount, binding.Subjects[0].Name)
}

// TestEnsureRBACIdempotentZeroWrite：全对象在位且规则一致 → 零写入
// （SA kubeconfig 直接起动的生产形态无 admin 需求的锚）。
func TestEnsureRBACIdempotentZeroWrite(t *testing.T) {
	cli := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: systemNamespace, Name: rbacServiceAccount}},
		desiredClusterRole(),
		desiredBinding(),
		tokenSecretFilled("tok-2"),
	)
	writes := countWrites(cli)
	got, err := ensureRBAC(context.Background(), cli, time.Second)
	require.NoError(t, err)
	assert.Equal(t, "tok-2", got)
	assert.Zero(t, *writes, "converged state must not issue any write")
}

// TestEnsureRBACRoleDriftUpdates：规则漂移（缺一行）→ 更新收敛。
func TestEnsureRBACRoleDriftUpdates(t *testing.T) {
	drifted := desiredClusterRole()
	drifted.Rules = drifted.Rules[:len(drifted.Rules)-1]
	cli := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: systemNamespace, Name: rbacServiceAccount}},
		drifted,
		desiredBinding(),
		tokenSecretFilled("tok-3"),
	)
	_, err := ensureRBAC(context.Background(), cli, time.Second)
	require.NoError(t, err)
	role, err := cli.RbacV1().ClusterRoles().Get(context.Background(), rbacServiceAccount, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, role.Rules, len(desiredClusterRole().Rules), "drifted rules must be reconverged")
}

// TestEnsureRBACTokenWait：token 未填充 → 窗内如实效错（诚实等待面）。
func TestEnsureRBACTokenWait(t *testing.T) {
	cli := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNamespace}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: systemNamespace,
				Name:      rbacTokenSecret,
				Annotations: map[string]string{
					corev1.ServiceAccountNameKey: rbacServiceAccount,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		},
	)
	_, err := ensureRBAC(context.Background(), cli, 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not populated")
}

// TestDesiredClusterRoleTable：最小权限集逐行钉死（ADR-0053 决策 4 表格
// 的静态锚——动词面扩表必须同批改此测试与 ADR）。
func TestDesiredClusterRoleTable(t *testing.T) {
	rule := func(group string, resources []string, verbs ...string) rbacv1.PolicyRule {
		return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: verbs}
	}
	assert.Equal(t, []rbacv1.PolicyRule{
		rule("", []string{"namespaces"}, "create", "delete", "get"), // delete = 空域收尾(ADR-0056 决策 5)
		rule("", []string{"nodes"}, "get", "list", "update"),
		rule("", []string{"pods"}, "create", "delete", "get", "list", "watch"),
		rule("", []string{"pods/exec"}, "create"),
		rule("", []string{"pods/log"}, "get"), // k8s 子资源真名单数(staging k3s 实证)
		rule("", []string{"services"}, "create", "delete", "get", "list", "update"),
		rule("", []string{"secrets"}, "create", "delete", "deletecollection", "get", "list", "update"),
		rule("", []string{"persistentvolumeclaims"}, "create", "get", "list"), // list = 空域收尾零卷判据(ADR-0056)
		rule("", []string{"events"}, "get", "list"),
		rule("apps", []string{"deployments", "daemonsets"}, "create", "delete", "get", "list", "update"),
		rule("networking.k8s.io", []string{"networkpolicies"}, "create", "delete", "get", "list"),
		rule("policy", []string{"pods/eviction"}, "create"),
	}, desiredClusterRole().Rules)
}

// TestRulesEqual：组内词序/规则序不参与语义；集合差即不等。
func TestRulesEqual(t *testing.T) {
	a := desiredClusterRole().Rules

	// 词序反转 + 规则序反转（norm 后应等价）。
	shuffled := make([]rbacv1.PolicyRule, len(a))
	for i := range a {
		verbs := append([]string(nil), a[i].Verbs...)
		for l, r := 0, len(verbs)-1; l < r; l, r = l+1, r-1 {
			verbs[l], verbs[r] = verbs[r], verbs[l]
		}
		shuffled[len(a)-1-i] = rbacv1.PolicyRule{APIGroups: a[i].APIGroups, Resources: a[i].Resources, Verbs: verbs}
	}
	assert.True(t, rulesEqual(a, shuffled))
	assert.False(t, rulesEqual(a, a[:len(a)-1]), "missing rule must not be equal")
	extra := append(append([]rbacv1.PolicyRule(nil), a...),
		rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"patch"}})
	assert.False(t, rulesEqual(a, extra), "extra verb must not be equal")
}
