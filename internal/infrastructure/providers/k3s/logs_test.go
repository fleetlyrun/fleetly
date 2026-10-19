package k3s

// k3s 日志帧等价性单测（R06-M4）：PodLogOptions 开 Timestamps 并解析
// RFC3339 前缀还原帧时间（VL 的 _time 不再等于采集墙钟）、pod 落点经锚定
// 缓存还原平台节点 ID（fleetly_node 归因）、Until 窗口按帧时间客户端截断
// （PodLogOptions 无 until 原语——显式不静默忽略）。帧字段与 swarm 面按
// capability.LogFrame 契约对齐。
//
// fake clientset 的 pods/log 子资源经 reactor 注入日志体：真 fake 只回
// "fake logs"，reactor 返回 *runtime.Unknown（client-go 约定的日志面形态），
// 同时截获 PodLogOptions 做参数面断言。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// logsRecorder 是 LogWriter 的收集端（流结束后逐帧断言）。
type logsRecorder struct {
	frames []capability.LogFrame
}

func (r *logsRecorder) WriteLog(_ context.Context, frame capability.LogFrame) error {
	r.frames = append(r.frames, frame)
	return nil
}

// seedLogPod 播一个日志发现面的 managed pod（ns 选择器 + workload 标记 +
// 落点节点名；appNS() 对应的 team/project/app 三轴标签齐全）。
func seedLogPod(t *testing.T, p *Provider, podName, nodeName, workloadID string) {
	t.Helper()
	_, err := p.cli.CoreV1().Pods("fleetly-shop").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Labels: map[string]string{
			labelManaged:    "true",
			labelTeam:       sanitizeNamePart("acme"),
			labelProject:    sanitizeNamePart("shop"),
			labelApp:        sanitizeNamePart("web"),
			labelWorkload:   workloadID,
			labelGeneration: "5",
		}},
		Spec: corev1.PodSpec{NodeName: nodeName},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// injectPodLogs 让 pods/log 子资源返回固定日志体，并记录最近一次
// PodLogOptions（Timestamps/SinceTime 参数面断言）。
func injectPodLogs(t *testing.T, cli *fake.Clientset, body string) **corev1.PodLogOptions {
	t.Helper()
	captured := new(*corev1.PodLogOptions)
	cli.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "log" {
			return false, nil, nil
		}
		*captured = action.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		return true, &runtime.Unknown{Raw: []byte(body)}, nil
	})
	return captured
}

// TestStreamLogsTimestampsAndNode（R06-M4）：帧时间来自日志行内的容器时间
// 戳（不是采集墙钟），Node 是平台节点 ID（锚定缓存还原），Container 是
// pod 名（实例锚词汇）。
func TestStreamLogsTimestampsAndNode(t *testing.T) {
	p, cli := newFakeProvider()
	p.nodeIDs = map[string]string{"node-a": "01NODEANCHOR00000000000000"}
	seedLogPod(t, p, "pod-web", "node-a", "w1")
	opts := injectPodLogs(t, cli,
		"2026-10-19T10:00:00.123456789Z line one\n"+
			"2026-10-19T10:00:01Z line two\n")

	rec := &logsRecorder{}
	q := capability.LogQuery{
		Namespace:   appNS(),
		WorkloadIDs: []string{"w1"},
		Since:       time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC),
	}
	require.NoError(t, p.StreamLogs(context.Background(), q, rec))

	require.NotNil(t, *opts, "pod log options must reach the logs API")
	assert.True(t, (*opts).Timestamps, "Timestamps must be on: frame time has to come from the container clock")
	require.NotNil(t, (*opts).SinceTime, "Since must map to SinceTime")
	assert.True(t, (*opts).SinceTime.Time.Equal(q.Since))

	require.Len(t, rec.frames, 2)
	first := rec.frames[0]
	assert.Equal(t, "w1", first.WorkloadID)
	assert.Equal(t, "pod-web", first.Container, "Container is the instance anchor (pod name)")
	assert.Equal(t, "01NODEANCHOR00000000000000", first.Node, "Node must be restored to the platform node ID")
	assert.True(t, first.Time.Equal(time.Date(2026, 10, 19, 10, 0, 0, 123456789, time.UTC)),
		"frame time must be the log line timestamp, got %s", first.Time)
	assert.Equal(t, []byte("line one"), first.Line, "timestamp prefix must be stripped from the payload line")
	assert.Equal(t, capability.LogKindRuntime, first.Kind)
	assert.Equal(t, "acme", first.Team)
	assert.Equal(t, "shop", first.Project)
	second := rec.frames[1]
	assert.True(t, second.Time.Equal(time.Date(2026, 10, 19, 10, 0, 1, 0, time.UTC)))
	assert.Equal(t, []byte("line two"), second.Line)
}

// TestStreamLogsUntilIsAppliedClientSide（R06-M4）：Until 不能被静默忽略
// （API 受理但 Provider 不实现是旧缺陷）——按帧时间截断到窗口内；窗口
// 下界语义与 docker logs --until 一致（严格早于 Until 的行保留）。
func TestStreamLogsUntilIsAppliedClientSide(t *testing.T) {
	p, cli := newFakeProvider()
	seedLogPod(t, p, "pod-web", "", "w1")
	injectPodLogs(t, cli,
		"2026-10-19T10:00:00Z before\n"+
			"2026-10-19T10:00:01Z boundary\n"+
			"2026-10-19T10:00:02Z after\n")

	rec := &logsRecorder{}
	q := capability.LogQuery{
		Namespace: appNS(),
		Until:     time.Date(2026, 10, 19, 10, 0, 1, 0, time.UTC),
	}
	require.NoError(t, p.StreamLogs(context.Background(), q, rec))
	require.Len(t, rec.frames, 1, "lines at/after until must not leak into the window")
	assert.Equal(t, []byte("before"), rec.frames[0].Line)

	// 窗口覆盖全量时不得误删（Until 在未来）。
	rec = &logsRecorder{}
	q.Until = time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC)
	require.NoError(t, p.StreamLogs(context.Background(), q, rec))
	assert.Len(t, rec.frames, 3)
}

// TestStreamLogsUnparseableLineFallsBack：时间戳不可解析的行仍要发出（丢
// 时间优于丢行——诚实退化），且无时间事实时不做 Until 截断（不拿墙钟当
// 行时间误杀）。
func TestStreamLogsUnparseableLineFallsBack(t *testing.T) {
	p, cli := newFakeProvider()
	seedLogPod(t, p, "pod-web", "node-a", "w1")
	injectPodLogs(t, cli, "no-timestamp-here\n")

	rec := &logsRecorder{}
	q := capability.LogQuery{
		Namespace: appNS(),
		Until:     time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	require.NoError(t, p.StreamLogs(context.Background(), q, rec))
	require.Len(t, rec.frames, 1)
	assert.Equal(t, []byte("no-timestamp-here"), rec.frames[0].Line)
	assert.False(t, rec.frames[0].Time.IsZero(), "fallback time keeps the frame usable")
	assert.Empty(t, rec.frames[0].Node, "unanchored node name stays empty until the anchor scan catches up")
}
