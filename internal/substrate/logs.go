package substrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 服务日志流（T2.20 日志管线的底座面）：docker service logs 的 moby/client
// 适配。第三方流格式（stdcopy 多路复用帧、Docker 时间戳头部）只存在于本
// 包内部，出口一律核心类型 LogLine——架构 §2.8 核心不出现第三方概念。

// LogLine 是服务日志单行（核心类型）。
type LogLine struct {
	// At 是 Docker 侧记录时间（timestamps=true 头部解析；解析失败取零值，
	// 调用方可回退本机时钟）。
	At time.Time
	// Stderr 报告该行来自 stderr 多路复用通道。
	Stderr bool
	// Line 是剥离时间戳头部后的原始内容（尾部换行已去除）。
	Line string
}

// ManagedServiceProcesses 返回指定应用的受管服务 compose 进程名集
// （label 过滤 fleetly.managed=true + fleetly.app=<app>，取
// fleetly.process label；字典序稳定）。日志采集的 per-app 服务发现入口
// （T2.20）；平台外同名服务不进结果。
func (c *Client) ManagedServiceProcesses(ctx context.Context, app string) ([]string, error) {
	rows, err := c.ServiceList(ctx, map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     app,
	})
	if err != nil {
		return nil, fmt.Errorf("substrate: service list for app %s: %w", app, err)
	}
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		name := r.Labels[state.LabelProcess]
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// StreamServiceLogs 打开 Swarm 服务日志流（service 传 Swarm 服务名，
// fleetly 命名 = naming.ServiceName(app, service)）。follow=false 时读至
// 流自然结束（配合 since 做轮询拉取）；follow=true 持续跟随。since 非零
// 时只取该时刻之后的行。返回 channel 在流结束、出错或 ctx 取消时关闭
// （错误语义：日志流尽力而为，故障由采集器下轮重试，不单独暴露错误通道）。
// 服务不存在 / 底座不可达按 state 端口哨兵归类（mapSubstrateErr）。
func (c *Client) StreamServiceLogs(ctx context.Context, service string, since time.Time, follow bool) (<-chan LogLine, error) {
	opts := mobyclient.ServiceLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     follow,
	}
	if !since.IsZero() {
		// Docker API 接受 RFC3339Nano；+1ns 防轮询边界重复（上一轮末行
		// 已入账）。
		opts.Since = since.Add(time.Nanosecond).Format(time.RFC3339Nano)
	}
	res, err := c.cli.ServiceLogs(ctx, service, opts)
	if err != nil {
		return nil, mapSubstrateErr(fmt.Errorf("substrate: service logs %s: %w", service, err))
	}
	out := make(chan LogLine)
	go func() {
		defer close(out)
		defer func() { _ = res.Close() }()
		streamLogs(ctx, res, out)
	}()
	return out, nil
}

// streamLogs 解多路复用帧并逐行投递（stdout/stderr 两路 bufio 扫描，
// StdCopy 按帧头分发到对应 pipe）。
func streamLogs(ctx context.Context, src io.Reader, out chan<- LogLine) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	defer func() { _ = stdoutR.Close() }()
	defer func() { _ = stderrR.Close() }()

	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = stdcopy.StdCopy(stdoutW, stderrW, src)
		_ = stdoutW.Close()
		_ = stderrW.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(ctx, stdoutR, false, out)
	}()
	go func() {
		defer wg.Done()
		scanLines(ctx, stderrR, true, out)
	}()
	wg.Wait()
	<-copyDone
}

// scanLines 逐行剥离 Docker 时间戳头部并投递；ctx 取消即停止读（管道
// 随 stdcopy goroutine 结束而关闭）。
func scanLines(ctx context.Context, r io.Reader, stderr bool, out chan<- LogLine) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 单行上限 1MiB（超长行报错终结本轮，下轮重试）
	for sc.Scan() {
		line := sc.Text()
		at, rest := splitTimestamp(line)
		select {
		case <-ctx.Done():
			return
		case out <- LogLine{At: at, Stderr: stderr, Line: rest}:
		}
	}
}

// splitTimestamp 剥离 Docker timestamps=true 头部（`<RFC3339Nano> <rest>`）。
func splitTimestamp(line string) (time.Time, string) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return time.Time{}, line
	}
	if sec, err := strconv.ParseInt(line[:idx], 10, 64); err == nil && sec > 1_000_000_000 {
		// unix 秒形态（防御；Timestamps=true 恒 RFC3339Nano）。
		return time.Unix(sec, 0).UTC(), line[idx+1:]
	}
	if at, err := time.Parse(time.RFC3339Nano, line[:idx]); err == nil {
		return at.UTC(), line[idx+1:]
	}
	return time.Time{}, line
}
