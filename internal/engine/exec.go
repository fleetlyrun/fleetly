package engine

// Exec 会话域（F3.2，ADR-0049）：受理（解析/限额）、路由（反向中继 hub）、
// 生命周期（空闲/硬 TTL、单消费端附着）。会话不是资源行——注册表是进程内
// 活体，台账 = 受理审计行（api 层四件一拍，本域零持久化）。
//
// 数据流：消费端（CLI gRPC / Console WS 适配器，ExecClientPipe）↔ 会话泵
// ↔ 中继 hub ↔ 节点代理 WS 连接（RelayAgentWriter 适配器）。帧协议单源
// capability；engine 不感知传输。
//
// 会话语义锚：消费端收流（Recv io.EOF / gRPC CloseSend）≠ 终止——只关
// 载体 stdin（`fleetly exec` 形态：attach 后 CloseSend 读退出码）；终止 =
// exit/error 收口、TTL、代理失联或消费端整流断开（ctx 结束）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/oklog/ulid/v2"
)

// exec 限额常量（ADR-0049 决策 4；编译期常量先例 = maxAppsPerProject 族）。
const (
	// execMaxSessionsPerTeam 是 per-Team 并发会话上限。
	execMaxSessionsPerTeam = 8
	// execIdleTimeout 是无帧空闲超时（任一方向帧活动即重置）。
	execIdleTimeout = 10 * time.Minute
	// execHardTTL 是会话硬时效（受理起算；未附着会话同样受管——票据
	// TTL 后无附着即由此收口）。
	execHardTTL = 30 * time.Minute
	// execOutboundCap 是下行帧缓冲上限（慢消费端防内存膨胀——满即收口）。
	execOutboundCap = 256
	// execCloseGrace 是消费端断开后等待代理收口的宽限。
	execCloseGrace = 5 * time.Second
)

// exec 会话域哨兵（api 层映射稳定 errcode）。
var (
	// ErrExecUnsupported 是 Runtime 缺 exec 子面（E_EXEC_UNSUPPORTED）。
	ErrExecUnsupported = errors.New("runtime has no exec sub-face")
	// ErrExecNoInstance 是解析无在跑实例。
	ErrExecNoInstance = errors.New("no running instance for workload")
	// ErrExecNodeUnanchored 是载体节点尚未锚定（刚加入/对账未及）。
	ErrExecNodeUnanchored = errors.New("carrier node not anchored yet")
	// ErrExecAgentOffline 是目标节点代理不在线（E_NODE_AGENT_OFFLINE）。
	ErrExecAgentOffline = errors.New("node relay agent offline")
	// ErrExecTeamLimit 是 per-Team 并发会话超限。
	ErrExecTeamLimit = errors.New("too many concurrent exec sessions for team")
	// ErrExecSessionNotFound 是会话不存在或已收口。
	ErrExecSessionNotFound = errors.New("exec session not found")
)

// ExecSessionInfo 是受理产品（api 层回显/票据绑定面）。
type ExecSessionInfo struct {
	ID         string
	TeamID     string
	AppID      string
	Process    string
	WorkloadID string
	Instance   string
	NodeID     string
	Argv       []string
	TTY        bool
	CreatedAt  time.Time
}

// CreateExecInput 是会话受理输入（app 归属/team 由 api 层解析注入）。
type CreateExecInput struct {
	TeamID  string
	AppID   string
	Process string
	Argv    []string
	TTY     bool
}

// ExecClientFrame 是消费端上行帧（传输中立）。
type ExecClientFrame struct {
	Stdin  []byte
	Resize *capability.ExecSize
}

// ExecServerFrame 是消费端下行帧（传输中立；Exit/Err 二选一收口）。
type ExecServerFrame struct {
	Meta   *ExecMetaFrame
	Stdout []byte
	Stderr []byte
	Exit   *int32
	Err    *ExecErrFrame
}

// ExecMetaFrame 是下行首帧：实际命中实例与节点。
type ExecMetaFrame struct {
	Instance string
	NodeID   string
}

// ExecErrFrame 是流中错误帧（code 为 errcode 注册表形态或传输面标识）。
type ExecErrFrame struct {
	Code    string
	Message string
}

