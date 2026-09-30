package assembly

// gateway 的 webhook 原生入口（F0.13）：POST /v1/hooks/<token>——HMAC 必须
// 对原始请求体字节计算，protojson 解码不可用，故不经 grpc-gateway 路由，
// 直挂 http.Handler 并经共享 gRPC conn 调用 ReceiveWebhook（拦截器链照常
// 执法）。Token 只作为路径段与验签材料存在：本文件不落任何含 token 的
// 日志（gateway 无逐请求访问日志；错误路径只记 delivery 标识）。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
)

// hooksReceiver 是接收面最小客户端面（adapter 只消费 ReceiveWebhook；
// 类型化客户端天然满足——测试注入窄假实现，与传输解耦）。
type hooksReceiver interface {
	ReceiveWebhook(ctx context.Context, in *deliveryv1.ReceiveWebhookRequest, opts ...grpc.CallOption) (*deliveryv1.ReceiveWebhookResponse, error)
}

// hookPayloadLimit 是 webhook 请求体上限（对齐 GitHub 25MiB 上限）。
const hookPayloadLimit = 25 << 20

// newHooksHandler 构造原生 webhook handler（client 是接收面 gRPC 客户端
// ——gateway 用共享 conn 的类型化包装；测试注入假客户端）。
func newHooksHandler(client hooksReceiver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHookStatus(w, http.StatusMethodNotAllowed, "post_only", "only POST is accepted at the hook URL")
			return
		}
		token := strings.Trim(strings.TrimPrefix(r.URL.Path, fleetlygrpc.HooksURLPrefix), "/")
		if token == "" || strings.Contains(token, "/") {
			writeHookStatus(w, http.StatusNotFound, "not_found", "hook URL is "+fleetlygrpc.HooksURLPrefix+"<token>")
			return
		}
		payload, err := io.ReadAll(io.LimitReader(r.Body, hookPayloadLimit+1))
		if err != nil {
			writeHookStatus(w, http.StatusBadRequest, "read_failed", "request body could not be read")
			return
		}
		if len(payload) > hookPayloadLimit {
			writeHookStatus(w, http.StatusRequestEntityTooLarge, "too_large", "payload exceeds the 25 MiB limit")
			return
		}
		resp, err := client.ReceiveWebhook(r.Context(), &deliveryv1.ReceiveWebhookRequest{
			Token:     token,
			Payload:   payload,
			Event:     r.Header.Get("X-GitHub-Event"),
			Delivery:  r.Header.Get("X-GitHub-Delivery"),
			Signature: r.Header.Get("X-Hub-Signature-256"),
		})
		if err != nil {
			// 统一错误信封出口（与 gateway 其余 REST 面同构；token 不进
			// 日志或响应）。
			newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
			return
		}
		writeHookProtoJSON(w, http.StatusOK, resp)
	})
}

// mountHooks 把原生 handler 挂到 grpc-gateway 之前（/v1/hooks/ 前缀独占，
// 其余路径回落 gateway mux）。
func mountHooks(gw http.Handler, hooks http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(fleetlygrpc.HooksURLPrefix, hooks)
	mux.Handle("/", gw)
	return mux
}

// writeHookStatus 写最小 JSON 状态体（原生路径的协议层错误不携带 detail）。
func writeHookStatus(w http.ResponseWriter, code int, status, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, `{"status":"`+status+`","reason":"`+reason+`"}`+"\n") //nolint:errcheck // 响应写失败无处置面
}

// writeHookProtoJSON 以 protojson snake_case 写响应（与 gateway 统一
// marshaler 同口径的 UseProtoNames；json.Compact 归一 protojson 的不稳定
// 空白——detrand 撒种防御，稳定输出对调用方是契约）。
func writeHookProtoJSON(w http.ResponseWriter, code int, m proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	if json.Compact(&buf, data) != nil {
		buf.Reset()
		buf.Write(data)
	}
	_, _ = w.Write(append(buf.Bytes(), '\n')) //nolint:errcheck // 响应写失败无处置面
}
