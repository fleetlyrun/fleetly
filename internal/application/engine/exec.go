package engine

// Exec 会话域（F3.2，ADR-0049）：受理（解析/限额）、路由（反向中继 hub）、
// 生命周期（空闲/硬 TTL、单消费端附着）。会话不是资源行——注册表是进程内
// 活体，台账 = 受理审计行（api 层四件一拍，本域零持久化）。
//
// 数据流：消费端（CLI gRPC / Console WS 适配器，ExecClientPipe）↔ 会话泵
// ↔ 中继 hub ↔ 节点中继 WS 连接（RelayWriter 适配器）。帧协议单源
// capability；engine 不感知传输。
//
// 会话语义锚：消费端收流（Recv io.EOF / gRPC CloseSend）≠ 终止——只关
// 载体 stdin（`fleetly exec` 形态：attach 后 CloseSend 读退出码）；终止 =
// exit/error 收口、TTL、中继失联或消费端整流断开（ctx 结束）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// exec 限额常量（ADR-0049 决策 4；编译期常量先例 = maxAppsPerProject 族）。
const (
	// execMaxSessionsPerTeam 是 per-Team 并发会话上限。
	execMaxSessionsPerTeam = 8
	// execIdleTimeout 是无帧空闲超时（任一方向帧活动即重置）。
	execIdleTimeout = 10 * time.Minute
	// execHardTTL 是会话硬时效（受理起算——CreateSession 即装配；未附着
	// 会话同样受管：票据清扫只删票据无回调，回收路径只有这里。2026-10-14
	// P0-1 K1 修正——此前只在 AttachClient 装，未附着会话永占 team 限额）。
	execHardTTL = 30 * time.Minute
	// execOutboundCap 是下行帧缓冲上限（慢消费端防内存膨胀——满即收口）。
	execOutboundCap = 256
	// execCloseGrace 是消费端断开后等待中继收口的宽限。
	execCloseGrace = 5 * time.Second
)

