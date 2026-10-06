package assembly

// gateway 的 exec 原生入口（F3.2，ADR-0049）三件：
//
//   - GET /v1/relay          节点中继代理 WS（Bearer = swarm join token，
//     集群成员权等价 C3；首帧 hello 绑定载体节点身份，随后多路复用
//     capability 帧协议的二进制形态）。
//   - GET /v1/exec/stream    会话消费端 WS（Console 终端页；浏览器 WS 无
//     自定义头——query 携带秒级单用途票据，ADR-0026 exec 版；帧形态 =
//     [1B kind][payload] 的会话流信封，与 gRPC oneof 同构）。
//   - GET /v1/platform/binary  控制面二进制下载（EnrollKit.AgentCommand 的
//     curl 目标；鉴权与 /v1/relay 同一 join token）。
//
// 与 uploads/hooks/events SSE 同一挂法族：root mux 精确路径 + gateway
// 回落；不进 gateway 注解面（swagger 不含——Console 侧手写客户端，
// webhook/uploads 先例）。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
)

// 原生入口路径常量（挂载清单真源）。
const (
	relayURLPath       = "/v1/relay"
	execStreamURLPath  = "/v1/exec/stream"
	platformBinaryPath = "/v1/platform/binary"
)

// 会话流 WS 信封（Console 终端消费径；[1B kind][payload]，与 gRPC
// StreamExecSession 的 oneof 同构映射；kind 只增）。
const (
	execFrameStdin  byte = 0x01 // payload 原始字节
	execFrameStdout byte = 0x02
	execFrameStderr byte = 0x03
	execFrameResize byte = 0x04 // payload JSON {cols,rows}
	execFrameExit   byte = 0x05 // payload JSON {code}
	execFrameError  byte = 0x06 // payload JSON {code,message}
	execFrameMeta   byte = 0x07 // payload JSON {instance,node_id}
)

// relayAcceptOptions 收紧 WS 握手（无压缩——帧是二进制协议；来源不限：
// 节点出站连接的目标地址由 AgentCommand 携带）。
var relayAcceptOptions = websocket.AcceptOptions{
	CompressionMode: websocket.CompressionDisabled,
	OriginPatterns:  []string{"*"}, // 节点代理非浏览器（无 Origin 面）
}

// mountRelay 挂载节点中继代理入口。
func mountRelay(h http.Handler, eng *engine.Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != relayURLPath {
			h.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeExecStatus(w, http.StatusMethodNotAllowed, "get_only", "the relay endpoint accepts WebSocket GET only")
			return
		}
		serveRelayAgent(w, r, eng)
	})
}

// serveRelayAgent 服务一条代理连接：凭证（HTTP 层，升级前 401）→ 升级 →
// hello（文本 JSON）→ engine 附着（凭证复验 + 节点绑定）→ 读循环投递帧。
func serveRelayAgent(w http.ResponseWriter, r *http.Request, eng *engine.Engine) {
	// 凭证先于升级校验：坏凭证对 HTTP 面呈现 401（匿名面最小事实），
	// 不进 WS 协议层。
	if err := eng.ExecValidateClusterToken(r.Context(), bearerToken(r)); err != nil {
		writeExecStatus(w, http.StatusUnauthorized, "bad_credential", "credential does not match an active swarm join token")
		return
	}
	conn, err := websocket.Accept(w, r, &relayAcceptOptions)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "relay closing") //nolint:errcheck // 关闭面错误无处置

	ctx := r.Context()
	conn.SetReadLimit(1 << 20)

	// hello 首帧（带界读：握手不挂死连接池）。
	hctx, hcancel := context.WithTimeout(ctx, 15*time.Second)
	_, helloRaw, err := conn.Read(hctx)
	hcancel()
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "hello expected")
		return
	}
	var hello capability.AgentHello
	if err := json.Unmarshal(helloRaw, &hello); err != nil || hello.Type != "hello" || hello.CarrierNodeID == "" {
		_ = conn.Close(websocket.StatusPolicyViolation, "malformed hello")
		return
	}
	token := bearerToken(r)
	feed, detach, err := eng.RelayAgentAttach(ctx, token, hello.CarrierNodeID, hello.AgentVersion, &wsAgentWriter{conn: conn})
	if err != nil {
		slog.Warn("relay agent attach rejected", "err", err.Error())
		_ = conn.Close(websocket.StatusPolicyViolation, "attach rejected")
		return
	}
	defer detach()

	// 读循环：二进制帧 → engine 投递；文本帧忽略（hello 之外无文本面）。
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageBinary {
			continue
		}
		f, err := capability.ParseAgentFrame(data)
		if err != nil {
			slog.Warn("relay agent frame dropped", "err", err.Error())
			continue
		}
		feed(f)
	}
}

// wsAgentWriter 把代理写端适配为 engine.RelayAgentWriter（写串行化经
// per-frame 互斥；coder/websocket 单写者纪律——读循环与下行写分属两个
// goroutine 面）。
type wsAgentWriter struct {
	conn *websocket.Conn
}

func (w *wsAgentWriter) WriteAgentFrame(b []byte) error {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
	defer cancel()
	return w.conn.Write(wctx, websocket.MessageBinary, b)
}

