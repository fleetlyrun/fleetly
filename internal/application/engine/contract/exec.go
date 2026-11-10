// exec.go 是 exec 会话域应用模块的核/模块契约面（engine 去应用模块 import
// 第六批，2026-11-10）：engine 核只持 Exec 接口与构造端口袋，具体装配器由
// 组合根经 engine.Deps.ExecFactory 注入——核不再 import exec 会话应用模块
// 包。值类型/哨兵随契约面下沉（fleetlygrpc 流适配器、assembly WS/relay 适配
// 器与 apitest 的既名引用经 engine 别名零改）；端口形态全部落在 domain 面
// （capability/state/repo 签名），契约叶子保持零应用模块类型（ADR-0088
// 决策 7 裁定二）。
package contract

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// exec 会话域哨兵（api 层映射稳定 errcode）。
var (
	// ErrExecUnsupported 是 Runtime 缺 exec 扩展接口（E_EXEC_UNSUPPORTED）。
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

// RelayStatus 是节点中继在连状态（节点视图回显面）。
type RelayStatus struct {
	Online  bool
	Version string
}

// Exec 是 exec 会话域应用模块的核消费面：lifecycle 的回环中继挂载经
// ExecFace/RelayInternalCredential，gRPC exec 面、assembly WS 会话适配与
// relay 原生入口经其余方法（消费点零改）。
type Exec interface {
	Module
	// ExecFace 返回 exec 扩展接口 typed 持有（nil = Runtime 缺 exec 能力，
	// 受理面诚实失败 E_EXEC_UNSUPPORTED）。
	ExecFace() capability.RuntimeExec
	// RelayInternalCredential 是回环中继的凭证回调面（NodeRelayOptions.
	// Credential；carrierNodeID 是签名对齐参数）。
	RelayInternalCredential(ctx context.Context, carrierNodeID string) (string, error)
	// ExecUnsupported 报告 Runtime 是否缺 exec 扩展接口（api 受理面锚）。
	ExecUnsupported() bool
	// CreateSession 受理会话：实时解析实例与节点 → 限额 → 注册。
	CreateSession(ctx context.Context, in CreateExecInput) (*ExecSessionInfo, error)
	// AbandonSession 是受理侧回滚（api 层 commit 失败路径）。
	AbandonSession(id string)
	// AttachClient 附着消费端并驱动会话至收口（阻塞直至会话终止）。
	AttachClient(ctx context.Context, sessionID string, pipe ExecClientPipe) error
	// ExecInstances 返回进程的在跑实例集快照（API 列表面；诚实空）。
	ExecInstances(ctx context.Context, appID, process string) ([]capability.ExecTargetInstance, error)
	// NodePlatformIDByCarrier 是载体节点 → 平台节点 ID 的反查面。
	NodePlatformIDByCarrier(ctx context.Context, carrierNodeID string) (string, error)
	// ExecSessionByID 是会话查询面（票据兑换校验：票据绑会话）。
	ExecSessionByID(id string) (*ExecSessionInfo, bool)
	// ValidateRelayCredential 校验中继凭证（/v1/relay 与 /v1/platform/
	// binary 两个原生入口的 HTTP 层鉴权单源）。
	ValidateRelayCredential(ctx context.Context, bearer string) error
	// RelayAttach 是中继连接附着（凭证绑定 + 节点锚定校验序）；feed 供
	// 适配器上行投递，detach 在连接断开时调用。
	RelayAttach(ctx context.Context, token, carrierNodeID, relayVersion string, w RelayWriter) (feed func(capability.RelayFrame), detach func(), err error)
	// DropRelay 断开某平台节点的在连中继（重铸/吊销的即时失效面）。
	DropRelay(platformID string)
	// DropAllRelays 断开全部在连中继（rotate 全量作废面）。
	DropAllRelays()
	// RelayStatuses 返回全量在连快照（ListNodes 回显消费）。
	RelayStatuses() map[string]RelayStatus
}

// ExecPorts 是 exec 会话装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值；形态与 exec 会话应用模块的 Deps 逐字段同形，契约叶子只
// 依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type ExecPorts struct {
	// Log 是域内日志器（中继顶替/失联两处 Info）。
	Log *slog.Logger
	// ExecFace 是 exec 扩展接口 typed 持有（nil = 受理诚实失败
	// E_EXEC_UNSUPPORTED——无降级路径）。
	ExecFace capability.RuntimeExec
	// Repos 是只读聚合目录（节点锚定/中继凭证行——transact.Stores 整体
	// 注入，ADR-0088 决策 3）。
	Repos transact.Stores
	// Clock 是受理时间戳面。
	Clock state.Clock
	// EmitLedger 是事实发射端口（核 emitLedger 方法值；签名单源本叶子
	// EmitLedger，见 facts.go）。
	EmitLedger EmitLedger
}

// ExecFactory 由组合根注入（engine.Deps.ExecFactory），把端口袋装配成
// Exec。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时 exec
// 面停用：门面 nil-safe 返回 ErrExecUnsupported 族/零值，lifecycle 跳过
// 回环中继挂载。
type ExecFactory func(ExecPorts) Exec
