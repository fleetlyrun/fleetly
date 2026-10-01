package assembly

// gateway 的 SSE 事件入口（F1.2 / ADR-0026）：GET /v1/events/follow?
// ticket=...&after_seq=N&follow=1。EventSource 不能设自定义头——凭证经
// IssueEventTicket 换一次性短时票据后以 query 参数携带（秒级 TTL、单
// 用途、限本路径；泄漏面受控）。SSE 帧化（text/event-stream）住本文件，
// 订阅与票据兑换经 EventStreamSource 与 gRPC StreamEvents 同源。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/api/fleetlygrpc"
)

// eventsSSEPath 是原生 SSE 挂载路径（精确匹配，先于 gateway 的 /v1/...）。
const eventsSSEPath = "/v1/events/follow"

// ssePingInterval 是空闲连接的 ping 帧（SSE 注释帧 ": ping"；代理的空闲
// 超时普遍在 30~60s，15s 留足余量）。
const ssePingInterval = 15 * time.Second

// newEventsSSEHandler 构造 SSE 原生入口（src 与 gRPC 面同源）。
func newEventsSSEHandler(src *fleetlygrpc.EventStreamSource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeHookStatus(w, http.StatusMethodNotAllowed, "get_only", "the SSE event stream accepts GET only")
			return
		}
		// 票据即凭证（单用途）：缺失/无效/已用统一 401（对匿名面不区分）。
		if !src.RedeemTicket(r.URL.Query().Get("ticket")) {
			writeHookStatus(w, http.StatusUnauthorized, "bad_ticket",
				"mint a fresh one-time ticket via POST /v1/events/ticket (Bearer) and pass it as ?ticket=")
			return
		}
		afterSeq, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
		if err != nil {
			afterSeq = 0
		}
		follow := r.URL.Query().Get("follow") != "0"

		// 断档预检先于 200/SSE 头（410 走统一错误信封；状态行一旦发出，
		// 错误只能以 SSE error 帧收口，410 语义就丢了）。
		if err := src.Gone(r.Context(), afterSeq); err != nil {
			newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeHookStatus(w, http.StatusInternalServerError, "no_streaming", "the connection does not support streaming")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// 订阅在 goroutine 跑、主循环负责 ping 帧——订阅核心阻塞在轮询拍
		// 上，空闲 ping 必须与之交错。
		events := make(chan *telemetryv1.Event, 16)
		done := make(chan error, 1)
		subCtx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			done <- src.Subscribe(subCtx, afterSeq, follow, func(ev *telemetryv1.Event) error {
				select {
				case events <- ev:
					return nil
				case <-subCtx.Done():
					return subCtx.Err()
				}
			})
		}()

		wrote := false
		ping := time.NewTicker(ssePingInterval)
		defer ping.Stop()
		for {
			select {
			case ev := <-events:
				if err := writeSSEEvent(w, ev); err != nil {
					return
				}
				wrote = true
				flusher.Flush()
			case <-ping.C:
				// SSE 注释帧：对 EventSource 透明，仅喂代理。
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case err := <-done:
				if err == nil {
					return
				}
				// 流开始前失败（断档等）走 HTTP 状态面；开始后只能以 SSE
				// error 帧收口（状态行已发出）。
				if !wrote {
					newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
				} else {
					_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", sseErrorPayload(err)) //nolint:errcheck // 客户端已断则无处置面
					flusher.Flush()
				}
				return
			}
		}
	})
}

// writeSSEEvent 写一帧事件（id=seq 供 Last-Event-ID 重连续用；data 为
// protojson snake_case，与 --json 同口径；json.Compact 归一空白）。
func writeSSEEvent(w http.ResponseWriter, ev *telemetryv1.Event) error {
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(ev)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.GetSeq(), ev.GetName(), buf.Bytes()); err != nil {
		return err
	}
	return nil
}

// sseErrorPayload 是流中错误的载荷（apperr 码优先，机械错误给类别）。
func sseErrorPayload(err error) string {
	if e, ok := err.(*apperr.Error); ok {
		return `"` + e.Code() + `"`
	}
	return `"stream_error"`
}

var _ proto.Message = (*telemetryv1.Event)(nil)