// ExecClientPipe 是会话消费端管道（fleetlygrpc 流适配器与 assembly WS
// 适配器实现；Attach 后由会话泵驱动直至收口）。
type ExecClientPipe interface {
	// Recv 阻塞收上行帧；io.EOF = 消费端收流（stdin 关闭语义，非终止）。
	Recv() (ExecClientFrame, error)
	// Send 发下行帧；返回错误 = 消费端不可达（会话收口）。
	Send(ExecServerFrame) error
}

// RelayAgentWriter 是节点代理连接的下行写端（assembly WS 适配器实现；
// 实现负责串行化与连接生命周期）。
type RelayAgentWriter interface {
	// WriteAgentFrame 下发一条线上形态中继帧（capability.Marshal 产物）。
	WriteAgentFrame(b []byte) error
}

// execDomain 是 exec 会话域实例态。
type execDomain struct {
	mu       sync.Mutex
	sessions map[string]*execSession
	agents   map[string]*relayAgentConn // 平台节点 ID → 在连代理
}

// relayAgentConn 是一条在连节点代理。
type relayAgentConn struct {
	nodeID  string
	version string
	w       RelayAgentWriter
}

// execSession 是一个受理后的会话活体。
type execSession struct {
	info ExecSessionInfo

	mu       sync.Mutex
	closed   bool
	attached bool
	out      chan ExecServerFrame
	agent    *relayAgentConn // 路由锚（受理时锁定；代理失联即收口）

	idleTimer *time.Timer
	hardTimer *time.Timer
}

func (d *execDomain) init() {
	d.sessions = make(map[string]*execSession)
	d.agents = make(map[string]*relayAgentConn)
}

// ExecUnsupported 报告 Runtime 是否缺 exec 子面（api 受理面诚实失败锚）。
func (e *Engine) ExecUnsupported() bool { return e.execFace == nil }

// ExecValidateClusterToken 校验中继凭证（/v1/platform/binary 原生入口的
// 鉴权面；RelayAgentAttach 内含同一校验——单源 Provider 调用）。
func (e *Engine) ExecValidateClusterToken(ctx context.Context, token string) error {
	if e.execFace == nil {
		return ErrExecUnsupported
	}
	return e.execFace.ExecClusterToken(ctx, token)
}

// CreateSession 受理会话：实时解析实例与节点 → 限额 → 注册（未附着态；
// 消费端在票据 TTL 内附着，超时由硬 TTL 兜底收口）。解析快照语义：实例
// 漂移不重试——会话与实例绑定，漂移即诚实失败由客户端重开。
func (e *Engine) CreateSession(ctx context.Context, in CreateExecInput) (*ExecSessionInfo, error) {
	if e.execFace == nil {
		return nil, ErrExecUnsupported
	}
	workloadID := WorkloadID(in.AppID, in.Process)
	target, err := e.execFace.ExecTarget(ctx, workloadID)
	if err != nil {
		return nil, err
	}
	if target.Instance == "" || target.CarrierNodeID == "" {
		return nil, fmt.Errorf("%w: %s", ErrExecNoInstance, workloadID)
	}
	n, err := e.nodes.ByCarrier(ctx, e.db.Runner(), target.CarrierNodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, target.CarrierNodeID)
	}

	e.exec.mu.Lock()
	teamCount := 0
	for _, s := range e.exec.sessions {
		if s.info.TeamID == in.TeamID {
			teamCount++
		}
	}
	if teamCount >= execMaxSessionsPerTeam {
		e.exec.mu.Unlock()
		return nil, fmt.Errorf("%w: limit %d", ErrExecTeamLimit, execMaxSessionsPerTeam)
	}
	agent := e.exec.agents[n.PlatformID]
	if agent == nil {
		e.exec.mu.Unlock()
		return nil, fmt.Errorf("%w: node %s", ErrExecAgentOffline, n.PlatformID)
	}

	s := &execSession{
		info: ExecSessionInfo{
			ID:         ulid.Make().String(),
			TeamID:     in.TeamID,
			AppID:      in.AppID,
			Process:    in.Process,
			WorkloadID: workloadID,
			Instance:   target.Instance,
			NodeID:     n.PlatformID,
			Argv:       in.Argv,
			TTY:        in.TTY,
			CreatedAt:  e.clock.Now(),
		},
		out:   make(chan ExecServerFrame, execOutboundCap),
		agent: agent,
	}
	e.exec.sessions[s.info.ID] = s
	e.exec.mu.Unlock()
	return &s.info, nil
}

