package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RuntimeExec 是 exec 子面（F3.2，ADR-0049）：进入运行中 Workload 载体的
// 会话。Provider 贡献三件编排器原语——管理侧实例解析（ExecTarget）、节点
// 侧载体执行（ExecWorkload）、节点中继（RunNodeRelay + 凭证校验）；
// 会话语义（受理/限额/审计/票据/路由/超时）住 engine，不经 Runtime。
// 未实现时 exec 受理诚实失败（E_EXEC_UNSUPPORTED，无降级路径）。
type RuntimeExec interface {
	// ExecTarget 管理侧实时解析 Workload 的在跑实例与所在节点（快照语义：
	// 每次调用直读任务列表——观测缓存不参与决策）。Instance 是编排器实例
	// 标识（平台不解析）；CarrierNodeID 经平台锚定表反查平台节点 ID。
	ExecTarget(ctx context.Context, workloadID string) (ExecTargetInstance, error)

	// ExecWorkload 节点侧执行：在本地 daemon 解析（WorkloadID, Instance）
	// 的载体并执行 argv。tty=true 时 PTY（Resize 订阅驱动尺寸；stdout 与
	// stderr 合流为 PTY 语义），否则管道（stdcopy 双路）。Stdin 由实现读至
	// EOF（客户端关闭即容器 stdin 关闭）；返回载体进程退出码（err 非空时
	// 退出码无效）。目标载体不在本节点时返回 ErrExecTargetGone。
	ExecWorkload(ctx context.Context, req ExecWorkloadRequest) (int, error)

	// RunNodeRelay 节点中继循环：拨号控制面 gateway 的 /v1/relay，
	// 握手上报载体节点身份，随后收发会话帧（多路复用，帧协议见 execwire
	// 单源）。阻塞直至 ctx 结束或不可恢复错误；调用方负责重连节拍。
	RunNodeRelay(ctx context.Context, o NodeRelayOptions) error

	// ExecClusterToken 校验中继凭证（swarm join token 对照——集群成员权
	// 等价，C3；manager 回环中继与节点中继同面）。/v1/relay 与
	// /v1/platform/binary 两个原生入口的鉴权单源。
	ExecClusterToken(ctx context.Context, token string) error
}

// ExecTargetInstance 是 ExecTarget 的解析产品。
type ExecTargetInstance struct {
	// Instance 是编排器实例标识（swarm task ID 观测；exec 请求经它限定
	// 具体载体，节点中继侧按平台标记 + 实例标记定位容器）。
	Instance string
	// CarrierNodeID 是实例所在节点的载体节点 ID（平台锚定表反查平台节点
	// ID——中继连接绑定的同一张表）。
	CarrierNodeID string
}

// ExecWorkloadRequest 是节点侧 exec 执行请求。
type ExecWorkloadRequest struct {
	WorkloadID string
	Instance   string
	// Argv 是完整命令（数组形态；零 shell——shellguard 射程延续）。
	Argv []string
	TTY  bool
	// Stdin 是会话输入流（读到 EOF 即关闭载体 stdin；客户端断开 = EOF）。
	Stdin io.Reader
	// Stdout/Stderr 是输出流（TTY 形态只有 Stdin/Stdout 合流语义——
	// 实现只写 Stdout，Stderr 恒空）。
	Stdout io.Writer
	Stderr io.Writer
	// Resize 是终端尺寸订阅（nil = 无终端关注；实现按帧驱动 exec resize，
	// 通道关闭即停止订阅）。
	Resize <-chan ExecSize
}

// ExecSize 是一次终端尺寸变化。
type ExecSize struct {
	Cols uint16
	Rows uint16
}

// NodeRelayOptions 是节点中继循环的输入。
type NodeRelayOptions struct {
	// GatewayURL 是控制面 gateway 基址（如 http://10.0.0.3:9081；节点
	// 零入站端口——中继出站拨号唯一通道）。
	GatewayURL string
	// JoinToken 取中继凭证（每次拨号前调用——manager 回环中继借此在
	// rotate 后自愈取新 token；worker 中继返回启动注入的固定值）。
	JoinToken func(ctx context.Context) (string, error)
	// Version 是握手上报的平台版本（节点视图回显；帧协议只增，
	// 滞后中继照常受理）。
	Version string
}

// ErrExecTargetGone 是 ExecWorkload 哨兵：目标载体不在本节点（受理后
// 实例漂移/容器退出）。engine 映射为会话 error 帧如实上抛。
var ErrExecTargetGone = errors.New("exec target carrier is not on this node")

// ErrExecNoRunning 是 ExecTarget 哨兵：Workload 无在跑实例（服务缺席/
// 任务非 running）。engine 归一为受理位 E_NOT_FOUND——跨层哨兵词汇单源
// 在 capability（engine 不 import providers）。
var ErrExecNoRunning = errors.New("no running instance for workload")

