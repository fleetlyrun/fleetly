// Package execs 是 exec 会话域卫星（F3.2，ADR-0049）：受理（解析/限额）、
// 生命周期（空闲/硬 TTL、单消费端附着）；中继路由半边（hub 握手/上行投
// 递/下行下发）见 execrelay.go。会话不是资源行——注册表是进程内活体，
// 台账 = 受理审计行（api 层四件一拍，本域零持久化）。
//
// 数据流：消费端（CLI gRPC / Console WS 适配器，ExecClientPipe）↔ 会话泵
// ↔ 中继 hub ↔ 节点中继 WS 连接（RelayWriter 适配器）。帧协议单源
// capability；卫星不感知传输。
//
// 会话语义锚：消费端收流（Recv io.EOF / gRPC CloseSend）≠ 终止——只关
// 载体 stdin（`fleetly exec` 形态：attach 后 CloseSend 读退出码）；终止 =
// exit/error 收口、TTL、中继失联或消费端整流断开（ctx 结束）。
//
// P4 wave-1（ADR-0088 决策 2）：接收者下域，Engine 嵌入 execs 卫星
// struct。P5 wave-2（ADR-0088 决策 2 拆包形态，2026-11-01）：嵌入解开
// ——卫星独立成包，engine 核经构造注入持有（New 装配面），gRPC/assembly
// 消费面经核门面委托；依赖显式逐字段注入 + emitLedger 机制端口（签名单源
// engineports.EmitLedger），WorkloadID/AuditDetailJSON 事实帮手同源
// engineports；lifecycle 的回环中继挂载经 ExecFace 访问器（组合根调用
// 留核）。禁回引 *engine.Engine（enginebackref 跨包执法）。
package execs

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
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
	// execIDRandBytes 是会话 ID 的随机字节数（hex 后 32 字符，连前缀总长
	// 38——P3-1 随机化铸造面，见 CreateSession 语义锚）。
	execIDRandBytes = 16
	// relayInternalCredentialPrefix 是控制面内部中继凭证前缀（H10：只存
	// 内存、不落库/不入日志，进程消亡即失效；与节点个体凭证 fltrelay_
	// 分面——内部面可声明任意已锚定节点，是进程自身信任面）。
	relayInternalCredentialPrefix = "fltint_"
	// relayInternalCredentialBytes 是内部 token 随机字节数（256-bit 熵）。
	relayInternalCredentialBytes = 32
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
	// ErrExecRelayUnauthorized 是中继凭证不在册（伪造/已轮换/已吊销的
	// 节点个体凭证，或 join token——H10：join token 退出 relay 鉴权面）。
	ErrExecRelayUnauthorized = errors.New("relay credential is not an active node relay credential")
	// ErrExecRelayStore 是中继凭证存储故障（gateway 映射 503、WS 映射
	// 1011——绝不能落成 401，否则轮换/存储故障不可区分）。
	ErrExecRelayStore = errors.New("relay credential store is unavailable")
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
	//（ExecInstances 观测行的编排器实例标识），不中即 ErrExecInstanceGone
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