// AbandonSession 是受理侧回滚（api 层 commit 失败路径：审计未落则会话
// 不留——注册表活体与台账一致）。
func (e *Engine) AbandonSession(id string) {
	e.exec.mu.Lock()
	s := e.exec.sessions[id]
	if s != nil {
		delete(e.exec.sessions, id)
	}
	e.exec.mu.Unlock()
	if s != nil {
		e.finishSession(s, &ExecErrFrame{Code: "session_abandoned", Message: "session acceptance rolled back"})
	}
}

// AttachClient 附着消费端并驱动会话至收口（阻塞直至会话终止：exit/error
// 帧、TTL、代理失联或消费端整流断开）。第二附着者即刻 attach_conflict。
// 消费端收流（Recv EOF）只关载体 stdin——`fleetly exec` 形态依赖此语义
// （attach 后 CloseSend 读退出码）。
func (e *Engine) AttachClient(ctx context.Context, sessionID string, pipe ExecClientPipe) error {
	e.exec.mu.Lock()
	s := e.exec.sessions[sessionID]
	if s == nil {
		e.exec.mu.Unlock()
		return ErrExecSessionNotFound
	}
	if s.attached {
		e.exec.mu.Unlock()
		_ = pipe.Send(ExecServerFrame{Err: &ExecErrFrame{Code: "attach_conflict", Message: "session already has an attached client"}})
		return nil
	}
	s.attached = true
	agent := s.agent
	e.exec.mu.Unlock()

	// 首帧元数据 + 向代理下发会话开帧（受理时已锁定路由锚）。
	if err := pipe.Send(ExecServerFrame{Meta: &ExecMetaFrame{Instance: s.info.Instance, NodeID: s.info.NodeID}}); err != nil {
		e.finishSession(s, nil)
		return nil
	}
	open := capability.AgentSessionOpen{
		SessionID: s.info.ID, WorkloadID: s.info.WorkloadID,
		Instance: s.info.Instance, Argv: s.info.Argv, TTY: s.info.TTY,
	}
	if err := e.sendAgent(agent, capability.AgentFrameOpen, s.info.ID, capability.EncodeAgentJSON(open)); err != nil {
		e.finishSession(s, agentGoneFrame(err))
		return nil
	}

	// 空闲/硬 TTL 计时。
	s.idleTimer = time.AfterFunc(execIdleTimeout, func() {
		e.finishSession(s, &ExecErrFrame{Code: "idle_timeout", Message: "no session traffic within the idle window"})
	})
	s.hardTimer = time.AfterFunc(execHardTTL, func() {
		e.finishSession(s, &ExecErrFrame{Code: "session_expired", Message: "session hard TTL exceeded"})
	})
	defer func() {
		s.idleTimer.Stop()
		s.hardTimer.Stop()
	}()

	// 上行泵：消费端帧 → 代理。EOF = stdin 关闭（StdinEOF 帧转达），
	// 非终止；写失败（代理断）由 deliver 路径收口兜底。
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		for {
			f, err := pipe.Recv()
			if err != nil {
				return
			}
			e.touchSession(s)
			switch {
			case f.Resize != nil:
				pl := capability.ExecSizeWire{Cols: f.Resize.Cols, Rows: f.Resize.Rows}
				if e.sendAgent(agent, capability.AgentFrameResize, s.info.ID, capability.EncodeAgentJSON(pl)) != nil {
					return
				}
			default:
				if e.sendAgent(agent, capability.AgentFrameStdin, s.info.ID, f.Stdin) != nil {
					return
				}
			}
		}
	}()

	// 下行泵：代理帧通道 → 消费端。
	for {
		select {
		case <-upDone:
			// 消费端收流：关载体 stdin（一次性信号）；会话继续至 exit。
			_ = e.sendAgent(agent, capability.AgentFrameStdinEOF, s.info.ID, nil)
			upDone = nil // nil 通道恒阻塞——此 case 只处理一次
		case f, ok := <-s.out:
			if !ok {
				return nil // 会话已收口（终结帧已投递或消费端不可达）
			}
			e.touchSession(s)
			if err := pipe.Send(f); err != nil {
				e.finishSession(s, nil)
				return nil
			}
			if f.Exit != nil || f.Err != nil {
				e.finishSession(s, nil)
				return nil
			}
		case <-ctx.Done():
			// 整流断开：尽力终止载体 exec 后收口（宽限内等 exit 帧）。
			_ = e.sendAgent(agent, capability.AgentFrameClose, s.info.ID, nil)
			deadline := time.After(execCloseGrace)
			for {
				select {
				case f, ok := <-s.out:
					if !ok {
						return nil
					}
					if f.Exit != nil || f.Err != nil {
						e.finishSession(s, nil)
						return nil
					}
				case <-deadline:
					e.finishSession(s, &ExecErrFrame{Code: "client_gone", Message: "client stream ended; agent close not acknowledged"})
					return nil
				}
			}
		}
	}
}

