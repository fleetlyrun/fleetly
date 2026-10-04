package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// StreamLogs 实现 RuntimeLogs 子面：集群面服务日志流（ADR-0040 决策 2，
// 单机假设审计发现 A 的根治）。服务发现走 ServiceList（fleetly.ns 标签
// 全等，集群对象——节点拓扑透明），日志读取走 ServiceLogs（manager API
// 聚合**所有节点** task 日志，Details:true 携带 node/task 归因）。
//
// 多服务合流（Q-4/P1-15 形态保持）：每服务一个读 goroutine，帧 fan-in
// 到单一写出点——Follow 与非 Follow 统一走同一路径（非 Follow 各流 EOF
// 后收尾；Follow 各流持续至 ctx 取消）。容器间交错是可接受的输出形态。
// LogFrame.Container 语义 = swarm task ID（容器 ID 的集群稳定等价物，
// ADR-0040）；WorkloadID 从服务标记还原。
func (p *Provider) StreamLogs(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	services, err := p.listLogServices(ctx, q.Namespace)
	if err != nil {
		return fmt.Errorf("swarm logs %s: %w", q.Namespace, err)
	}
	targets := make([]swarm.Service, 0, len(services))
	for _, svc := range services {
		labels := svc.Spec.Labels
		if labels[labelWorkload] == "" {
			continue // 非平台 Workload 载体（防御：ns 标记在但 workload 缺席）
		}
		if q.WorkloadID != "" && labels[labelWorkload] != q.WorkloadID {
			continue
		}
		targets = append(targets, svc)
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
		// 单服务读失败：取消其余读端。Follow 流不会自发 EOF，不取消
		// 会让 StreamLogs 挂死；错误如实上抛而非静默截断。
		cancel()
	}
	for _, svc := range targets {
		readers.Add(1)
		go func(svc swarm.Service) {
			defer readers.Done()
			if err := p.pumpServiceLogs(sctx, svc, q, frames); err != nil {
				fail(err)
			}
		}(svc)
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

// pumpServiceLogs 读单服务日志流，帧发往 frames（合流路径的读端单元；
// 返回 nil = EOF/ctx 取消的正常收口，非 nil = 读端错误）。tail/时间窗
// 交给 dockerd 裁剪（ServiceLogs 同参语义；Since/Until 取 RFC3339 文本
// 形态，零值 = 不限）。
func (p *Provider) pumpServiceLogs(ctx context.Context, svc swarm.Service, q capability.LogQuery, frames chan<- capability.LogFrame) error {
	opts := client.ServiceLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		// Follow 透传调用方（N0.1 P2-12：此前硬编码 true——真实 swarm 下
		// 不带 --follow 的 `fleetly logs` 会挂住到 ctx 超时）。
		Follow: q.Follow,
		// Timestamps 恒开（帧时间戳是检索/游标面的锚）；Details 恒开
		//（node/task 归因是集群面日志帧的身份组成部分，ADR-0040）。
		Timestamps: true,
		Details:    true,
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
	res, err := p.openServiceLogStream(ctx, svc.ID, opts)
	if err != nil {
		return fmt.Errorf("service logs %s: %w", svc.ID, err)
	}
	defer func() { _ = res.Close() }()

	lw := &serviceLineWriter{ctx: ctx, frames: frames, workloadID: svc.Spec.Labels[labelWorkload]}
	if _, err := stdcopy.StdCopy(lw, lw, res); err != nil {
		if ctx.Err() != nil || errors.Is(err, io.EOF) {
			return nil // 合流收口（读端失败/写端失败/调用方取消）或流自然终点
		}
		return err
	}
	return nil
}

// listLogServices 列出隔离域内服务（测试缝优先；集群对象——与
// InspectWorkloads 的发现面同源）。
func (p *Provider) listLogServices(ctx context.Context, ns capability.NamespaceRef) ([]swarm.Service, error) {
	if p.listServices != nil {
		return p.listServices(ctx, ns)
	}
	return p.listNsServices(ctx, ns)
}

// openServiceLogStream 打开单服务日志流（测试缝优先）。缝契约：读端在
// ctx 取消时必须解除阻塞（真实现由请求 ctx 取消响应体保证）。
func (p *Provider) openServiceLogStream(ctx context.Context, serviceID string, opts client.ServiceLogsOptions) (io.ReadCloser, error) {
	if p.openServiceLog != nil {
		return p.openServiceLog(ctx, serviceID, opts)
	}
	return p.cli.ServiceLogs(ctx, serviceID, opts)
}

// serviceLineWriter 是 stdcopy 的行接收端：解析服务日志行形态后发帧。
// stdcopy 以整行调用 Write（docker/cli 同款契约假设；行内空格保留——
// SplitN 限三段，消息体不拆）。
type serviceLineWriter struct {
	ctx        context.Context
	frames     chan<- capability.LogFrame
	workloadID string
}

func (w *serviceLineWriter) Write(buf []byte) (int, error) {
	frame := parseServiceLogLine(w.workloadID, buf)
	select {
	case w.frames <- frame:
	case <-w.ctx.Done():
		return 0, w.ctx.Err() // 合流已收口（读端失败/写端失败/调用方取消）
	}
	return len(buf), nil
}

// swarm 日志行归因的 details 键（daemon write_log_stream 携带，docker/cli
// parseContext 同源词汇）。
const (
	detailNodeID    = "com.docker.swarm.node.id"
	detailServiceID = "com.docker.swarm.service.id"
	detailTaskID    = "com.docker.swarm.task.id"
)

// parseServiceLogLine 解析一行服务日志（Timestamps+Details 形态：
// "<RFC3339> <k=v,k=v> <message>"；details 键值均 URL query 转义）。
// 形态不符（无 details/时间戳不可解析）时按无归因原始行发帧——丢归因
// 优于丢行（诚实边界，ADR-0040）。
//
// Line 恒拷贝出输入切片：调用方（stdcopy）的行缓冲会被下一帧复用，帧经
// channel 异步消费——子切片别名会读到复用后的字节（bufio.ReadBytes 每行
// 新分配的旧实现无此面）。
func parseServiceLogLine(workloadID string, line []byte) capability.LogFrame {
	f := capability.LogFrame{WorkloadID: workloadID, Line: bytes.Clone(bytes.TrimRight(line, "\r\n"))}
	parts := bytes.SplitN(line, []byte(" "), 3)
	if len(parts) != 3 {
		return f
	}
	ts, err := time.Parse(time.RFC3339Nano, string(parts[0]))
	if err != nil {
		return f
	}
	attrs, err := parseLogDetails(string(parts[1]))
	if err != nil {
		return f
	}
	f.Time = ts
	f.Node = attrs[detailNodeID]
	f.Container = attrs[detailTaskID]
	f.Line = bytes.Clone(bytes.TrimRight(parts[2], "\r\n"))
	return f
}

// parseLogDetails 解析 docker 日志行属性（k=v 逗号连接、k/v 均 URL query
// 转义——daemon stringAttrs 编码；docker/cli internal/logdetails 同款，
// 自写避免内部包依赖）。
func parseLogDetails(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("malformed log details %q", s)
		}
		uk, err := url.QueryUnescape(k)
		if err != nil {
			return nil, err
		}
		uv, err := url.QueryUnescape(v)
		if err != nil {
			return nil, err
		}
		out[uk] = uv
	}
	return out, nil
}