// Execs 是 exec 会话域卫星实例态（P3-1 收编显式最小依赖；P4 wave-1 接收
// 者下域；P5 wave-2 拆包导出更名，ADR-0088 决策 2/3）：行为随域、依赖
// 显式逐字段 + 机制端口注入（New 装配面，engine construct.go 组合根调
// 用）。禁挂 *engine.Engine 回引（enginebackref 跨包执法）。接收者名 x
// 沿域内约定（s 留给会话活体 execSession）。
type Execs struct {
	mu       sync.Mutex
	sessions map[string]*execSession
	relays   map[string]*relayConn // 平台节点 ID → 在连中继
	// log 是域内日志（中继顶替/失联两处 Info）。
	log *slog.Logger
	// after 是 TTL 计时器缝（默认 time.AfterFunc；测试捕获回调手动触发
	// ——FakeClock 驱不动真实 timer，返回 nil 计时器由各 nil 守卫兜住）。
	after func(d time.Duration, f func()) *time.Timer

	// 显式依赖（实测消费面）：execFace 是 exec 子面 typed 持有（F3.2，
	// ADR-0049；nil = 受理诚实失败 E_EXEC_UNSUPPORTED——无降级路径）；
	// repos 只读聚合目录（节点锚定/中继凭证行——transact.Stores 整体注入，
	// ADR-0088 决策 3）；clock 受理时间戳。
	execFace capability.RuntimeExec
	repos    transact.Stores
	clock    state.Clock

	// emitLedger 是事实发射端口（emitter，设计稿 §5 / L0#3）——中继活体
	// 顶替的审计事件（node.relay_replaced）经核的单写发射面落账；签名
	// 单源 engineports.EmitLedger（P5 wave-2 下沉），New 注入面绑定核方
	// 法值。
	emitLedger engineports.EmitLedger

	// 内部中继凭证（H10 §4.5）：控制面进程内中继（swarm manager 回环 /
	// k3s 每节点回环）的鉴权源。惰性一次铸造（crypto/rand，fltint_），只
	// 存内存——不落库、不入日志/脚本；进程重启即失效。内部 token 可在
	// hello 中声明任意已锚定节点，这是控制面进程自身信任面（若进程被
	// 攻破 exec 全链已失守，不构成新增边界）。
	internalOnce sync.Once
	internalCred string
	internalErr  error
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值）。
type Deps struct {
	Log      *slog.Logger
	ExecFace capability.RuntimeExec
	Repos    transact.Stores
	Clock    state.Clock
	// EmitLedger 是取证事实发射端口（核 emitLedger 方法值；签名单源
	// engineports.EmitLedger）。
	EmitLedger engineports.EmitLedger
}

// New 构造 exec 会话域卫星（注册表账本初始化；不启动——lifecycle 的回环
// 中继挂载与受理面由 engine 核驱动）。
func New(deps Deps) *Execs {
	x := &Execs{
		log:        deps.Log,
		execFace:   deps.ExecFace,
		repos:      deps.Repos,
		clock:      deps.Clock,
		emitLedger: deps.EmitLedger,
	}
	x.sessions = make(map[string]*execSession)
	x.relays = make(map[string]*relayConn)
	x.after = time.AfterFunc
	return x
}

// ExecFace 返回 exec 子面 typed 持有（nil = Runtime 缺 exec 能力，受理面
// 诚实失败 E_EXEC_UNSUPPORTED）。lifecycle 的回环中继挂载消费（组合根
// 调用留核，ADR-0088 决策 2）。
func (x *Execs) ExecFace() capability.RuntimeExec { return x.execFace }

// internalCredential 返回（惰性铸造的）内部中继凭证明文。
func (x *Execs) internalCredential() (string, error) {
	x.internalOnce.Do(func() {
		raw := make([]byte, relayInternalCredentialBytes)
		if _, err := cryptorand.Read(raw); err != nil {
			x.internalErr = fmt.Errorf("mint relay internal credential: %w", err)
			return
		}
		x.internalCred = relayInternalCredentialPrefix + base64.RawURLEncoding.EncodeToString(raw)
	})
	return x.internalCred, x.internalErr
}

// matchesInternal 报告 token 是否等于内部凭证（常数时间比较；铸造失败
// 视为不匹配——回环路径会把错误如实上抛）。
func (x *Execs) matchesInternal(token string) bool {
	cred, err := x.internalCredential()
	if err != nil || cred == "" || len(token) != len(cred) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(cred)) == 1
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

// CreateSession 受理会话：实时解析实例与节点 → 限额 → 注册（未附着态；
// 消费端在票据 TTL 内附着，超时由硬 TTL 兜底收口）。实例选择策略单源在
// 此（ADR-0067）：Instance 空 = auto（ExecInstances 首行——provider 已按
// ID 升序），非空 = 锚定匹配（不中即 ErrExecInstanceGone）。解析快照
// 语义：实例漂移不重试——会话与实例绑定，漂移即诚实失败由客户端重开。
//
// 会话 ID 语义锚（P3-1，2026-10-15）：会话是内存活体不落库，ADR-0070 的
// 可读 id 律不适用（该律执法持久化实体——见 ADR-0070「适用范围」）；ID
// 改 crypto 随机（exec-<32hex>）——单调序号使会话 ID 可枚举，是跨队会话
// ID 探测的放大器（S1 案卷，纯负资产），随机化与 P0-1 attach 的 Team 比对
// 纵深互补。
func (x *Execs) CreateSession(ctx context.Context, in CreateExecInput) (*ExecSessionInfo, error) {
	if x.execFace == nil {
		return nil, ErrExecUnsupported
	}
	workloadID := engineports.WorkloadID(in.AppID, in.Process)
	target, err := x.resolveExecTarget(ctx, workloadID, in.Instance)
	if err != nil {
		return nil, err
	}
	n, err := x.repos.Nodes.ByCarrier(ctx, target.CarrierNodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, target.CarrierNodeID)
	}

	x.mu.Lock()
	teamCount := 0
	for _, s := range x.sessions {
		if s.info.TeamID == in.TeamID {
			teamCount++
		}
	}
	if teamCount >= execMaxSessionsPerTeam {
		x.mu.Unlock()
		return nil, fmt.Errorf("%w: limit %d", ErrExecTeamLimit, execMaxSessionsPerTeam)
	}
	relay := x.relays[n.PlatformID]
	if relay == nil {
		x.mu.Unlock()
		return nil, fmt.Errorf("%w: node %s", ErrExecRelayOffline, n.PlatformID)
	}

	// ID 铸造在注册表同临界区：碰撞查重重铸（2^-128 碰撞，理论不可达，
	// 防御性循环——注册表是唯一查重面，进程内活体无台账）。
	var id string
	for {
		b := make([]byte, execIDRandBytes)
		if _, err := cryptorand.Read(b); err != nil {
			x.mu.Unlock()
			return nil, fmt.Errorf("mint exec session id: %w", err)
		}
		id = "exec-" + hex.EncodeToString(b)
		if x.sessions[id] == nil {
			break
		}
	}

	s := &execSession{
		info: ExecSessionInfo{
			ID:         id,
			TeamID:     in.TeamID,
			AppID:      in.AppID,
			Process:    in.Process,
			WorkloadID: workloadID,
			Instance:   target.Instance,
			NodeID:     n.PlatformID,
			Argv:       in.Argv,
			TTY:        in.TTY,
			CreatedAt:  x.clock.Now(),
		},
		out:   make(chan ExecServerFrame, execOutboundCap),
		relay: relay,
	}
	// 受理即起硬 TTL（K1，2026-10-14 P0-1）：未附着会话的回收路径。计时
	// 器字段读写一律持 s.mu（装配/触碰/收口三面同锁，防 timer 字段竞态）。
	s.mu.Lock()
	s.hardTimer = x.after(execHardTTL, func() {
		x.finish(s, &ExecErrFrame{Code: "session_expired", Message: "session hard TTL exceeded"})
	})
	s.mu.Unlock()
	x.sessions[s.info.ID] = s
	x.mu.Unlock()
	return &s.info, nil
}