// exec 会话域哨兵（api 层映射稳定 errcode）。
var (
	// ErrExecUnsupported 是 Runtime 缺 exec 子面（E_EXEC_UNSUPPORTED）。
	ErrExecUnsupported = errors.New("runtime has no exec sub-face")
	// ErrExecNoInstance 是解析无在跑实例。
	ErrExecNoInstance = errors.New("no running instance for workload")
	// ErrExecInstanceGone 是锚定实例不在跑（实例锚定受理：漂移/退出后
	// 重放的诚实形态——会话与实例绑定，不代选替身）。
	ErrExecInstanceGone = errors.New("requested instance is not running for workload")
	// ErrExecNodeUnanchored 是载体节点尚未锚定（刚加入/对账未及）。
	ErrExecNodeUnanchored = errors.New("carrier node not anchored yet")
	// ErrExecRelayOffline 是目标节点中继不在线（E_NODE_RELAY_OFFLINE）。
	ErrExecRelayOffline = errors.New("node relay relay offline")
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
	// Instance 是可选的实例锚（ADR-0067）：非空时锚定该具体在跑实例
	// （ExecInstances 观测行的编排器实例标识），不中即 ErrExecInstanceGone
	// ——会话与实例绑定，不代选替身；空 = auto（首个在跑实例）。
	Instance string
	Argv     []string
	TTY      bool
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

// RelayWriter 是节点中继连接的下行写端（assembly WS 适配器实现；
// 实现负责串行化与连接生命周期）。
type RelayWriter interface {
	// WriteRelayFrame 下发一条线上形态中继帧（capability.Marshal 产物）。
	WriteRelayFrame(b []byte) error
}

// execDomain 是 exec 会话域实例态。
type execDomain struct {
	mu       sync.Mutex
	sessions map[string]*execSession
	relays   map[string]*relayConn // 平台节点 ID → 在连中继
	execSeq  atomic.Uint64         // 会话序号（id = exec-<n>，ADR-0070 独立类）
	// after 是 TTL 计时器缝（默认 time.AfterFunc；测试捕获回调手动触发
	// ——FakeClock 驱不动真实 timer，返回 nil 计时器由各 nil 守卫兜住）。
	after func(d time.Duration, f func()) *time.Timer
}

// relayConn 是一条在连节点中继。
type relayConn struct {
	nodeID  string
	version string
	w       RelayWriter
}

// execSession 是一个受理后的会话活体。
type execSession struct {
	info ExecSessionInfo

	mu       sync.Mutex
	closed   bool
	attached bool
	out      chan ExecServerFrame
	relay    *relayConn // 路由锚（受理时锁定；中继失联即收口）

	idleTimer *time.Timer
	hardTimer *time.Timer
}

func (d *execDomain) init() {
	d.sessions = make(map[string]*execSession)
	d.relays = make(map[string]*relayConn)
	d.after = time.AfterFunc
}

// ExecUnsupported 报告 Runtime 是否缺 exec 子面（api 受理面诚实失败锚）。
func (e *Engine) ExecUnsupported() bool { return e.execFace == nil }

// ExecValidateClusterToken 校验中继凭证（/v1/platform/binary 原生入口的
// 鉴权面；RelayAttach 内含同一校验——单源 Provider 调用）。
func (e *Engine) ExecValidateClusterToken(ctx context.Context, token string) error {
	if e.execFace == nil {
		return ErrExecUnsupported
	}
	return e.execFace.ExecClusterToken(ctx, token)
}

// CreateSession 受理会话：实时解析实例与节点 → 限额 → 注册（未附着态；
// 消费端在票据 TTL 内附着，超时由硬 TTL 兜底收口）。实例选择策略单源在
// 此（ADR-0067）：Instance 空 = auto（ExecInstances 首行——provider 已按
// ID 升序），非空 = 锚定匹配（不中即 ErrExecInstanceGone）。解析快照
// 语义：实例漂移不重试——会话与实例绑定，漂移即诚实失败由客户端重开。
func (e *Engine) CreateSession(ctx context.Context, in CreateExecInput) (*ExecSessionInfo, error) {
	if e.execFace == nil {
		return nil, ErrExecUnsupported
	}
	workloadID := WorkloadID(in.AppID, in.Process)
	target, err := e.resolveExecTarget(ctx, workloadID, in.Instance)
	if err != nil {
		return nil, err
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
	relay := e.exec.relays[n.PlatformID]
	if relay == nil {
		e.exec.mu.Unlock()
		return nil, fmt.Errorf("%w: node %s", ErrExecRelayOffline, n.PlatformID)
	}

	s := &execSession{
		info: ExecSessionInfo{
			// 会话是内存活体（不落库）：进程内单调序号即身份（ADR-0070）。
			ID:         entityid.Join("exec", int64(e.exec.execSeq.Add(1))), //nolint:gosec // 序号域内非负
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
		relay: relay,
	}
	// 受理即起硬 TTL（K1，2026-10-14 P0-1）：未附着会话的回收路径。计时
	// 器字段读写一律持 s.mu（装配/触碰/收口三面同锁，防 timer 字段竞态）。
	s.mu.Lock()
	s.hardTimer = e.exec.after(execHardTTL, func() {
		e.finishSession(s, &ExecErrFrame{Code: "session_expired", Message: "session hard TTL exceeded"})
	})
	s.mu.Unlock()
	e.exec.sessions[s.info.ID] = s
	e.exec.mu.Unlock()
	return &s.info, nil
}

// resolveExecTarget 是实例选择策略的单源（auto = 首个在跑，锚定 = 匹配）。
// Provider 哨兵归一为受理位信封（跨层哨兵词汇单源 capability：engine 不
// import providers）。
func (e *Engine) resolveExecTarget(ctx context.Context, workloadID, instance string) (capability.ExecTargetInstance, error) {
	rows, err := e.execFace.ExecInstances(ctx, workloadID)
	if err != nil {
		if errors.Is(err, capability.ErrExecNoRunning) {
			return capability.ExecTargetInstance{}, fmt.Errorf("%w: %s", ErrExecNoInstance, workloadID)
		}
		return capability.ExecTargetInstance{}, err
	}
	selected := capability.ExecTargetInstance{}
	if instance == "" {
		if len(rows) > 0 {
			selected = rows[0] // auto：provider 升序首行（确定性）
		}
	} else {
		for _, row := range rows {
			if row.Instance == instance {
				selected = row
				break
			}
		}
		if selected.Instance == "" {
			return capability.ExecTargetInstance{}, fmt.Errorf("%w: workload %s instance %s", ErrExecInstanceGone, workloadID, instance)
		}
	}
	if selected.Instance == "" || selected.CarrierNodeID == "" {
		return capability.ExecTargetInstance{}, fmt.Errorf("%w: %s", ErrExecNoInstance, workloadID)
	}
	return selected, nil
}

// ExecInstances 返回进程的在跑实例集快照（API 列表面；列表面诚实空——
// 无在跑实例是观测行而非错误，与受理面哨兵语义分立）。
func (e *Engine) ExecInstances(ctx context.Context, appID, process string) ([]capability.ExecTargetInstance, error) {
	if e.execFace == nil {
		return nil, ErrExecUnsupported
	}
	rows, err := e.execFace.ExecInstances(ctx, WorkloadID(appID, process))
	if err != nil {
		if errors.Is(err, capability.ErrExecNoRunning) {
			return []capability.ExecTargetInstance{}, nil
		}
		return nil, err
	}
	return rows, nil
}

// NodePlatformIDByCarrier 是载体节点 → 平台节点 ID 的反查面（实例列表
// 回显 node 用；锚定表未及即 ErrExecNodeUnanchored——调用方按诚实边界
// 降级呈现，与受理面同源）。
func (e *Engine) NodePlatformIDByCarrier(ctx context.Context, carrierNodeID string) (string, error) {
	n, err := e.nodes.ByCarrier(ctx, e.db.Runner(), carrierNodeID)
	if err != nil {
		return "", fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, carrierNodeID)
	}
	return n.PlatformID, nil
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
// 帧、TTL、中继失联或消费端整流断开）。第二附着者即刻 attach_conflict。
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
	relay := s.relay
	e.exec.mu.Unlock()

	// 首帧元数据 + 向中继下发会话开帧（受理时已锁定路由锚）。
	if err := pipe.Send(ExecServerFrame{Meta: &ExecMetaFrame{Instance: s.info.Instance, NodeID: s.info.NodeID}}); err != nil {
		e.finishSession(s, nil)
		return nil
	}
	open := capability.RelaySessionOpen{
		SessionID: s.info.ID, WorkloadID: s.info.WorkloadID,
		Instance: s.info.Instance, Argv: s.info.Argv, TTY: s.info.TTY,
	}
	if err := e.sendRelay(relay, capability.RelayFrameOpen, s.info.ID, capability.EncodeRelayJSON(open)); err != nil {
		e.finishSession(s, relayGoneFrame(err))
		return nil
	}

	// 空闲计时（attach 起算）；硬 TTL 已在受理起算（CreateSession），不重
	// 复装配——重复装配会漏停旧计时器。装配持 s.mu。
	s.mu.Lock()
	s.idleTimer = e.exec.after(execIdleTimeout, func() {
		e.finishSession(s, &ExecErrFrame{Code: "idle_timeout", Message: "no session traffic within the idle window"})
	})
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.idleTimer != nil {
			s.idleTimer.Stop()
		}
		s.mu.Unlock()
	}()

	// 上行泵：消费端帧 → 中继。EOF = stdin 关闭（StdinEOF 帧转达），
	// 非终止；写失败（中继断）由 deliver 路径收口兜底。
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
				if e.sendRelay(relay, capability.RelayFrameResize, s.info.ID, capability.EncodeRelayJSON(pl)) != nil {
					return
				}
			default:
				if e.sendRelay(relay, capability.RelayFrameStdin, s.info.ID, f.Stdin) != nil {
					return
				}
			}
		}
	}()

	// 下行泵：中继帧通道 → 消费端。
	for {
		select {
		case <-upDone:
			// 消费端收流：关载体 stdin（一次性信号）；会话继续至 exit。
			_ = e.sendRelay(relay, capability.RelayFrameStdinEOF, s.info.ID, nil)
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
			_ = e.sendRelay(relay, capability.RelayFrameClose, s.info.ID, nil)
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
					e.finishSession(s, &ExecErrFrame{Code: "client_gone", Message: "client stream ended; relay close not acknowledged"})
					return nil
				}
			}
		}
	}
}

