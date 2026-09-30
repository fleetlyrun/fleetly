package assembly

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lynx-go/grpcapi/gateway"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
)

// redactedInternalMessage 是 Internal/Unknown 的对外统一文案（原始消息只
// 落服务端日志，不泄内部细节）。
const redactedInternalMessage = "internal server error"

// newGatewayErrorHandler 构造 REST 错误统一出口（语义对齐 grpcapi
// gateway.NewErrorHandler，错误体换为 apperr 信封）：
//
//   - 信封还原：*apperr.Error 经 status detail 携带完整信封（errcode、
//     处置提示、docs、context）；无 detail 的机械错误走退化信封
//     （Internal/Unknown 回落在册 E_INTERNAL，其余 code 诚实留空）；
//   - HTTP 状态：gRPC code 机械映射（gateway.DefaultCodeToHTTP）；
//   - Internal/Unknown 脱敏：message 替换为通用文案，原文随 error_id 记日志；
//   - 429 从 RetryInfo detail 提取 Retry-After（向上取整，至少 1s）。
func newGatewayErrorHandler(logger *slog.Logger) runtime.ErrorHandlerFunc {
	return func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		if logger == nil {
			logger = slog.Default()
		}
		st, ok := status.FromError(err)
		if !ok || st == nil {
			logger.ErrorContext(ctx, "http error: non-status error converted to internal", "error", err)
			st = status.New(codes.Internal, redactedInternalMessage)
		}

		errorID := gateway.NewErrorID()
		envelope := apperr.EnvelopeFromGRPCStatus(st, errorID)
		if st.Code() == codes.Internal || st.Code() == codes.Unknown {
			logger.ErrorContext(ctx, "http response: internal error sanitized",
				"code", st.Code().String(), "original_message", st.Message(),
				"error_id", errorID, "path", r.URL.Path)
			envelope.Message = redactedInternalMessage
		}

		httpStatus := gateway.DefaultCodeToHTTP(st.Code())
		if httpStatus == http.StatusTooManyRequests {
			if retryAfter, ok := retryAfterSeconds(st); ok {
				w.Header().Set("Retry-After", retryAfter)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		m := marshaler
		if m == nil {
			m = gateway.NewMarshaler()
		}
		data, merr := m.Marshal(envelope)
		if merr != nil {
			logger.ErrorContext(ctx, "failed to encode error response", "error", merr, "error_id", errorID)
			return
		}
		if _, werr := w.Write(append(data, '\n')); werr != nil {
			logger.ErrorContext(ctx, "failed to write error response", "error", werr, "error_id", errorID)
		}
	}
}

// retryAfterSeconds 从 status details 提取 RetryInfo 建议退避秒数（向上
// 取整，至少 1s）；无 detail 返回 false。
func retryAfterSeconds(st *status.Status) (string, bool) {
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok && ri.GetRetryDelay().AsDuration() > 0 {
			secs := int64(math.Ceil(ri.GetRetryDelay().AsDuration().Seconds()))
			if secs < 1 {
				secs = 1
			}
			return strconv.FormatInt(secs, 10), true
		}
	}
	return "", false
}