// ---- 中继帧协议（execwire 单源；assembly 的 /v1/relay 服务端与 swarm
// 节点中继客户端共用；engine 路由消费同构类型）。字节形态：[1B kind][26B
// session ULID][payload]（hello 例外：文本 JSON 首帧）。kind 与 payload
// 形态只增不改。 ----

// RelayFrameKind 是中继帧类别。
type RelayFrameKind byte

const (
	// Manager→relay。
	RelayFrameOpen     RelayFrameKind = 0x10 // payload JSON RelaySessionOpen
	RelayFrameStdin    RelayFrameKind = 0x11 // payload 原始字节
	RelayFrameResize   RelayFrameKind = 0x12 // payload JSON ExecSizeWire
	RelayFrameClose    RelayFrameKind = 0x13 // 无 payload（终止会话）
	RelayFrameStdinEOF RelayFrameKind = 0x14 // 无 payload（消费端收流：关载体 stdin——非终止）
	// Relay→manager。
	RelayFrameAck    RelayFrameKind = 0x20 // payload JSON RelaySessionAck
	RelayFrameStdout RelayFrameKind = 0x21 // payload 原始字节
	RelayFrameStderr RelayFrameKind = 0x22 // payload 原始字节
	RelayFrameExit   RelayFrameKind = 0x23 // payload JSON RelaySessionExit
	RelayFrameError  RelayFrameKind = 0x24 // payload JSON RelaySessionError
)

// RelayHello 是中继首帧（WS 文本帧，JSON）。
type RelayHello struct {
	Type          string `json:"type"` // 恒 "hello"
	CarrierNodeID string `json:"carrier_node_id"`
	RelayVersion  string `json:"relay_version"`
}

// RelaySessionOpen 是 manager→relay 的会话开帧载荷。
type RelaySessionOpen struct {
	SessionID  string   `json:"session_id"`
	WorkloadID string   `json:"workload_id"`
	Instance   string   `json:"instance"`
	Argv       []string `json:"argv"`
	TTY        bool     `json:"tty"`
}

// RelaySessionAck 是 relay→manager 的会话受理回执。
type RelaySessionAck struct {
	Instance string `json:"instance"`
}

// RelaySessionExit 是 relay→manager 的退出帧载荷。
type RelaySessionExit struct {
	Code int `json:"code"`
}

// RelaySessionError 是 relay→manager 的错误帧载荷（收口即会话终止）。
type RelaySessionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ExecSizeWire 是 resize 帧载荷（线上 JSON 形态）。
type ExecSizeWire struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// RelayFrame 是一条解出的中继帧（hello 之外）。
type RelayFrame struct {
	Kind      RelayFrameKind
	SessionID string
	Payload   []byte
}

// ulidLen 是会话 ID 定长（26 ASCII 字节）。
const ulidLen = 26

// MarshalRelayFrame 编码一条中继帧（hello 不走此函数——文本帧独立）。
func MarshalRelayFrame(kind RelayFrameKind, sessionID string, payload []byte) []byte {
	out := make([]byte, 0, 1+ulidLen+len(payload))
	out = append(out, byte(kind))
	out = append(out, sessionID...)
	return append(out, payload...)
}

// ParseRelayFrame 解码一条二进制中继帧。
func ParseRelayFrame(b []byte) (RelayFrame, error) {
	if len(b) < 1+ulidLen {
		return RelayFrame{}, fmt.Errorf("relay frame too short: %d bytes", len(b))
	}
	f := RelayFrame{Kind: RelayFrameKind(b[0]), SessionID: string(b[1 : 1+ulidLen]), Payload: b[1+ulidLen:]}
	switch f.Kind {
	case RelayFrameOpen, RelayFrameStdin, RelayFrameResize, RelayFrameClose, RelayFrameStdinEOF,
		RelayFrameAck, RelayFrameStdout, RelayFrameStderr, RelayFrameExit, RelayFrameError:
		return f, nil
	default:
		return RelayFrame{}, fmt.Errorf("unknown relay frame kind 0x%02x", byte(f.Kind))
	}
}

// DecodeRelayJSON 是控制帧载荷的 JSON 解码助手（泛型收窄到载荷类型）。
func DecodeRelayJSON[T any](payload []byte) (T, error) {
	var v T
	if err := json.Unmarshal(payload, &v); err != nil {
		return v, fmt.Errorf("relay frame payload: %w", err)
	}
	return v, nil
}

// EncodeRelayJSON 是控制帧载荷的 JSON 编码助手。
func EncodeRelayJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// 控制载荷都是受控结构体，编码失败 = 编程错误。
		panic(fmt.Sprintf("relay frame payload encode: %v", err))
	}
	return b
}