// deliverAgentFrame 是 hub 上行投递（代理读循环逐帧调用）。
func (e *Engine) deliverAgentFrame(f capability.AgentFrame) {
	e.exec.mu.Lock()
	s := e.exec.sessions[f.SessionID]
	e.exec.mu.Unlock()
	if s == nil {
		return // 会话已收口（迟到帧丢弃）
	}
	var out ExecServerFrame
	switch f.Kind {
	case capability.AgentFrameAck:
		return // 受理回执（instance 已在 meta 首帧回显）
	case capability.AgentFrameStdout:
		out.Stdout = f.Payload
	case capability.AgentFrameStderr:
		out.Stderr = f.Payload
	case capability.AgentFrameExit:
		pl, err := capability.DecodeAgentJSON[capability.AgentSessionExit](f.Payload)
		if err != nil {
			return
		}
		// 退出码透传（int→int32：POSIX 退出码值域 0-255，钳制边界是
		// 纵深防御——代理侧是我们自己的 ExecWorkload 返回值）。
		code := int32(clampExitCode(pl.Code))
		out.Exit = &code
	case capability.AgentFrameError:
		pl, err := capability.DecodeAgentJSON[capability.AgentSessionError](f.Payload)
		if err != nil {
			return
		}
		out.Err = &ExecErrFrame{Code: pl.Code, Message: pl.Message}
	default:
		return
	}
	select {
	case s.out <- out:
	default:
		// 慢消费端：缓冲满即收口（内存膨胀防护，ADR-0049 决策 4 注记）。
		e.finishSession(s, &ExecErrFrame{Code: "client_stalled", Message: "client is not draining session output"})
	}
}

// sendAgent 下发一帧到代理连接；失败即注销该连接并收口其名下会话。
func (e *Engine) sendAgent(a *relayAgentConn, kind capability.AgentFrameKind, sessionID string, payload []byte) error {
	if err := a.w.WriteAgentFrame(capability.MarshalAgentFrame(kind, sessionID, payload)); err != nil {
		e.dropAgentConn(a)
		return err
	}
	return nil
}

// touchSession 重置空闲计时。
func (e *Engine) touchSession(s *execSession) {
	if s.idleTimer != nil {
		s.idleTimer.Reset(execIdleTimeout)
	}
}

// finishSession 收口会话（幂等）：停计时、尽力向代理发终止、清注册表、
// 投递终结帧（有则）后关闭下行通道。
func (e *Engine) finishSession(s *execSession, last *ExecErrFrame) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.hardTimer != nil {
		s.hardTimer.Stop()
	}
	e.exec.mu.Lock()
	agent := s.agent
	if e.exec.sessions[s.info.ID] == s {
		delete(e.exec.sessions, s.info.ID)
	}
	e.exec.mu.Unlock()
	if agent != nil {
		// 尽力终止（代理断线时静默；代理侧 exec 收口尽力而为）。
		_ = agent.w.WriteAgentFrame(capability.MarshalAgentFrame(capability.AgentFrameClose, s.info.ID, nil))
	}
	if last != nil {
		select {
		case s.out <- ExecServerFrame{Err: last}:
		default:
		}
	}
	close(s.out)
}

// ---- 中继 hub（/v1/relay 服务端握手与注册面；assembly WS 适配器消费） ----

