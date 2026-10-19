package k3s

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// installPodLabelFilter 给 fake clientset 补 label 选择器语义（fake tracker
// 的 List/DeleteCollection 都不消费 label selector——不补则命中数/删除面
// 不可信）：List 按 selector 过滤，DeleteCollection 删除匹配集。
func installPodLabelFilter(t *testing.T, cli *fake.Clientset) {
	t.Helper()
	gvr := corev1.SchemeGroupVersion.WithResource("pods")
	gvk := corev1.SchemeGroupVersion.WithKind("Pod")

	cli.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		la, ok := action.(k8stesting.ListActionImpl)
		if !ok {
			return false, nil, nil
		}
		sel, err := labels.Parse(la.GetListRestrictions().Labels.String())
		if err != nil {
			return true, nil, err
		}
		all, err := cli.Tracker().List(gvr, gvk, la.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		out := &corev1.PodList{}
		for _, pod := range all.(*corev1.PodList).Items {
			if sel.Matches(labels.Set(pod.Labels)) {
				out.Items = append(out.Items, pod)
			}
		}
		return true, out, nil
	})
	cli.PrependReactor("delete-collection", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		dca, ok := action.(k8stesting.DeleteCollectionActionImpl)
		if !ok {
			return false, nil, nil
		}
		sel, err := labels.Parse(dca.GetListRestrictions().Labels.String())
		if err != nil {
			return true, nil, err
		}
		all, err := cli.Tracker().List(gvr, gvk, dca.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		for _, pod := range all.(*corev1.PodList).Items {
			if !sel.Matches(labels.Set(pod.Labels)) {
				continue
			}
			if err := cli.Tracker().Delete(gvr, dca.GetNamespace(), pod.Name); err != nil {
				return true, nil, err
			}
		}
		return true, nil, nil
	})
}

// restartPod 造一个带平台 workload 标签的载体 pod（namespace = app 域）。
func restartPod(carrierID, name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: namespaceName(appNS()),
		Labels: map[string]string{
			labelManaged:  "true",
			labelWorkload: carrierID,
		},
	}}
}

// R02-M3：按代次化载体 ID 驱逐恰一个代次的 pod——旧代/裸 ID 的 pod 零触碰；
// 零命中 = 未找到（state.ErrNotFound，不再静默假成功）。
func TestRestartDeletesGenerationScopedCarrier(t *testing.T) {
	p, cli := newFakeProvider(
		restartPod("app1-web-g2", "app1-web-g2-0"),
		restartPod("app1-web-g2", "app1-web-g2-1"),
		restartPod("app1-web-g1", "app1-web-g1-0"),
		restartPod("app1-web", "app1-web-0"),
	)
	installPodLabelFilter(t, cli)
	nsName := namespaceName(appNS())

	require.NoError(t, p.Restart(context.Background(), appNS(), "app1-web-g2"))

	for _, name := range []string{"app1-web-g2-0", "app1-web-g2-1"} {
		_, err := cli.CoreV1().Pods(nsName).Get(context.Background(), name, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "target-generation pod %s must be evicted, got %v", name, err)
	}
	for _, name := range []string{"app1-web-g1-0", "app1-web-0"} {
		_, err := cli.CoreV1().Pods(nsName).Get(context.Background(), name, metav1.GetOptions{})
		assert.NoError(t, err, "other carrier ids must not be evicted")
	}
}

func TestRestartMissingCarrierIsNotFound(t *testing.T) {
	p, cli := newFakeProvider(restartPod("app1-web-g1", "app1-web-g1-0"))
	installPodLabelFilter(t, cli)

	err := p.Restart(context.Background(), appNS(), "app1-web-g9")
	require.Error(t, err)
	assert.True(t, errors.Is(err, capability.ErrRestartTargetNotFound), "a zero-hit restart must wrap state.ErrNotFound, got %v", err)

	// 既有载体未被误删。
	_, gerr := cli.CoreV1().Pods(namespaceName(appNS())).Get(context.Background(), "app1-web-g1-0", metav1.GetOptions{})
	assert.NoError(t, gerr)
}

// 编译期锚：Restart 的载体定位面仍是 RuntimeRestart 契约。
var _ capability.RuntimeRestart = (*Provider)(nil)
