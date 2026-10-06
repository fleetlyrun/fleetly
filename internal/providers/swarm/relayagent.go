package swarm

// 节点中继代理（F3.2，ADR-0049 决策 2）：`fleetlyd relay` 在节点上运行的本
// 实现与 manager 进程内回环代理共用——拨号控制面 gateway 的 /v1/relay，
// 握手上报载体节点身份（docker info 的本机 NodeID），随后多路复用收发
// exec 会话帧。阻塞至 ctx 结束（内部重连退避）；凭证每次拨号前取新
//（回环形态 = 本地 SwarmInspect，rotate 后自愈；worker 形态 = 启动注入
// 固定 token，轮换后失联待重跑装载脚本——泄漏处置语义）。

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
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// relay 重连节拍（上限钳制；连接期错误不轰炸 manager）。
const (
	relayReconnectMin = time.Second
	relayReconnectMax = 30 * time.Second
)

// relayFrameCap 是单帧输出分块上限（stdout/stderr 大块写切帧；manager 侧
// 读上限与其对齐）。
const relayFrameCap = 32 * 1024

// RunRelayAgent 实现 RuntimeExec：代理主循环（重连 + 服务单连接）。
func (p *Provider) RunRelayAgent(ctx context.Context, o capability.RelayAgentOptions) error {
	backoff := relayReconnectMin
	for {
		err := p.relayAgentOnce(ctx, o)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			p.logAgent("relay agent connection ended", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > relayReconnectMax {
			backoff = relayReconnectMax
		}
	}
}

// relayAgentOnce 服务一条连接：拨号 → hello → 会话帧循环，直至错误或
// ctx 结束。成功服务一段时期（无错误存活）后重连退避复位由调用侧节拍
// 保证（错误路径才退避增长——简化：恒定增长，长连接场景重连罕见）。
func (p *Provider) relayAgentOnce(ctx context.Context, o capability.RelayAgentOptions) error {
	if o.GatewayURL == "" {
		return errors.New("relay agent: gateway url is required")
	}
	token := ""
	if o.JoinToken != nil {
		t, err := o.JoinToken(ctx)
		if err != nil {
			return fmt.Errorf("relay agent: credential: %w", err)
		}
		token = t
	} else {
		t, err := p.localClusterToken(ctx)
		if err != nil {
			return err
		}
		token = t
	}
	info, err := p.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("relay agent: local info: %w", err)
	}
	if info.Info.Swarm.NodeID == "" {
		return errors.New("relay agent: local node is not in a swarm")
	}

	hdr := http.Header{"Authorization": []string{"Bearer " + token}}
	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	conn, dialResp, err := websocket.Dial(dialCtx, o.GatewayURL+"/v1/relay", &websocket.DialOptions{
		HTTPHeader: hdr, HTTPClient: relayHTTPClient(),
	})
	cancelDial()
	if err != nil {
		// 失败路径的响应体必须关（bodyclose 面）；成功路径 Body 为 nil
		// （coder/websocket 契约——连接接管了流）。
		if dialResp != nil && dialResp.Body != nil {
			_ = dialResp.Body.Close()
		}
		return fmt.Errorf("relay agent: dial: %w", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "agent stopping") //nolint:errcheck // 重连循环吞关闭错误
	conn.SetReadLimit(1 << 20)

	hello := capability.AgentHello{Type: "hello", CarrierNodeID: info.Info.Swarm.NodeID, AgentVersion: o.AgentVersion}
	hb, _ := json.Marshal(hello)
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.Write(wctx, websocket.MessageText, hb)
	wcancel()
	if err != nil {
		return fmt.Errorf("relay agent: hello: %w", err)
	}

	a := &agentConn{
		provider: p, conn: conn,
		send:     make(chan []byte, 64),
		sessions: make(map[string]*agentSession),
	}
	defer a.shutdown()
	go a.writeLoop(ctx)

	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("relay agent: read: %w", err)
		}
		if msgType != websocket.MessageBinary {
			continue // 服务端只发二进制帧（hello 无应答面）
		}
		f, err := capability.ParseAgentFrame(data)
		if err != nil {
			p.logAgent("relay agent: malformed frame", err)
			continue
		}
		a.dispatch(ctx, f)
	}
}

// agentSession 是节点侧一个会话的活体。
type agentSession struct {
	cancel context.CancelFunc
	stdin  *io.PipeWriter
	resize chan capability.ExecSize
}

// agentConn 是单连接的多路复用面。
type agentConn struct {
	provider *Provider
	conn     *websocket.Conn
	send     chan []byte

	mu       sync.Mutex
	sessions map[string]*agentSession
	closed   bool
}