// RelayAgentAttach 是代理连接附着：校验凭证（集群成员权等价，C3）→ 载体
// 节点 ID 反查平台节点 ID（锚定表）→ 注册写端。同节点重连顶替旧连接：
// 未附着会话重指新连接（受理面立即可用），已附着会话收口（exec 不可迁移，
// 客户端重开——诚实边界）。feed 供适配器上行投递；detach 在连接断开时调用。
func (e *Engine) RelayAgentAttach(ctx context.Context, token, carrierNodeID, agentVersion string, w RelayAgentWriter) (feed func(capability.AgentFrame), detach func(), err error) {
	if e.execFace == nil {
		return nil, nil, ErrExecUnsupported
	}
	if err := e.execFace.ExecClusterToken(ctx, token); err != nil {
		return nil, nil, err
	}
	n, err := e.nodes.ByCarrier(ctx, e.db.Runner(), carrierNodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, carrierNodeID)
	}

	a := &relayAgentConn{nodeID: n.PlatformID, version: agentVersion, w: w}
	e.exec.mu.Lock()
	old := e.exec.agents[n.PlatformID]
	e.exec.agents[n.PlatformID] = a
	var attached []*execSession
	for _, s := range e.exec.sessions {
		if s.agent != old {
			continue
		}
		if s.attached {
			attached = append(attached, s) // 在途 exec 不可迁移：收口
		} else {
			s.agent = a // 未附着：重指新连接
		}
	}
	e.exec.mu.Unlock()
	if old != nil {
		e.log.Info("relay agent replaced", "node", n.PlatformID, "version", agentVersion)
	} else {
		e.log.Info("relay agent connected", "node", n.PlatformID, "version", agentVersion)
	}
	for _, s := range attached {
		e.finishSession(s, agentGoneFrame(nil))
	}

	feed = func(f capability.AgentFrame) { e.deliverAgentFrame(f) }
	detach = func() { e.dropAgentConn(a) }
	return feed, detach, nil
}

// dropAgentConn 注销代理连接（身份校验：已被顶替的连接不误删继任者）并
// 收口其名下会话（agent_disconnected 如实告知）。
func (e *Engine) dropAgentConn(a *relayAgentConn) {
	e.exec.mu.Lock()
	current := e.exec.agents[a.nodeID]
	if current == a {
		delete(e.exec.agents, a.nodeID)
	}
	var affected []*execSession
	for _, s := range e.exec.sessions {
		if s.agent == a {
			affected = append(affected, s)
		}
	}
	e.exec.mu.Unlock()
	if current != a {
		return // 顶替路径已处理会话重指/收口
	}
	e.log.Info("relay agent disconnected", "node", a.nodeID)
	for _, s := range affected {
		e.finishSession(s, agentGoneFrame(nil))
	}
}

// RelayStatus 是节点中继在连状态（节点视图回显面）。
type RelayStatus struct {
	Online  bool
	Version string
}

// RelayStatuses 返回全量在连快照（ListNodes 回显消费）。
func (e *Engine) RelayStatuses() map[string]RelayStatus {
	e.exec.mu.Lock()
	defer e.exec.mu.Unlock()
	out := make(map[string]RelayStatus, len(e.exec.agents))
	for id, a := range e.exec.agents {
		out[id] = RelayStatus{Online: true, Version: a.version}
	}
	return out
}

// ExecActiveTeamSessions 返回 Team 的在册会话数（受理面限额回显用）。
func (e *Engine) ExecActiveTeamSessions(teamID string) int {
	e.exec.mu.Lock()
	defer e.exec.mu.Unlock()
	n := 0
	for _, s := range e.exec.sessions {
		if s.info.TeamID == teamID {
			n++
		}
	}
	return n
}

// ExecSessionByID 是会话查询面（票据兑换校验：票据绑会话）。
func (e *Engine) ExecSessionByID(id string) (*ExecSessionInfo, bool) {
	e.exec.mu.Lock()
	defer e.exec.mu.Unlock()
	s, ok := e.exec.sessions[id]
	if !ok {
		return nil, false
	}
	return &s.info, true
}

func agentGoneFrame(err error) *ExecErrFrame {
	msg := "node relay agent connection lost"
	if err != nil {
		msg += ": " + err.Error()
	}
	return &ExecErrFrame{Code: "agent_disconnected", Message: msg}
}

// clampExitCode 钳制退出码到 proto int32 值域（G115 转换收口）。
func clampExitCode(v int) int32 {
	if v > 1<<30 {
		return 1 << 30
	}
	if v < -(1 << 30) {
		return -(1 << 30)
	}
	return int32(v)
}
