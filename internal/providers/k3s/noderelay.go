package k3s

// 节点中继的 k3s 集中形态（F3.2/ADR-0049 契约，ADR-0053 决策 1）：
// RunNodeRelay 枚举锚定节点，**每节点一条**回环连接拨控制面 gateway 的
// /v1/relay（hello.carrier_node_id = k8s 节点名）——engine 的 relays[平台
// 节点 ID] 路由模型零改动；会话帧经 apiserver exec 服务（ExecWorkload 的
// SPDY 通道，任意节点可达——apiserver 即中继）。与 swarm 形态的载体事实
// 不同（swarm 中继真在节点上跑 docker exec；k3s 连接全部在 manager 进程
// 内）、平台语义等价：relay_online = "该节点的 exec 可服务"。
//
// 节点生命周期：节拍列锚定节点（fleetly.node.id 在场）——新节点起连接
//（未锚定进平台表即退避重试，锚定后自愈）、消失节点停连接（连接关闭 →
// engine 侧 dropRelayConn 收口其名下会话）。凭证每次拨号前取 node token
// 文件（k3s 无 swarm rotate 面——Enrollment rotate 诚实失败，同口径）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// relay 重连节拍与帧上限（swarm 侧同源语义）。
const (
	relayReconnectMin = time.Second
	relayReconnectMax = 30 * time.Second
	relayNodePoll     = 10 * time.Second
)

// RunNodeRelay 实现 RuntimeExec：集中形态主循环（节点枚举 + per-node
// 连接生命周期）。阻塞直至 ctx 结束。
func (p *Provider) RunNodeRelay(ctx context.Context, o capability.NodeRelayOptions) error {
	if o.GatewayURL == "" {
		return errors.New("node relay: gateway url is required")
	}
	type nodeLoop struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	loops := map[string]*nodeLoop{}
	defer func() {
		for _, l := range loops {
			l.cancel()
		}
		for _, l := range loops {
			<-l.done
		}
	}()
	tick := time.NewTicker(relayNodePoll)
	defer tick.Stop()
	sweep := func() {
		nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			slog.Info("node relay: node list failed", "err", err.Error())
			return
		}
		want := map[string]bool{}
		for i := range nodes.Items {
			n := &nodes.Items[i]
			if n.Labels[labelNodeID] == "" {
				continue // 未锚定（锚定扫描未及）：下拍再看
			}
			want[n.Name] = true
		}
		for name := range want {
			if _, ok := loops[name]; ok {
				continue
			}
			nctx, cancel := context.WithCancel(ctx)
			l := &nodeLoop{cancel: cancel, done: make(chan struct{})}
			loops[name] = l
			go func(nodeName string, l *nodeLoop) {
				defer close(l.done)
				p.nodeRelayLoop(nctx, o, nodeName)
			}(name, l)
			slog.Info("node relay: node registered", "node", name)
		}
		for name, l := range loops {
			if want[name] {
				continue
			}
			delete(loops, name)
			l.cancel()
			<-l.done
			slog.Info("node relay: node deregistered", "node", name)
		}
	}
	sweep()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			sweep()
		}
	}
}

// nodeRelayLoop 单节点的重连环（连接级错误退避增长；ctx 结束即返）。
func (p *Provider) nodeRelayLoop(ctx context.Context, o capability.NodeRelayOptions, nodeName string) {
	backoff := relayReconnectMin
	for {
		err := p.nodeRelayOnce(ctx, o, nodeName)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Info("node relay connection ended", "node", nodeName, "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > relayReconnectMax {
			backoff = relayReconnectMax
		}
	}
}

// nodeRelayOnce 服务一条节点连接：拨号 → hello（载体节点身份）→ 会话帧
// 循环，直至错误或 ctx 结束（swarm relayOnce 同构——帧面单源
// capability，连接面各 Provider 私有）。
func (p *Provider) nodeRelayOnce(ctx context.Context, o capability.NodeRelayOptions, nodeName string) error {
	token := ""
	if o.JoinToken != nil {
		t, err := o.JoinToken(ctx)
		if err != nil {
			return fmt.Errorf("node relay: credential: %w", err)
		}
		token = t
	} else {
		t, err := p.readNodeToken()
		if err != nil {
			return fmt.Errorf("node relay: credential: %w", err)
		}
		token = t
	}

	hdr := http.Header{"Authorization": []string{"Bearer " + token}}
	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, dialResp, err := websocket.Dial(dialCtx, o.GatewayURL+"/v1/relay", &websocket.DialOptions{
		HTTPHeader: hdr, HTTPClient: relayHTTPClient(),
	})
	cancelDial()
	if err != nil {
		// 失败路径的响应体必须关（bodyclose 面）；成功路径 Body 为 nil
		//（coder/websocket 契约——连接接管了流）。
		if dialResp != nil && dialResp.Body != nil {
			_ = dialResp.Body.Close()
		}
		return fmt.Errorf("node relay: dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "relay stopping") //nolint:errcheck // 重连循环吞关闭错误
	conn.SetReadLimit(1 << 20)

	hello := capability.RelayHello{Type: "hello", CarrierNodeID: nodeName, RelayVersion: o.Version}
	hb, _ := json.Marshal(hello)
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.Write(wctx, websocket.MessageText, hb)
	wcancel()
	if err != nil {
		return fmt.Errorf("node relay: hello: %w", err)
	}

	a := &relayConn{
		provider: p, conn: conn,
		send:     make(chan []byte, 64),
		sessions: make(map[string]*relaySession),
	}
	defer a.shutdown()
	go a.writeLoop(ctx)

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("node relay: read: %w", err)
		}
		if msgType != websocket.MessageBinary {
			continue // 服务端只发二进制帧（hello 无应答面）
		}
		f, err := capability.ParseRelayFrame(data)
		if err != nil {
			slog.Info("node relay: malformed frame", "err", err.Error())
			continue
		}
		a.dispatch(ctx, f)
	}
}

