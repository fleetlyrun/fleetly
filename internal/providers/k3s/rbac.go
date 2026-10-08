package k3s

// RBAC 最小权限收敛（ADR-0053 决策 4，ADR-0052 挂账 6）：Provider 以自举
// 身份（调用方给的 kubeconfig——试点形态即 k3s admin）落一套专用身份
// （fleetly-system Namespace + fleetly-manager ServiceAccount + 同名
// ClusterRole 最小规则集 + ClusterRoleBinding + 长期 token Secret），随后
// 工作客户端整体换为 SA token——自举客户端即弃，进程内不再持全权凭证。
//
// 与载体侧 automountServiceAccountToken=false 正交：SA 是 fleetlyd 自己的
// 身份，不挂任何载体。规则集 = Provider 全部动词面的精确清单（表格逐行
// 点验见 ADR-0053；升格动词面时同批扩表 + admin 重跑一次收敛——SA 自身
// 无权改 ClusterRole，这是有意的单向门）。

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// rbacServiceAccount 是 fleetlyd 的集群身份（systemNamespace 内）。
	rbacServiceAccount = "fleetly-manager"
	// rbacTokenSecret 是长期 SA token 的载体（controller 填充 data.token；
	// 轮换是后续批——长期 token 的诚实边界入 Notes/runbook）。
	rbacTokenSecret = "fleetly-manager-token"
	// rbacTokenWait 是等 controller 填 token 的窗口（k3s 内嵌
	// controller-manager 秒级填充；测试夹具可缩短）。
	rbacTokenWait = 15 * time.Second
)

// desiredClusterRole 是最小权限规则集（ADR-0053 决策 4 表格的代码单源：
// 动词按 Provider 现有调用面逐一点验——新 API 调用面必须同批扩表）。
func desiredClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: rbacServiceAccount},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"namespaces"}, Verbs: []string{"create", "get"}},
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "update"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create", "delete", "get", "list", "watch"}},
			{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}},
			// pods/log 是 k8s 子资源真名（单数——"pods/logs" 不命中任何
			// 子资源，log collector 全程 403；staging k3s 真机实证 ADR-0055）。
			{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}},
			{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"create", "delete", "get", "list", "update"}},
			{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"create", "delete", "deletecollection", "get", "list", "update"}},
			{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, Verbs: []string{"create", "get"}},
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets"}, Verbs: []string{"create", "delete", "get", "list", "update"}},
			// networkpolicies list：成员资格隔离的期望集收敛对照（ADR-0054）。
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"create", "delete", "get", "list"}},
			{APIGroups: []string{"policy"}, Resources: []string{"pods/eviction"}, Verbs: []string{"create"}},
		},
	}
}

