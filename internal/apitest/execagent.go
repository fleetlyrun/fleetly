package apitest

// AttachFakeExecAgent：进程内假节点代理（F3.2，ADR-0049 测试基建）——
// 经 engine.RelayAgentAttach 的接口面直接接入 hub（RelayAgentWriter 是
// 传输中立接口——WS 之外的本进程形态是接口设计的红利），会话帧按
// capability 线上协议在 hub 与 FakeRuntime.ExecWorkload 间桥接。
// golden（CLI 全链）与 apitest 契约测试共用；真实代理循环（WS 拨号 +
// 重连）由 e2e dind 承担。本文件同时是中继帧协议的进程内第二消费方
// （协议形状漂移在此红）。

import (
	"context"
	"io"
	"sync"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/node"
)

// fakeExecAgent 是进程内代理活体。
type fakeExecAgent struct {
	rt   *FakeRuntime
	feed func(capability.AgentFrame)

	mu       sync.Mutex
	sessions map[string]*fakeAgentSession
}

// fakeAgentSession 是一个桥接会话。
type fakeAgentSession struct {
	stdin  *io.PipeWriter
	resize chan capability.ExecSize
	cancel context.CancelFunc
}

// AttachFakeExecAgent 把假代理接入 hub 并确保载体节点已锚定（nodes 表
// 预置——CreateSession 的 ByCarrier 反查前提；DescribeCluster 假身份
// 同源）。返回值是 detach（测试收口）。
func AttachFakeExecAgent(ctx context.Context, h *Harness) (detach func()) {
	a := &fakeExecAgent{rt: h.Runtime, sessions: map[string]*fakeAgentSession{}}

	if err := h.Services.Nodes.Upsert(ctx, h.DB.Runner(), &node.Node{
		PlatformID: FakeExecNode, CarrierID: FakeExecCarrier, Role: "manager", Available: true,
	}); err != nil {
		panic("apitest: seed node: " + err.Error())
	}
	feed, detachHub, err := h.Engine.RelayAgentAttach(ctx, "TESTTOKEN", FakeExecCarrier, "test-agent", &fakeAgentWriter{a: a})
	if err != nil {
		panic("apitest: attach fake agent: " + err.Error())
	}
	a.feed = feed
	return func() {
		detachHub()
		a.mu.Lock()
		sessions := make([]*fakeAgentSession, 0, len(a.sessions))
		for _, s := range a.sessions {
			sessions = append(sessions, s)
		}
		a.sessions = map[string]*fakeAgentSession{}
		a.mu.Unlock()
		for _, s := range sessions {
			s.cancel()
			_ = s.stdin.Close()
		}
	}
}

// fakeAgentWriter 是 hub 下行写端（进程内：直接驱动代理分发）。
type fakeAgentWriter struct {
	a *fakeExecAgent
}

func (w *fakeAgentWriter) WriteAgentFrame(b []byte) error {
	f, err := capability.ParseAgentFrame(b)
	if err != nil {
		return err
	}
	w.a.dispatch(f)
	return nil
}

// dispatch 处理 hub 下行帧（与 swarm 侧 agentConn.dispatch 同构——协议
// 形状的进程内镜像）。
func (a *fakeExecAgent) dispatch(f capability.AgentFrame) {
	switch f.Kind {
	case capability.AgentFrameOpen:
		open, err := capability.DecodeAgentJSON[capability.AgentSessionOpen](f.Payload)
		if err != nil {
			return
		}
		a.startSession(open)
	case capability.AgentFrameStdin:
		if s := a.session(f.SessionID); s != nil && len(f.Payload) > 0 {
			_, _ = s.stdin.Write(f.Payload)
		}
	case capability.AgentFrameStdinEOF:
		if s := a.session(f.SessionID); s != nil {
			_ = s.stdin.Close()
		}
	case capability.AgentFrameResize:
		if s := a.session(f.SessionID); s != nil {
			if pl, err := capability.DecodeAgentJSON[capability.ExecSizeWire](f.Payload); err == nil {
				select {
				case s.resize <- capability.ExecSize(pl):
				default:
				}
			}
		}
	case capability.AgentFrameClose:
		if s := a.session(f.SessionID); s != nil {
			s.cancel()
			// stdin EOF 让阻塞读的假执行立即收口（真实 docker 路径由
			// hijack 关闭承载）。
			_ = s.stdin.Close()
		}
	}
}

func (a *fakeExecAgent) session(id string) *fakeAgentSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// startSession 桥接一个会话：ack → FakeRuntime.ExecWorkload → exit/error。
func (a *fakeExecAgent) startSession(open capability.AgentSessionOpen) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	s := &fakeAgentSession{stdin: pw, resize: make(chan capability.ExecSize, 8), cancel: cancel}
	a.mu.Lock()
	a.sessions[open.SessionID] = s
	a.mu.Unlock()

	a.feed(capability.AgentFrame{Kind: capability.AgentFrameAck, SessionID: open.SessionID,
		Payload: capability.EncodeAgentJSON(capability.AgentSessionAck{Instance: open.Instance})})

	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.sessions, open.SessionID)
			a.mu.Unlock()
			cancel()
			_ = pw.Close()
		}()
		code, err := a.rt.ExecWorkload(ctx, capability.ExecWorkloadRequest{
			WorkloadID: open.WorkloadID, Instance: open.Instance, Argv: open.Argv, TTY: open.TTY,
			Stdin:  pr,
			Stdout: &fakeFrameFeed{a: a, kind: capability.AgentFrameStdout, session: open.SessionID},
			Stderr: &fakeFrameFeed{a: a, kind: capability.AgentFrameStderr, session: open.SessionID},
			Resize: s.resize,
		})
		if err != nil {
			a.feed(capability.AgentFrame{Kind: capability.AgentFrameError, SessionID: open.SessionID,
				Payload: capability.EncodeAgentJSON(capability.AgentSessionError{Code: "exec_failed", Message: err.Error()})})
			return
		}
		a.feed(capability.AgentFrame{Kind: capability.AgentFrameExit, SessionID: open.SessionID,
			Payload: capability.EncodeAgentJSON(capability.AgentSessionExit{Code: code})})
	}()
}

// fakeFrameFeed 把假执行输出转上行帧。
type fakeFrameFeed struct {
	a       *fakeExecAgent
	kind    capability.AgentFrameKind
	session string
}

func (f *fakeFrameFeed) Write(p []byte) (int, error) {
	f.a.feed(capability.AgentFrame{Kind: f.kind, SessionID: f.session, Payload: append([]byte(nil), p...)})
	return len(p), nil
}
