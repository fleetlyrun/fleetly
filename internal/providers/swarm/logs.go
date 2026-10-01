package swarm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// StreamLogs 实现 RuntimeLogs 子面：按隔离域/Workload 过滤容器、流式
// 读日志（Follow 持续跟随）。容器发现走标签选择器（平台 ID 标记锚定，
// 不解析载体命名）。
//
// 多容器合流（Q-4/P1-15）：每容器一个读 goroutine，帧 fan-in 到单一
// 写出点——Follow 与非 Follow 统一走同一路径（非 Follow 各流 EOF 后
// 收尾；Follow 各流持续至 ctx 取消）。LogFrame 自带时间戳，容器间交错
// 是可接受的输出形态。旧实现串行逐容器跟随：首个容器的流永不 EOF，
// 其余容器只见于列表、永不输出。
func (p *Provider) StreamLogs(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	containers, err := p.listLogContainers(ctx, q.Namespace)
	if err != nil {
		return fmt.Errorf("swarm logs %s: %w", q.Namespace, err)
	}
	targets := make([]container.Summary, 0, len(containers))
	for _, c := range containers {
		if q.WorkloadID != "" && workloadIDOfContainer(c) != q.WorkloadID {
			continue
		}
		targets = append(targets, c)
	}
	if len(targets) == 0 {
		return nil
	}

	// sctx 贯穿全部读端与帧发送：任一读端失败或写端失败即取消，让
	// 其余 goroutine 干净退出（无泄漏、无孤儿帧）。
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	frames := make(chan capability.LogFrame)
	var (
		readers  sync.WaitGroup // 读端 goroutine 集
		mu       sync.Mutex     // firstErr 的写保护
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
		// 单容器读失败：取消其余读端。Follow 流不会自发 EOF，不取消
		// 会让 StreamLogs 挂死；错误如实上抛而非静默截断。
		cancel()
	}
	for _, c := range targets {
		readers.Add(1)
		go func(c container.Summary) {
			defer readers.Done()
			if err := p.pumpContainerLogs(sctx, c, q, frames); err != nil {
				fail(err)
			}
		}(c)
	}
	go func() {
		readers.Wait()
		close(frames)
	}()

	for frame := range frames {
		if err := w.WriteLog(ctx, frame); err != nil {
			// 写端失败：取消读端后排空 frames 至关闭（读端可能仍阻塞在
			// 发送上，不排干会泄漏 goroutine），随后返回写端错误。
			cancel()
			for range frames {
			}
			return err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return firstErr
}

// pumpContainerLogs 读单容器日志流，帧发往 frames（合流路径的读端单元；
// 返回 nil = EOF/ctx 取消的正常收口，非 nil = 读端错误）。
// tail/时间窗交给 dockerd 裁剪（ContainerLogs 同参语义；Since/Until 取
// RFC3339 文本形态，零值 = 不限）。
func (p *Provider) pumpContainerLogs(ctx context.Context, c container.Summary, q capability.LogQuery, frames chan<- capability.LogFrame) error {
	opts := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		// Follow 透传调用方（N0.1 P2-12：此前硬编码 true——真实 swarm 下
		// 不带 --follow 的 `fleetly logs` 会挂住到 ctx 超时）。
		Follow:     q.Follow,
		Timestamps: true,
	}
	if q.TailLines > 0 {
		opts.Tail = strconv.FormatInt(q.TailLines, 10)
	}
	if !q.Since.IsZero() {
		opts.Since = q.Since.Format(time.RFC3339Nano)
	}
	if !q.Until.IsZero() {
		opts.Until = q.Until.Format(time.RFC3339Nano)
	}
	res, err := p.openContainerLogStream(ctx, c.ID, opts)
	if err != nil {
		return fmt.Errorf("container logs %s: %w", c.ID, err)
	}
	defer func() { _ = res.Close() }()

	// docker 日志流为 8 字节 stdcopy 头多路复用；bufio 按行切帧。
	workloadID := workloadIDOfContainer(c)
	node := nodeIDOfContainer(c)
	r := bufio.NewReader(res)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			ts, body := splitDockerTimestamp(line)
			frame := capability.LogFrame{
				WorkloadID: workloadID,
				Container:  c.ID,
				Node:       node,
				Time:       ts,
				Line:       bytes.TrimRight(body, "\r\n"),
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return nil // 合流已收口（读端失败/写端失败/调用方取消）
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if ctx.Err() != nil {
				return nil // 跟随模式随 ctx 取消收口
			}
			return err
		}
	}
}

// listLogContainers 列出隔离域内容器（测试缝优先；含 Exited——最近缓冲
// 口径，F0.25 诚实标注"仅实时+最近缓冲"）。
func (p *Provider) listLogContainers(ctx context.Context, ns capability.NamespaceRef) ([]container.Summary, error) {
	if p.listContainers != nil {
		return p.listContainers(ctx, ns)
	}
	return p.listNsContainers(ctx, ns)
}

// openContainerLogStream 打开单容器日志流（测试缝优先）。缝契约：读端
// 在 ctx 取消时必须解除阻塞（真实现由请求 ctx 取消响应体保证）。
func (p *Provider) openContainerLogStream(ctx context.Context, containerID string, opts client.ContainerLogsOptions) (io.ReadCloser, error) {
	if p.openContainerLog != nil {
		return p.openContainerLog(ctx, containerID, opts)
	}
	return p.cli.ContainerLogs(ctx, containerID, opts)
}

// listNsContainers 列出隔离域内容器（docker 直连形态）。
func (p *Provider) listNsContainers(ctx context.Context, ns capability.NamespaceRef) ([]container.Summary, error) {
	filters := client.Filters{}.Add("label", labelSelectorArgs(ns)...)
	res, err := p.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// splitDockerTimestamp 拆出 RFC3339 前缀时间戳（docker timestamps 选项）。
func splitDockerTimestamp(line []byte) (time.Time, []byte) {
	if i := bytes.IndexByte(line, ' '); i > 0 {
		if ts, err := time.Parse(time.RFC3339Nano, string(line[:i])); err == nil {
			return ts, line[i+1:]
		}
	}
	return time.Time{}, line
}

// workloadIDOfContainer 从容器标记还原平台 Workload ID。
func workloadIDOfContainer(c container.Summary) string {
	if c.Labels == nil {
		return ""
	}
	return c.Labels[labelWorkload]
}

// nodeIDOfContainer 从容器标记还原平台节点 ID（swarm 调度标记搬运）。
func nodeIDOfContainer(c container.Summary) string {
	if c.Labels == nil {
		return ""
	}
	return c.Labels["com.docker.swarm.node.id"]
}
