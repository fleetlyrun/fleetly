package assembly

// gateway 的上传产物原生入口（F3.5，ADR-0019 附录 A 留白收口）：POST
// /v1/uploads?project_id=<id>，请求体 = 未压缩 tar 字节流。UploadSource 是
// client-streaming 写面，无 HTTP 注解面（grpc-gateway 不支持）——原生
// handler 把请求体流式转发为帧序列（首帧 meta + 余帧 ≤1MiB chunk），
// 服务端拦截器链照常执法（authn→freeze；幂等豁免与限额在 A.2 已裁）。
//
// REST 形态裁决：raw body（application/x-tar）而非 multipart——上传物是
// 单部分流，multipart 边界解析零收益且要求全量缓冲策略；Console fetch
// 可原生发送任意请求体（chunked 或 Content-Length 皆可）。Content-Type
// 不强校验：服务端只认内容寻址，媒体类型是建议不是契约。
//
// GET /v1/uploads（ListUploads 注解面）不被本挂法遮蔽：精确路径按方法
// 分派，非 POST 原样回落 gateway。

import (
	"context"
	"io"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
)

// uploadsURLPrefix 是上传产物 REST 入口的精确路径（与 ListUploads 注解面
// 同路径、按方法分派）。
const uploadsURLPrefix = "/v1/uploads"

// uploadChunkSize 是请求体到 gRPC 帧的切分粒度（与 CLI 上传同量级——
// 单条消息远小于 gRPC 收包上限，ADR-0019 附录 A.3）。
const uploadChunkSize = 1 << 20

// uploadsStreamer 是上传面最小客户端面（adapter 只消费 UploadSource；
// 测试注入窄假实现，与传输解耦——hooksReceiver 同款）。
type uploadsStreamer interface {
	UploadSource(ctx context.Context, opts ...grpc.CallOption) (deliveryv1.BuildsService_UploadSourceClient, error)
}

// newUploadsHandler 构造上传原生 handler（next 是 gateway——非 POST 方法
// 原样回落，注解面 GET 不被遮蔽）。
func newUploadsHandler(client uploadsStreamer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		projectID := r.URL.Query().Get("project_id")
		if projectID == "" {
			writeHookStatus(w, http.StatusBadRequest, "missing_project", "the project_id query parameter is required")
			return
		}
		// Authorization 头进 gRPC metadata（grpc-gateway 注解面对该头有
		// 内置透传；原生路径手动补齐同一条通道）。
		callCtx := r.Context()
		if a := r.Header.Get("Authorization"); a != "" {
			callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", a)
		}
		stream, err := client.UploadSource(callCtx)
		if err != nil {
			newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
			return
		}
		if err := stream.Send(&deliveryv1.UploadSourceRequest{
			Part: &deliveryv1.UploadSourceRequest_Meta{
				Meta: &deliveryv1.UploadSourceMeta{ProjectId: projectID},
			},
		}); err != nil {
			// 首帧拒绝（冻结/授权）经 CloseAndRecv 取回真实 RPC 状态——
			// 与 CLI 同款，错误不被客户端 EOF 掩蔽。
			if resp, cerr := stream.CloseAndRecv(); cerr != nil {
				newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, cerr)
				return
			} else if resp != nil {
				writeHookProtoJSON(w, http.StatusOK, resp)
				return
			}
			newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
			return
		}
		buf := make([]byte, uploadChunkSize)
		for {
			n, rerr := io.ReadFull(r.Body, buf)
			if n > 0 {
				chunk := buf[:n]
				if serr := stream.Send(&deliveryv1.UploadSourceRequest{
					Part: &deliveryv1.UploadSourceRequest_Chunk{Chunk: chunk},
				}); serr != nil {
					// 服务端中途拒绝（限额等）：Send 多为 io.EOF，真实状态
					// 经 CloseAndRecv 取回（首帧同款，错误不被掩蔽）。
					if resp, cerr := stream.CloseAndRecv(); cerr != nil {
						newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, cerr)
						return
					} else if resp != nil {
						writeHookProtoJSON(w, http.StatusOK, resp)
						return
					}
					newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, serr)
					return
				}
			}
			if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
				break
			}
			if rerr != nil {
				_ = stream.CloseSend()
				writeHookStatus(w, http.StatusBadRequest, "read_failed", "the request body could not be read")
				return
			}
		}
		resp, err := stream.CloseAndRecv()
		if err != nil {
			newGatewayErrorHandler(nil)(r.Context(), nil, nil, w, r, err)
			return
		}
		writeHookProtoJSON(w, http.StatusOK, resp)
	})
}

// mountUploads 把上传入口挂在 root mux 的精确路径（先于 gateway 的 "/"
// 回落；与 webhook/SSE 原生挂法同族）。
func mountUploads(gw http.Handler, uploads http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(uploadsURLPrefix, uploads)
	mux.Handle("/", gw)
	return mux
}
