package swarm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// StreamLogs 实现 RuntimeLogs 子面：按隔离域/Workload 过滤容器、流式
// 读日志（Follow 持续跟随）。容器发现走标签选择器（平台 ID 标记锚定，
// 不解析载体命名）。
func (p *Provider) StreamLogs(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	containers, err := p.listNsContainers(ctx, q.Namespace)
	if err != nil {
		return fmt.Errorf("swarm logs %s: %w", q.Namespace, err)
	}
	for _, c := range containers {
		if q.WorkloadID != "" && workloadIDOfContainer(c) != q.WorkloadID {
			continue
		}
		if err := p.streamContainerLogs(ctx, c, q, w); err != nil {
			return err
		}
	}
	return nil
}

// listNsContainers 列出隔离域内容器（含 Exited——最近缓冲口径，F0.25
// 诚实标注"仅实时+最近缓冲"）。
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

// streamContainerLogs 流式读取单容器日志并翻译为 LogFrame。tail/时间窗
// 交给 dockerd 裁剪（ContainerLogs 同参语义；Since/Until 取 RFC3339 文本
// 形态，零值 = 不限）。
func (p *Provider) streamContainerLogs(ctx context.Context, c container.Summary, q capability.LogQuery, w capability.LogWriter) error {
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
	res, err := p.cli.ContainerLogs(ctx, c.ID, opts)
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
			if werr := w.WriteLog(ctx, frame); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if ctx.Err() != nil {
				return nil // 跟随模式随 ctx 取消收口
			}
			return err
		}
	}
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
