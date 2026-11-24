package k3s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// controller-wins 收敛回归群（k3s 面；swarm 面见 swarm/runtime_converge_test.go）：
// ADR-0082 后果段承诺「人工改载体 / 整删载体……最迟 60s+一拍内收敛」。三者
// 的共同前提是**账本温热**（Provider 进程存活期间已下发过同内容）——账本
// 说「这个期望我下发过」不等于「服务端对象现在还是我留下的样子」。

const convergeDeployment = "fleetly-web-api-01jweb0000000000000000we"

func convergeWorkload() capability.Workload {
	return capability.Workload{ID: "w1", Process: "api", Image: "nginx:1.27", Replicas: 1}
}

// TestEnsureConvergesExternalScaleDown：平台外 `kubectl scale deployment
// --replicas=0` 后，下一拍 Ensure 必须把副本数压回期望值。
func TestEnsureConvergesExternalScaleDown(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := convergeWorkload()

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	steady, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	assert.Empty(t, steady.Changed, "an untouched carrier must stay quiet")

	// 平台外缩零：改 spec.replicas（夹具按 API server 语义推进
	// metadata.generation）。
	d, err := cli.AppsV1().Deployments(nsName).Get(ctx, convergeDeployment, metav1.GetOptions{})
	require.NoError(t, err)
	zero := int32(0)
	d.Spec.Replicas = &zero
	_, err = cli.AppsV1().Deployments(nsName).Update(ctx, d, metav1.UpdateOptions{})
	require.NoError(t, err)

	converged, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	d, err = cli.AppsV1().Deployments(nsName).Get(ctx, convergeDeployment, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, d.Spec.Replicas)
	assert.EqualValues(t, 1, *d.Spec.Replicas, "an externally scaled-down carrier must be pushed back on the very next beat")
	assert.Equal(t, "1", d.Spec.Template.Labels[labelGeneration], "the convergence write keeps the pod template stable (no pod-template change)")
	assert.Equal(t, []string{w.ID}, converged.Changed, "the convergence write is a real write (the engine's converge audit rides on it)")
}

// TestEnsureRecreatesExternalDeletedCarrier：平台外 `kubectl delete
// deployment` 后，下一拍 Ensure 必须重建载体。
func TestEnsureRecreatesExternalDeletedCarrier(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := convergeWorkload()

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	_, err = p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	err = cli.AppsV1().Deployments(nsName).Delete(ctx, convergeDeployment, metav1.DeleteOptions{})
	require.NoError(t, err)

	converged, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	_, gerr := cli.AppsV1().Deployments(nsName).Get(ctx, convergeDeployment, metav1.GetOptions{})
	assert.NoError(t, gerr, "an externally deleted carrier must be recreated on the very next beat")
	assert.Contains(t, converged.Changed, w.ID, "the recreation is a real write")
}

// TestEnsureRecreatesAfterConvergenceRemoval：域内收敛删掉载体后（期望集
// 不含它），再次期望同名同内容时必须重建——收敛删除须把账本清干净，否则
// 账本的「同内容已下发」会把重建判成跳过。
func TestEnsureRecreatesAfterConvergenceRemoval(t *testing.T) {
	p, cli := newFakeProvider()
	ctx := context.Background()
	nsName := "fleetly-shop"
	w := convergeWorkload()

	_, err := p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)

	// 期望集清空：域内收敛删除载体。
	removed, err := p.Ensure(ctx, appNS(), nil, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	assert.Contains(t, removed.Removed, "deployment/"+convergeDeployment)
	_, gerr := cli.AppsV1().Deployments(nsName).Get(ctx, convergeDeployment, metav1.GetOptions{})
	require.Error(t, gerr, "the carrier must be gone after convergence removal")

	// 再次期望同内容。
	_, err = p.Ensure(ctx, appNS(), []capability.Workload{w}, capability.Generation(1), capability.Materials{})
	require.NoError(t, err)
	_, gerr = cli.AppsV1().Deployments(nsName).Get(ctx, convergeDeployment, metav1.GetOptions{})
	assert.NoError(t, gerr, "a removed carrier must be recreated when desired again with the same content")
}