// relaySession 是一个会话的活体。
type relaySession struct {
	cancel context.CancelFunc
	stdin  *io.PipeWriter
	resize chan capability.ExecSize
}

// relayConn 是单连接的多路复用面（swarm 同构；providers 互不 import 的
// 纪律下各自私有）。
type relayConn struct {
	provider *Provider
	conn     *websocket.Conn
	send     chan []byte

	mu       sync.Mutex
	sessions map[string]*relaySession
	closed   bool
}

// writeLoop 串行化写（coder/websocket 单写者纪律）。
func (a *relayConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-a.send:
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := a.conn.Write(wctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				_ = a.conn.Close(websocket.StatusInternalError, "relay write failed")
				return
			}
		}
	}
}

// shutdown 收口连接与全部会话（ctx 结束/读循环退出路径）。
func (a *relayConn) shutdown() {
	a.mu.Lock()
	a.closed = true
	sessions := make([]*relaySession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.sessions = make(map[string]*relaySession)
	a.mu.Unlock()
	for _, s := range sessions {
		s.cancel()
		_ = s.stdin.Close()
	}
}

// dispatch 处理一条下行帧（manager→relay）。
func (a *relayConn) dispatch(ctx context.Context, f capability.RelayFrame) {
	switch f.Kind {
	case capability.RelayFrameOpen:
		open, err := capability.DecodeRelayJSON[capability.RelaySessionOpen](f.Payload)
		if err != nil {
			return
		}
		a.startSession(ctx, open)
	case capability.RelayFrameStdin:
		if s := a.session(f.SessionID); s != nil {
			if len(f.Payload) > 0 {
				_, _ = s.stdin.Write(f.Payload)
			}
		}
	case capability.RelayFrameStdinEOF:
		if s := a.session(f.SessionID); s != nil {
			_ = s.stdin.Close()
		}
	case capability.RelayFrameResize:
		if s := a.session(f.SessionID); s != nil {
			pl, err := capability.DecodeRelayJSON[capability.ExecSizeWire](f.Payload)
			if err == nil {
				select {
				case s.resize <- capability.ExecSize(pl):
				default: // 最新值语义：丢旧保新（终端 resize 高频）
				}
			}
		}
	case capability.RelayFrameClose:
		if s := a.session(f.SessionID); s != nil {
			s.cancel() // ExecWorkload ctx 收口 = SPDY 流关闭（kubelet 终止进程）
		}
	}
}

func (a *relayConn) session(id string) *relaySession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// startSession 起一个 exec 会话 goroutine：ack → ExecWorkload → exit/error
// 收口帧。
func (a *relayConn) startSession(ctx context.Context, open capability.RelaySessionOpen) {
	sctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	s := &relaySession{cancel: cancel, stdin: pw, resize: make(chan capability.ExecSize, 8)}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		_ = pw.Close()
		return
	}
	a.sessions[open.SessionID] = s
	a.mu.Unlock()

	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.sessions, open.SessionID)
			a.mu.Unlock()
			cancel()
			_ = pw.Close()
		}()
		// 受理回执（instance 与受理解析同源——平台回显链）。
		a.post(capability.MarshalRelayFrame(capability.RelayFrameAck, open.SessionID,
			capability.EncodeRelayJSON(capability.RelaySessionAck{Instance: open.Instance})))
		code, err := a.provider.ExecWorkload(sctx, capability.ExecWorkloadRequest{
			WorkloadID: open.WorkloadID,
			Instance:   open.Instance,
			Argv:       open.Argv,
			TTY:        open.TTY,
			Stdin:      pr,
			Stdout:     &relayFrameWriter{a: a, kind: capability.RelayFrameStdout, session: open.SessionID},
			Stderr:     &relayFrameWriter{a: a, kind: capability.RelayFrameStderr, session: open.SessionID},
			Resize:     s.resize,
		})
		if err != nil {
			// 执行失败如实上报（exit 帧缺席即会话失败；连接级收口时 post
			// 静默——对端已不在）。
			a.post(capability.MarshalRelayFrame(capability.RelayFrameError, open.SessionID,
				capability.EncodeRelayJSON(capability.RelaySessionError{Code: "exec_failed", Message: err.Error()})))
			return
		}
		a.post(capability.MarshalRelayFrame(capability.RelayFrameExit, open.SessionID,
			capability.EncodeRelayJSON(capability.RelaySessionExit{Code: code})))
	}()
}

// post 入队一帧（连接收口后静默丢弃）。
func (a *relayConn) post(b []byte) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return
	}
	select {
	case a.send <- b:
	default:
		// 写队列满：连接级异常由 writeLoop 超时收口兜底。
	}
}

// relayFrameWriter 把容器输出流切帧上行。
type relayFrameWriter struct {
	a       *relayConn
	kind    capability.RelayFrameKind
	session string
}

func (w *relayFrameWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		chunk := p
		if len(chunk) > relayFrameCap {
			chunk = chunk[:relayFrameCap]
		}
		w.a.post(capability.MarshalRelayFrame(w.kind, w.session, chunk))
		p = p[len(chunk):]
	}
	return total, nil
}

// relayHTTPClient 是回环代理拨号 gateway 的 HTTP 客户端（明文 VPC 形态，
// ADR-0049 决策 2；代理安全承载 = node token）。
func relayHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true, // 每次拨号独立连接（重连语义清晰）
	}}
}