// mountExecStream 挂载会话消费端入口（Console 终端）。
func mountExecStream(h http.Handler, src *fleetlygrpc.ExecStreamSource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != execStreamURLPath {
			h.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeExecStatus(w, http.StatusMethodNotAllowed, "get_only", "the exec stream endpoint accepts WebSocket GET only")
			return
		}
		serveExecStream(w, r, src)
	})
}

// serveExecStream 服务一条会话消费端连接：票据兑换（绑会话、单用途）→
// engine 会话泵驱动至收口。
func serveExecStream(w http.ResponseWriter, r *http.Request, src *fleetlygrpc.ExecStreamSource) {
	sessionID := r.URL.Query().Get("session_id")
	ticket := r.URL.Query().Get("ticket")
	if sessionID == "" || ticket == "" {
		writeExecStatus(w, http.StatusUnauthorized, "bad_ticket", "session_id and ticket query parameters are required")
		return
	}
	if !src.RedeemExecTicket(sessionID, ticket) {
		writeExecStatus(w, http.StatusUnauthorized, "bad_ticket", "ticket is invalid, expired, or already used")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		OriginPatterns:  []string{"*"}, // 终端页与 API 同源部署；跨源部署形态由反代承担
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "stream closing") //nolint:errcheck // 关闭面错误无处置
	conn.SetReadLimit(1 << 20)

	pipe := &execWSClient{conn: conn}
	if err := src.Engine().AttachClient(r.Context(), sessionID, pipe); err != nil {
		// 会话不存在等受理面错误：作为 error 帧投递（票据已兑——如实收口）。
		_ = pipe.Send(engine.ExecServerFrame{Err: &engine.ExecErrFrame{Code: "session_unavailable", Message: err.Error()}})
	}
}

// execWSClient 是 Console WS 的会话消费端适配器。
type execWSClient struct {
	conn *websocket.Conn
}

func (c *execWSClient) Recv() (engine.ExecClientFrame, error) {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return engine.ExecClientFrame{}, err
		}
		if len(data) == 0 {
			continue
		}
		switch data[0] {
		case execFrameStdin:
			return engine.ExecClientFrame{Stdin: data[1:]}, nil
		case execFrameResize:
			var s struct {
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if err := json.Unmarshal(data[1:], &s); err != nil {
				continue
			}
			return engine.ExecClientFrame{Resize: &capability.ExecSize{Cols: s.Cols, Rows: s.Rows}}, nil
		default:
			// 未知 kind 忽略（信封只增；旧客户端兼容面）。
		}
	}
}

func (c *execWSClient) Send(f engine.ExecServerFrame) error {
	var buf []byte
	switch {
	case f.Meta != nil:
		payload, _ := json.Marshal(struct {
			Instance string `json:"instance"`
			NodeID   string `json:"node_id"`
		}{f.Meta.Instance, f.Meta.NodeID})
		buf = append([]byte{execFrameMeta}, payload...)
	case f.Exit != nil:
		payload, _ := json.Marshal(struct {
			Code int32 `json:"code"`
		}{*f.Exit})
		buf = append([]byte{execFrameExit}, payload...)
	case f.Err != nil:
		payload, _ := json.Marshal(f.Err)
		buf = append([]byte{execFrameError}, payload...)
	case f.Stderr != nil:
		buf = append([]byte{execFrameStderr}, f.Stderr...)
	default:
		buf = append([]byte{execFrameStdout}, f.Stdout...)
	}
	wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.conn.Write(wctx, websocket.MessageBinary, buf)
}

// mountPlatformBinary 挂载控制面二进制下载入口（AgentCommand 的 curl
// 目标；Bearer = join token 与 /v1/relay 同源）。
func mountPlatformBinary(h http.Handler, eng *engine.Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != platformBinaryPath {
			h.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			writeExecStatus(w, http.StatusMethodNotAllowed, "get_only", "the binary endpoint accepts GET only")
			return
		}
		if err := eng.ExecValidateClusterToken(r.Context(), bearerToken(r)); err != nil {
			writeExecStatus(w, http.StatusUnauthorized, "bad_credential", "credential does not match an active swarm join token")
			return
		}
		self, err := os.Executable()
		if err != nil {
			writeExecStatus(w, http.StatusInternalServerError, "binary_unavailable", "control-plane binary path could not be resolved")
			return
		}
		f, err := os.Open(self) //nolint:gosec // 路径来自 os.Executable，非外部输入
		if err != nil {
			writeExecStatus(w, http.StatusInternalServerError, "binary_unavailable", "control-plane binary could not be opened")
			return
		}
		defer f.Close() //nolint:errcheck // 只读句柄
		st, err := f.Stat()
		if err != nil {
			writeExecStatus(w, http.StatusInternalServerError, "binary_unavailable", "control-plane binary could not be stat'd")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)
	})
}

// bearerToken 提取 Authorization: Bearer 值（缺失空串）。
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	v := r.Header.Get("Authorization")
	if len(v) > len(prefix) && v[:len(prefix)] == prefix {
		return v[len(prefix):]
	}
	return ""
}

// writeExecStatus 是原生入口的统一最小错误体（SSE 同款形态：非 gateway
// 信封——匿名面只呈现最小事实）。
func writeExecStatus(w http.ResponseWriter, code int, short, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": short, "message": message})
}
