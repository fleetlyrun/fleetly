package k3s

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// StreamLogs 实现 RuntimeLogs 子面：pod logs API（tail/since/until/容器过
// 滤/文本匹配；Follow 持续跟随）。集群聚合 = 域 label 选 pod 逐个读流合流
// （swarm 集群面 ServiceLogs 同源语义，ADR-0040 发现 A）。
func (p *Provider) StreamLogs(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	nsName := namespaceName(q.Namespace)
	sel := labels.Set(nsSelector(q.Namespace)).AsSelector()
	if len(q.WorkloadIDs) > 0 {
		// 多 Workload 过滤 = In 选择器（ADR-0060；workload id 全局唯一，
		// 单选择器替换域选择器与单值形态同语义）。
		in, err := labels.NewRequirement(labelWorkload, selection.In, q.WorkloadIDs)
		if err != nil {
			return fmt.Errorf("k3s logs: %w", err)
		}
		sel = labels.NewSelector().Add(*in)
	}
	pods, err := p.cli.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return fmt.Errorf("k3s logs: %w", err)
	}
	names := make([]string, 0, len(pods.Items))
	workloadByPod := make(map[string]string, len(pods.Items))
	nodeByPod := make(map[string]string, len(pods.Items))
	for _, pod := range pods.Items {
		if len(q.Containers) > 0 && !slices.Contains(q.Containers, pod.Name) {
			continue // 实例锚（ADR-0067）：pod 名精确匹配（日志面锚定身份，
			// 不筛相位——刚退出的副本尾部日志仍可读）
		}
		names = append(names, pod.Name)
		workloadByPod[pod.Name] = pod.Labels[labelWorkload]
		// 节点归因还原（R06-M4）：pod 落点是 k8s 节点名，平台节点 ID 经
		// 锚定缓存还原（Watch 事件面 platformNodeID 同源）；未锚定 = 空
		// （锚定扫描下一拍补齐，诚实边界）。
		nodeByPod[pod.Name] = p.platformNodeID(pod.Spec.NodeName)
	}
	sort.Strings(names)
	if len(names) == 0 {
		if q.Follow {
			<-ctx.Done() // 跟随形态：域空则挂到取消（消费端语义一致）
		}
		return nil
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(names))
	for _, name := range names {
		wg.Add(1)
		go func(podName string) {
			defer wg.Done()
			if err := p.pumpPodLogs(ctx, nsName, podName, workloadByPod[podName], nodeByPod[podName], q, w); err != nil {
				errCh <- err
			}
		}(name)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pumpPodLogs 读取单个 pod 的日志流并转写为帧（容器名/时间戳/k8s 合流
// 日志无 stdout/stderr 之分——诚实面：Line 即容器原始输出行）。
// pumpPodLogs 的 workloadID 从 pod 标记实取（域内全部形态下逐帧归属，
// 不再退化空串——ADR-0060）；筛选集由 StreamLogs 的选择器执法。
//
// Timestamps 恒开（R06-M4）：帧时间必须来自容器侧日志行（swarm 面
// Timestamps:true 同源语义），否则补读历史行会全部挤到采集时刻——VL 的
// _time 时间线失真、采集环游标（以帧时间推进）错位。
func (p *Provider) pumpPodLogs(ctx context.Context, nsName, podName, workloadID, node string, q capability.LogQuery, w capability.LogWriter) error {
	opts := &corev1.PodLogOptions{Follow: q.Follow, Timestamps: true}
	if q.TailLines > 0 {
		t := q.TailLines
		opts.TailLines = &t
	}
	if !q.Since.IsZero() {
		s := metav1.NewTime(q.Since)
		opts.SinceTime = &s
	}
	// Until：PodLogOptions 无对应原语（k8s 只有 since）——按帧时间在客户端
	// 截断（R06-M4：API 受理 but silently ignore 是旧缺陷，不接受静默）。
	stream, err := p.cli.CoreV1().Pods(nsName).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("k3s logs %s: %w", podName, err)
	}
	defer func() { _ = stream.Close() }() //nolint:errcheck // 只读流收尾，错误无处置面（swarm 同款口径）
	r := bufio.NewReader(stream)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			frame, ts, parsed := parsePodLogLine(line, workloadID, podName, node)
			if parsed && !q.Until.IsZero() && !ts.Before(q.Until) {
				// 窗口越界即截断：容器日志按时间递增写出（容器运行时
				// 追加日志），后续行只会更晚——不必读完整个流。
				return nil
			}
			if q.Text == "" || strings.Contains(string(frame.Line), q.Text) {
				frame.Team = q.Namespace.Team
				frame.Project = q.Namespace.Project
				frame.Kind = capability.LogKindRuntime
				if werr := w.WriteLog(ctx, frame); werr != nil {
					return werr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("k3s logs %s: %w", podName, err)
		}
	}
}

// parsePodLogLine 解析 Timestamps 形态的 pod 日志行（"<RFC3339Nano> <msg>"）。
// 返回帧、行时间与"时间戳是否解析成功"——解析失败时帧时间回落读取时刻
// （丢时间优于丢行：诚实退化），且调用方不做 Until 截断（没有行时间事实，
// 拿墙钟当行时间会误杀整段历史）。
//
// Line 恒拷贝出输入切片：bufio 的行缓冲可能复用，帧经 writer 异步消费。
func parsePodLogLine(line []byte, workloadID, podName, node string) (capability.LogFrame, time.Time, bool) {
	trimmed := bytes.TrimRight(line, "\r\n")
	frame := capability.LogFrame{
		WorkloadID: workloadID,
		Container:  podName,
		Node:       node,
		Time:       time.Now().UTC(),
		Line:       bytes.Clone(trimmed),
	}
	sep := bytes.IndexByte(trimmed, ' ')
	if sep <= 0 {
		return frame, time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, string(trimmed[:sep]))
	if err != nil {
		return frame, time.Time{}, false
	}
	frame.Time = ts.UTC()
	frame.Line = bytes.Clone(trimmed[sep+1:])
	return frame, ts, true
}