// deliverRelayFrame 是 hub 上行投递（中继读循环逐帧调用）。
func (e *Engine) deliverRelayFrame(f capability.RelayFrame) {
	e.exec.mu.Lock()
	s := e.exec.sessions[f.SessionID]
	e.exec.mu.Unlock()
	if s == nil {
		return // 会话已收口（迟到帧丢弃）
	}
	var out ExecServerFrame
	switch f.Kind {
	case capability.RelayFrameAck:
		return // 受理回执（instance 已在 meta 首帧回显）
	case capability.RelayFrameStdout:
		out.Stdout = f.Payload
	case capability.RelayFrameStderr:
		out.Stderr = f.Payload
	case capability.RelayFrameExit:
		pl, err := capability.DecodeRelayJSON[capability.RelaySessionExit](f.Payload)
		if err != nil {
			return
		}
		// 退出码透传（int→int32：POSIX 退出码值域 0-255，钳制边界是
		// 纵深防御——代理侧是我们自己的 ExecWorkload 返回值）。
		code := int32(clampExitCode(pl.Code))
		out.Exit = &code
	case capability.RelayFrameError:
		pl, err := capability.DecodeRelayJSON[capability.RelaySessionError](f.Payload)
		if err != nil {
			return
		}
		out.Err = &ExecErrFrame{Code: pl.Code, Message: pl.Message}
	default:
		return
	}
	// 发送与 closed 复查同临界区（K2，2026-10-14 P0-1）：finishSession 置
	// closed 后才 close(s.out)，发送方持锁复查即被排除——close 恒独占，
	// send-on-closed 竞态结构性消除。
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return // 会话已收口（迟到帧丢弃）
	}
	select {
	case s.out <- out:
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		// 慢消费端：缓冲满即收口（内存膨胀防护，ADR-0049 决策 4 注记）。
		e.finishSession(s, &ExecErrFrame{Code: "client_stalled", Message: "client is not draining session output"})
	}
}