// resolveExecTarget 是实例选择策略的单源（auto = 首个在跑，锚定 = 匹配）。
// Provider 哨兵归一为受理位信封（跨层哨兵词汇单源 capability：本包不
// import providers）。
func (x *Execs) resolveExecTarget(ctx context.Context, workloadID, instance string) (capability.ExecTargetInstance, error) {
	rows, err := x.execFace.ExecInstances(ctx, workloadID)
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

// AbandonSession 是受理侧回滚（api 层 commit 失败路径：审计未落则会话
// 不留——注册表活体与台账一致）。
func (x *Execs) AbandonSession(id string) {
	x.mu.Lock()
	s := x.sessions[id]
	if s != nil {
		delete(x.sessions, id)
	}
	x.mu.Unlock()
	if s != nil {
		x.finish(s, &ExecErrFrame{Code: "session_abandoned", Message: "session acceptance rolled back"})
	}
}

// AttachClient 附着消费端并驱动会话至收口（阻塞直至会话终止：exit/error
// 帧、TTL、中继失联或消费端整流断开）。第二附着者即刻 attach_conflict。
// 消费端收流（Recv EOF）只关载体 stdin——`fleetly exec` 形态依赖此语义
// （attach 后 CloseSend 读退出码）。
func (x *Execs) AttachClient(ctx context.Context, sessionID string, pipe ExecClientPipe) error {
	x.mu.Lock()
	s := x.sessions[sessionID]
	if s == nil {
		x.mu.Unlock()
		return ErrExecSessionNotFound
	}
	if s.attached {
		x.mu.Unlock()
		_ = pipe.Send(ExecServerFrame{Err: &ExecErrFrame{Code: "attach_conflict", Message: "session already has an attached client"}})
		return nil
	}
	s.attached = true
	relay := s.relay
	x.mu.Unlock()

	// 首帧元数据 + 向中继下发会话开帧（受理时已锁定路由锚）。
	if err := pipe.Send(ExecServerFrame{Meta: &ExecMetaFrame{Instance: s.info.Instance, NodeID: s.info.NodeID}}); err != nil {
		x.finish(s, nil)
		return nil
	}
	open := capability.RelaySessionOpen{
		SessionID: s.info.ID, WorkloadID: s.info.WorkloadID,
		Instance: s.info.Instance, Argv: s.info.Argv, TTY: s.info.TTY,
	}
	if err := x.send(relay, capability.RelayFrameOpen, s.info.ID, capability.EncodeRelayJSON(open)); err != nil {
		x.finish(s, relayGoneFrame(err))
		return nil
	}

	// 空闲计时（attach 起算）；硬 TTL 已在受理起算（CreateSession），不重
	// 复装配——重复装配会漏停旧计时器。装配持 s.mu。
	s.mu.Lock()
	s.idleTimer = x.after(execIdleTimeout, func() {
		x.finish(s, &ExecErrFrame{Code: "idle_timeout", Message: "no session traffic within the idle window"})
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
			x.touch(s)
			switch {
			case f.Resize != nil:
				pl := capability.ExecSizeWire{Cols: f.Resize.Cols, Rows: f.Resize.Rows}
				if x.send(relay, capability.RelayFrameResize, s.info.ID, capability.EncodeRelayJSON(pl)) != nil {
					return
				}
			default:
				if x.send(relay, capability.RelayFrameStdin, s.info.ID, f.Stdin) != nil {
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
			_ = x.send(relay, capability.RelayFrameStdinEOF, s.info.ID, nil)
			upDone = nil // nil 通道恒阻塞——此 case 只处理一次
		case f, ok := <-s.out:
			if !ok {
				return nil // 会话已收口（终结帧已投递或消费端不可达）
			}
			x.touch(s)
			if err := pipe.Send(f); err != nil {
				x.finish(s, nil)
				return nil
			}
			if f.Exit != nil || f.Err != nil {
				x.finish(s, nil)
				return nil
			}
		case <-ctx.Done():
			// 整流断开：尽力终止载体 exec 后收口（宽限内等 exit 帧）。
			_ = x.send(relay, capability.RelayFrameClose, s.info.ID, nil)
			deadline := time.After(execCloseGrace)
			for {
				select {
				case f, ok := <-s.out:
					if !ok {
						return nil
					}
					if f.Exit != nil || f.Err != nil {
						x.finish(s, nil)
						return nil
					}
				case <-deadline:
					x.finish(s, &ExecErrFrame{Code: "client_gone", Message: "client stream ended; relay close not acknowledged"})
					return nil
				}
			}
		}
	}
}

// touch 重置空闲计时（idleTimer 读写持 s.mu——timer 字段三面同锁的触碰
// 半边）。
func (x *Execs) touch(s *execSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Reset(execIdleTimeout)
	}
}

// finish 收口会话（幂等）：停计时、尽力向中继发终止、清注册表、投递终
// 结帧（有则）后关闭下行通道。
func (x *Execs) finish(s *execSession, last *ExecErrFrame) {
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

	x.mu.Lock()
	relay := s.relay
	if x.sessions[s.info.ID] == s {
		delete(x.sessions, s.info.ID)
	}
	x.mu.Unlock()
	if relay != nil {
		// 尽力终止（中继断线时静默；中继侧 exec 收口尽力而为）。
		_ = relay.w.WriteRelayFrame(capability.MarshalRelayFrame(capability.RelayFrameClose, s.info.ID, nil))
	}
	// 终结帧：closed 已置位，deliver 持锁复查即被排除——此处是唯一发送
	// 方，随后的 close 恒独占（K2 的另一半保证）。
	if last != nil {
		select {
		case s.out <- ExecServerFrame{Err: last}:
		default:
		}
	}
	close(s.out)
}

// ExecInstances 返回进程的在跑实例集快照（API 列表面；列表面诚实空——
// 无在跑实例是观测行而非错误，与受理面哨兵语义分立）。
func (x *Execs) ExecInstances(ctx context.Context, appID, process string) ([]capability.ExecTargetInstance, error) {
	if x.execFace == nil {
		return nil, ErrExecUnsupported
	}
	rows, err := x.execFace.ExecInstances(ctx, engineports.WorkloadID(appID, process))
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
func (x *Execs) NodePlatformIDByCarrier(ctx context.Context, carrierNodeID string) (string, error) {
	n, err := x.repos.Nodes.ByCarrier(ctx, carrierNodeID)
	if err != nil {
		return "", fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, carrierNodeID)
	}
	return n.PlatformID, nil
}

// ExecActiveTeamSessions 返回 Team 的在册会话数（受理面限额回显用）。
func (x *Execs) ExecActiveTeamSessions(teamID string) int {
	x.mu.Lock()
	defer x.mu.Unlock()
	n := 0
	for _, s := range x.sessions {
		if s.info.TeamID == teamID {
			n++
		}
	}
	return n
}

// ExecSessionByID 是会话查询面（票据兑换校验：票据绑会话）。
func (x *Execs) ExecSessionByID(id string) (*ExecSessionInfo, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	s, ok := x.sessions[id]
	if !ok {
		return nil, false
	}
	return &s.info, true
}

// ExecUnsupported 报告 Runtime 是否缺 exec 子面（api 受理面诚实失败锚）。
func (x *Execs) ExecUnsupported() bool { return x.execFace == nil }

// ValidateRelayCredential 校验中继凭证（/v1/relay 与 /v1/platform/binary
// 两个原生入口的 HTTP 层鉴权单源，H10 §4.5）：空 → 拒；内部 token（控制
// 面进程内中继）→ 过；sha256 在 node_relay_credentials 有行 → 过；否则
// 拒（join token 不再被接受）。存储故障与"无此凭证"必须可区分——前者
// 返回 ErrExecRelayStore（gateway 映射 503），后者 ErrExecRelayUnauthorized
// （401）。
func (x *Execs) ValidateRelayCredential(ctx context.Context, bearer string) error {
	if x.execFace == nil {
		return ErrExecUnsupported
	}
	if bearer == "" {
		return ErrExecRelayUnauthorized
	}
	if x.matchesInternal(bearer) {
		return nil
	}
	if _, err := x.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(bearer)); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return ErrExecRelayUnauthorized
		}
		return fmt.Errorf("%w: %w", ErrExecRelayStore, err)
	}
	return nil
}

// RelayInternalCredential 是回环中继的凭证回调面（NodeRelayOptions.
// Credential）：manager 回环与 k3s 每节点回环都从同一 engine 进程取内部
// token。carrierNodeID 是签名对齐参数（内部 token 可声明任意已锚定节点
// ——控制面进程自身信任面，不按 carrier 分面）；不写库、不入日志。
func (x *Execs) RelayInternalCredential(_ context.Context, _ string) (string, error) {
	return x.internalCredential()
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
