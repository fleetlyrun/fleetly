package k3s

// 网络载体的收敛单测（R06-M6）：egress deny NetworkPolicy 的 create-only
// 路径升级为 create-or-update 单源（putNetpol）——存量旧形状 policy 在平台
// 升级后必须收敛到当前期望形态，而非"AlreadyExists 即收敛完成"永久锁死。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TestReconcileEgressNetpolConvergesShapeDrift（R06-M6 回归锚）：预置旧形状
// 的 managed egress policy（放行集缺"同 Namespace 全通"——升级前的历史
// 形态），Ensure 必须把 spec 收敛到 toEgressNetpol 的当前形态；第二次
// Ensure（形状已等）零写（幂等重放不产生 update）。
func TestReconcileEgressNetpolConvergesShapeDrift(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"

	// 旧形状：同名同 managed 标签，但放行集只剩 DNS 一条（legacy 形态）。
	stale := toEgressNetpol()
	require.Len(t, stale.Spec.Egress, 2)
	stale.Spec.Egress = stale.Spec.Egress[1:]
	_, err := cli.NetworkingV1().NetworkPolicies(nsName).Create(ctx, stale, metav1.CreateOptions{})
	require.NoError(t, err)

	ws := []capability.Workload{{
		ID: "w1", Process: "api", Image: "nginx:1.27",
		EgressNetworks: []string{"isolated"},
	}}
	require.NoError(t, ensureErr(p, ctx, appNS(), ws, 1, capability.Materials{}))

	pol, err := cli.NetworkingV1().NetworkPolicies(nsName).Get(ctx, toEgressNetpol().Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, toEgressNetpol().Spec, pol.Spec,
		"a stale egress policy must converge to the current shape (create-only locks the old shape forever)")
	assert.Equal(t, "true", pol.Labels[labelManaged], "managed label is part of the converged shape")

	// 幂等重放：形状相等时零写（第二拍只允许读，不产生 update）。
	updates := func() int {
		n := 0
		for _, a := range cli.Actions() {
			if a.GetVerb() == "update" && a.GetResource().Resource == "networkpolicies" {
				n++
			}
		}
		return n
	}
	require.Equal(t, 1, updates(), "the drift pass itself is exactly one update")
	require.NoError(t, ensureErr(p, ctx, appNS(), ws, 2, capability.Materials{}))
	assert.Equal(t, 1, updates(), "equal shape must not be rewritten (idempotent replay)")
}