// sendRelay 下发一帧到中继连接；失败即注销该连接并收口其名下会话。
func (e *Engine) sendRelay(a *relayConn, kind capability.RelayFrameKind, sessionID string, payload []byte) error {
	if err := a.w.WriteRelayFrame(capability.MarshalRelayFrame(kind, sessionID, payload)); err != nil {
		e.dropRelayConn(a)
		return err
	}
	return nil
}

// touchSession 重置空闲计时（idleTimer 读写持 s.mu——timer 字段三面同锁
// 的触碰半边）。
func (e *Engine) touchSession(s *execSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Reset(execIdleTimeout)
	}
}

// finishSession 收口会话（幂等）：停计时、尽力向中继发终止、清注册表、
// 投递终结帧（有则）后关闭下行通道。
func (e *Engine) finishSession(s *execSession, last *ExecErrFrame) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	// 计时器停在与 closed 同临界区（timer 字段三面同锁的收口半边）。
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	if s.hardTimer != nil {
		s.hardTimer.Stop()
	}
	s.mu.Unlock()

	e.exec.mu.Lock()
	relay := s.relay
	if e.exec.sessions[s.info.ID] == s {
		delete(e.exec.sessions, s.info.ID)
	}
	e.exec.mu.Unlock()
	if relay != nil {
		// 尽力终止（中继断线时静默；中继侧 exec 收口尽力而为）。
		_ = relay.w.WriteRelayFrame(capability.MarshalRelayFrame(capability.RelayFrameClose, s.info.ID, nil))
	}
	// 终结帧：closed 已置位，deliverRelayFrame 持锁复查即被排除——此处
	// 是唯一发送方，随后的 close 恒独占（K2 的另一半保证）。
	if last != nil {
		select {
		case s.out <- ExecServerFrame{Err: last}:
		default:
		}
	}
	close(s.out)
}

// ---- 中继 hub（/v1/relay 服务端握手与注册面；assembly WS 适配器消费） ----

// RelayAttach 是中继连接附着：校验凭证（集群成员权等价，C3）→ 载体
// 节点 ID 反查平台节点 ID（锚定表）→ 注册写端。同节点重连顶替旧连接：
// 未附着会话重指新连接（受理面立即可用），已附着会话收口（exec 不可迁移，
// 客户端重开——诚实边界）。feed 供适配器上行投递；detach 在连接断开时调用。
func (e *Engine) RelayAttach(ctx context.Context, token, carrierNodeID, relayVersion string, w RelayWriter) (feed func(capability.RelayFrame), detach func(), err error) {
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

	a := &relayConn{nodeID: n.PlatformID, version: relayVersion, w: w}
	e.exec.mu.Lock()
	old := e.exec.relays[n.PlatformID]
	e.exec.relays[n.PlatformID] = a
	var attached []*execSession
	for _, s := range e.exec.sessions {
		if s.relay != old {
			continue
		}
		if s.attached {
			attached = append(attached, s) // 在途 exec 不可迁移：收口
		} else {
			s.relay = a // 未附着：重指新连接
		}
	}
	e.exec.mu.Unlock()
	if old != nil {
		e.log.Info("node relay replaced", "node", n.PlatformID, "version", relayVersion)
	} else {
		e.log.Info("node relay connected", "node", n.PlatformID, "version", relayVersion)
	}
	for _, s := range attached {
		e.finishSession(s, relayGoneFrame(nil))
	}

	feed = func(f capability.RelayFrame) { e.deliverRelayFrame(f) }
	detach = func() { e.dropRelayConn(a) }
	return feed, detach, nil
}

// dropRelayConn 注销中继连接（身份校验：已被顶替的连接不误删继任者）并
// 收口其名下会话（relay_disconnected 如实告知）。
func (e *Engine) dropRelayConn(a *relayConn) {
	e.exec.mu.Lock()
	current := e.exec.relays[a.nodeID]
	if current == a {
		delete(e.exec.relays, a.nodeID)
	}
	var affected []*execSession
	for _, s := range e.exec.sessions {
		if s.relay == a {
			affected = append(affected, s)
		}
	}
	e.exec.mu.Unlock()
	if current != a {
		return // 顶替路径已处理会话重指/收口
	}
	e.log.Info("node relay disconnected", "node", a.nodeID)
	for _, s := range affected {
		e.finishSession(s, relayGoneFrame(nil))
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
	out := make(map[string]RelayStatus, len(e.exec.relays))
	for id, a := range e.exec.relays {
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

func relayGoneFrame(err error) *ExecErrFrame {
	msg := "node relay connection lost"
	if err != nil {
		msg += ": " + err.Error()
	}
	return &ExecErrFrame{Code: "relay_disconnected", Message: msg}
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
