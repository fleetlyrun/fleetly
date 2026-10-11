package k3s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"

	"github.com/fleetlyrun/fleetly/internal/capability"
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
	for _, pod := range pods.Items {
		names = append(names, pod.Name)
		workloadByPod[pod.Name] = pod.Labels[labelWorkload]
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
			if err := p.pumpPodLogs(ctx, nsName, podName, workloadByPod[podName], q, w); err != nil {
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
func (p *Provider) pumpPodLogs(ctx context.Context, nsName, podName, workloadID string, q capability.LogQuery, w capability.LogWriter) error {
	opts := &corev1.PodLogOptions{Follow: q.Follow}
	if q.TailLines > 0 {
		t := q.TailLines
		opts.TailLines = &t
	}
	if !q.Since.IsZero() {
		s := metav1.NewTime(q.Since)
		opts.SinceTime = &s
	}
	stream, err := p.cli.CoreV1().Pods(nsName).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("k3s logs %s: %w", podName, err)
	}
	defer func() { _ = stream.Close() }() //nolint:errcheck // 只读流收尾，错误无处置面（swarm 同款口径）
	r := bufio.NewReader(stream)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(string(line), "\n")
			if q.Text == "" || strings.Contains(trimmed, q.Text) {
				frame := capability.LogFrame{
					WorkloadID: workloadID,
					Container:  podName,
					Node:       "",
					Time:       time.Now().UTC(),
					Line:       []byte(trimmed),
					Team:       q.Namespace.Team,
					Project:    q.Namespace.Project,
					Kind:       capability.LogKindRuntime,
				}
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
