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

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
)

// redactedInternalMessage 是 Internal/Unknown 的对外统一文案（原始消息只
// 落服务端日志，不泄内部细节）。
const redactedInternalMessage = "internal server error"

// errcodeToHTTP 是 REST 面错误码级 HTTP 状态映射（显式钉扎，全量覆盖在册
// 注册表；assembly 守卫测试钉住"新码必须显式决策"）。为什么不用 gRPC code
// 机械映射兜底全部：HTTP 状态的语义真源是错误码语义，不是传输层 gRPC code
// ——E_CONFLICT 族承载在 FailedPrecondition 上（gRPC 语义正确），机械映射
// 却把它打成 400，而"请求合法但与资源当前状态冲突"在 HTTP 语义上就是 409
// （批 0 复核回归：REST/SDK 按 409 写的重试逻辑不得静默破坏）。逐码盘点：
//
//   - 409 Conflict（状态冲突族）：E_ALREADY_EXISTS（唯一键已存在）、
//     E_IDEMPOTENCY_KEY_CONFLICT（幂等键异体/在途/双源不一致）、
//     E_CONFLICT / E_NOT_CANCELLABLE / E_NO_BASELINE / E_SECRET_UNAVAILABLE
//     （请求合法、当前状态不容——cancel/解绑/补件后可重试）；
//   - 400/404/429/401/403/500 与机械映射一致（本就对，钉扎防漂移）：
//     E_INVALID_ARGUMENT→400、E_NOT_FOUND→404、E_QUEUE_FULL 与
//     E_QUOTA_EXCEEDED→429（Retry-After 提取仍按 429 生效）、
//     E_UNAUTHENTICATED 与 E_INVALID_SIGNATURE→401、E_FORBIDDEN 与
//     E_INVALID_INVITATION→403、E_INTERNAL→500。
//
// gRPC code 的 gateway.DefaultCodeToHTTP 继续兜底无信封的机械错误
// （Canceled→499、DeadlineExceeded→504 等）。
var errcodeToHTTP = map[string]int{
	"E_ALREADY_EXISTS":           http.StatusConflict,
	"E_CONFLICT":                 http.StatusConflict,
	"E_FORBIDDEN":                http.StatusForbidden,
	"E_IDEMPOTENCY_KEY_CONFLICT": http.StatusConflict,
	"E_INTERNAL":                 http.StatusInternalServerError,
	"E_INVALID_ARGUMENT":         http.StatusBadRequest,
	"E_INVALID_INVITATION":       http.StatusForbidden,
	"E_INVALID_SIGNATURE":        http.StatusUnauthorized,
	"E_NOT_CANCELLABLE":          http.StatusConflict,
	"E_NOT_FOUND":                http.StatusNotFound,
	"E_NO_BASELINE":              http.StatusConflict,
	"E_QUEUE_FULL":               http.StatusTooManyRequests,
	"E_QUOTA_EXCEEDED":           http.StatusTooManyRequests,
	"E_SECRET_UNAVAILABLE":       http.StatusConflict,
	"E_UNAUTHENTICATED":          http.StatusUnauthorized,
}

// resolveHTTPStatus 定 HTTP 状态：信封还原出在册错误码时以 errcodeToHTTP
// 为准（语义真源在错误码），退化信封/机械错误回落 gRPC code 机械映射。
func resolveHTTPStatus(st *status.Status, envelope *sharedv1.ErrorResponse) int {
	if envelope != nil {
		if s, ok := errcodeToHTTP[envelope.GetCode()]; ok {
			return s
		}
	}
	return gateway.DefaultCodeToHTTP(st.Code())
}

// newGatewayErrorHandler 构造 REST 错误统一出口（语义对齐 grpcapi
// gateway.NewErrorHandler，错误体换为 apperr 信封）：
//
//   - 信封还原：*apperr.Error 经 status detail 携带完整信封（errcode、
//     处置提示、docs、context）；无 detail 的机械错误走退化信封
//     （Internal/Unknown 回落在册 E_INTERNAL，其余 code 诚实留空）；
//   - HTTP 状态：在册错误码走 errcodeToHTTP 显式映射，无信封机械错误
//     回落 gRPC code 机械映射（gateway.DefaultCodeToHTTP）；
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

		httpStatus := resolveHTTPStatus(st, envelope)
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