// writeLoop 串行化写（coder/websocket 单写者纪律）。
func (a *agentConn) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-a.send:
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := a.conn.Write(wctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				_ = a.conn.Close(websocket.StatusInternalError, "agent write failed")
				return
			}
		}
	}
}

// shutdown 收口连接与全部会话（ctx 结束/读循环退出路径）。
func (a *agentConn) shutdown() {
	a.mu.Lock()
	a.closed = true
	sessions := make([]*agentSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.sessions = make(map[string]*agentSession)
	a.mu.Unlock()
	for _, s := range sessions {
		s.cancel()
		_ = s.stdin.Close()
	}
}

// dispatch 处理一条下行帧（manager→agent）。
func (a *agentConn) dispatch(ctx context.Context, f capability.AgentFrame) {
	switch f.Kind {
	case capability.AgentFrameOpen:
		open, err := capability.DecodeAgentJSON[capability.AgentSessionOpen](f.Payload)
		if err != nil {
			return
		}
		a.startSession(ctx, open)
	case capability.AgentFrameStdin:
		if s := a.session(f.SessionID); s != nil {
			if len(f.Payload) > 0 {
				_, _ = s.stdin.Write(f.Payload) // PipeWriter 无 ctx 面；Close 由会话收口
			}
		}
	case capability.AgentFrameStdinEOF:
		if s := a.session(f.SessionID); s != nil {
			_ = s.stdin.Close()
		}
	case capability.AgentFrameResize:
		if s := a.session(f.SessionID); s != nil {
			pl, err := capability.DecodeAgentJSON[capability.ExecSizeWire](f.Payload)
			if err == nil {
				select {
				case s.resize <- capability.ExecSize(pl):
				default: // 最新值语义：丢旧保新（终端 resize 高频）
				}
			}
		}
	case capability.AgentFrameClose:
		if s := a.session(f.SessionID); s != nil {
			s.cancel() // ExecWorkload ctx 收口 = hijack 关闭（尽力终止）
		}
	}
}

func (a *agentConn) session(id string) *agentSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// startSession 起一个 exec 会话 goroutine：ack → ExecWorkload → exit/error
// 收口帧。
func (a *agentConn) startSession(ctx context.Context, open capability.AgentSessionOpen) {
	sctx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	s := &agentSession{cancel: cancel, stdin: pw, resize: make(chan capability.ExecSize, 8)}
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
		a.post(capability.MarshalAgentFrame(capability.AgentFrameAck, open.SessionID,
			capability.EncodeAgentJSON(capability.AgentSessionAck{Instance: open.Instance})))
		code, err := a.provider.ExecWorkload(sctx, capability.ExecWorkloadRequest{
			WorkloadID: open.WorkloadID,
			Instance:   open.Instance,
			Argv:       open.Argv,
			TTY:        open.TTY,
			Stdin:      pr,
			Stdout:     &agentFrameWriter{a: a, kind: capability.AgentFrameStdout, session: open.SessionID},
			Stderr:     &agentFrameWriter{a: a, kind: capability.AgentFrameStderr, session: open.SessionID},
			Resize:     s.resize,
		})
		if err != nil {
			// 执行失败如实上报（exit 帧缺席即会话失败；连接级收口时 post
			// 静默——对端已不在）。
			a.post(capability.MarshalAgentFrame(capability.AgentFrameError, open.SessionID,
				capability.EncodeAgentJSON(capability.AgentSessionError{Code: "exec_failed", Message: err.Error()})))
			return
		}
		a.post(capability.MarshalAgentFrame(capability.AgentFrameExit, open.SessionID,
			capability.EncodeAgentJSON(capability.AgentSessionExit{Code: code})))
	}()
}

// post 入队一帧（连接收口后静默丢弃）。
func (a *agentConn) post(b []byte) {
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

// agentFrameWriter 把容器输出流切帧上行。
type agentFrameWriter struct {
	a       *agentConn
	kind    capability.AgentFrameKind
	session string
}

func (w *agentFrameWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		chunk := p
		if len(chunk) > relayFrameCap {
			chunk = chunk[:relayFrameCap]
		}
		w.a.post(capability.MarshalAgentFrame(w.kind, w.session, chunk))
		p = p[len(chunk):]
	}
	return total, nil
}

// localClusterToken 是回环形态的凭证源（本地 SwarmInspect worker token；
// rotate 后下次重连自愈）。
func (p *Provider) localClusterToken(ctx context.Context) (string, error) {
	inspect, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("relay agent: local credential: %w", err)
	}
	return inspect.Swarm.JoinTokens.Worker, nil
}

func (p *Provider) logAgent(msg string, err error) {
	if err != nil {
		slog.Info(msg, "err", err.Error())
	}
}