// ensureRBAC 以自举身份收敛 RBAC 对象并返回 SA token。幂等：对象在位且
// ClusterRole 规则与期望一致时零写入（SA kubeconfig 直接起动的生产形态
// 无 admin 需求）；规则漂移（平台升级改动词面）需要写权限，错误文本带
// 可行动指引（以管理 kubeconfig 重跑一次收敛）。
func ensureRBAC(ctx context.Context, boot kubernetes.Interface, tokenWait time.Duration) (string, error) {
	// 全部对象 Get-first（缺席才写）：SA kubeconfig 直接起动的生产形态
	// 已收敛时零写、零 admin 权限需求。
	// Namespace（受管域同域——已是受管 reconciler 的落点，幂等复用）。
	if _, err := boot.CoreV1().Namespaces().Get(ctx, systemNamespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, cerr := boot.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: systemNamespace, Labels: map[string]string{labelManaged: "true"}},
		}, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return "", fmt.Errorf("k3s rbac bootstrap: namespace: %w", cerr)
		}
	} else if err != nil {
		return "", fmt.Errorf("k3s rbac bootstrap: namespace: %w", err)
	}
	// ServiceAccount。
	if _, err := boot.CoreV1().ServiceAccounts(systemNamespace).Get(ctx, rbacServiceAccount, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, cerr := boot.CoreV1().ServiceAccounts(systemNamespace).Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: rbacServiceAccount},
		}, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return "", fmt.Errorf("k3s rbac bootstrap: service account: %w", cerr)
		}
	} else if err != nil {
		return "", fmt.Errorf("k3s rbac bootstrap: service account: %w", err)
	}
	// ClusterRole：规则一致即跳过；漂移即更新（admin 自举面）。
	want := desiredClusterRole()
	cur, err := boot.RbacV1().ClusterRoles().Get(ctx, want.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := boot.RbacV1().ClusterRoles().Create(ctx, want, metav1.CreateOptions{}); err != nil {
			return "", fmt.Errorf("k3s rbac bootstrap: create cluster role (re-run once with an administrative kubeconfig to converge the role): %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("k3s rbac bootstrap: get cluster role: %w", err)
	default:
		if !rulesEqual(cur.Rules, want.Rules) {
			cur.Rules = want.Rules
			if _, err := boot.RbacV1().ClusterRoles().Update(ctx, cur, metav1.UpdateOptions{}); err != nil {
				return "", fmt.Errorf("k3s rbac bootstrap: update cluster role (re-run once with an administrative kubeconfig to converge the role): %w", err)
			}
		}
	}
	// ClusterRoleBinding（roleRef 不可变——存在且主体不一致即重建）。
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rbacServiceAccount},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: rbacServiceAccount},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: rbacServiceAccount, Namespace: systemNamespace}},
	}
	needBinding := true
	if curB, err := boot.RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{}); err == nil {
		if reflect.DeepEqual(curB.RoleRef, binding.RoleRef) && reflect.DeepEqual(curB.Subjects, binding.Subjects) {
			needBinding = false
		} else if derr := boot.RbacV1().ClusterRoleBindings().Delete(ctx, binding.Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return "", fmt.Errorf("k3s rbac bootstrap: replace binding: %w", derr)
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("k3s rbac bootstrap: get binding: %w", err)
	}
	if needBinding {
		if _, err := boot.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", fmt.Errorf("k3s rbac bootstrap: create binding: %w", err)
		}
	}
	// 长期 token Secret（controller 填充 data.token；k3s 秒级）。Get-first
	// 同上；随后轮询等 controller 填 token。
	if _, err := boot.CoreV1().Secrets(systemNamespace).Get(ctx, rbacTokenSecret, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, cerr := boot.CoreV1().Secrets(systemNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: rbacTokenSecret,
				Annotations: map[string]string{
					corev1.ServiceAccountNameKey: rbacServiceAccount,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		}, metav1.CreateOptions{}); cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return "", fmt.Errorf("k3s rbac bootstrap: token secret: %w", cerr)
		}
	} else if err != nil {
		return "", fmt.Errorf("k3s rbac bootstrap: token secret: %w", err)
	}
	deadline := time.Now().Add(tokenWait)
	for {
		sec, err := boot.CoreV1().Secrets(systemNamespace).Get(ctx, rbacTokenSecret, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("k3s rbac bootstrap: read token: %w", err)
		}
		if tok := string(sec.Data["token"]); tok != "" {
			return tok, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("k3s rbac bootstrap: service account token not populated within %s (token controller lag)", tokenWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// rulesEqual 比较 PolicyRule 集（归一化：逐规则内部排序后整体比对——
// 顺序与组内词序不参与语义）。
func rulesEqual(a, b []rbacv1.PolicyRule) bool {
	if len(a) != len(b) {
		return false
	}
	norm := func(rs []rbacv1.PolicyRule) []rbacv1.PolicyRule {
		out := make([]rbacv1.PolicyRule, len(rs))
		copy(out, rs)
		for i := range out {
			sort.Strings(out[i].APIGroups)
			sort.Strings(out[i].Resources)
			sort.Strings(out[i].Verbs)
		}
		sort.Slice(out, func(i, j int) bool {
			k := func(r rbacv1.PolicyRule) string {
				return fmt.Sprintf("%v/%v=%v", r.APIGroups, r.Resources, r.Verbs)
			}
			return k(out[i]) < k(out[j])
		})
		return out
	}
	return reflect.DeepEqual(norm(a), norm(b))
}
