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
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
)

// errcodeToHTTP 是 REST 面错误码级 HTTP 状态映射（显式钉扎，全量覆盖在册
// 注册表；assembly 守卫测试钉住"新码必须显式决策"）。为什么不用 gRPC code
// 机械映射兜底全部：HTTP 状态的语义真源是错误码语义，不是传输层 gRPC code
// ——E_CONFLICT 族承载在 FailedPrecondition 上（gRPC 语义正确），机械映射
// 却把它打成 400，而"请求合法但与资源当前状态冲突"在 HTTP 语义上就是 409
// （批 0 复核回归：REST/SDK 按 409 写的重试逻辑不得静默破坏）。逐码盘点：
//
//   - 409 Conflict（状态冲突族）：E_ALREADY_EXISTS（唯一键已存在）、
//     E_IDEMPOTENCY_KEY_CONFLICT（幂等键异体/在途/双源不一致）、
//     E_CONFLICT / E_NOT_CANCELLABLE / E_NO_BASELINE / E_CHANGE_FROZEN /
//     E_PLATFORM_BACKUP_FAILED（请求合法、当前状态不容
//     ——cancel/解绑/lift 冻结/装 restic 后可重试）与 E_SECRET_UNAVAILABLE
//     （P4-3 批 3 收窄后只剩 seal/open 运算失败——密钥在场但运算出错，
//     修钥后可重试；"未配主密钥" nil 面已分流 E_MASTER_KEY_UNAVAILABLE）；
//   - 503 Service Unavailable（配置级 off 态族，absence is an explicit off
//     state）：E_REGISTRY_UNAVAILABLE（registry.addr 空/无 content 面）、
//     E_MASTER_KEY_UNAVAILABLE（未配主密钥，cipher 面缺席）、
//     E_LOG_STORE_UNAVAILABLE（logging.addr 空）、
//     E_METRICS_STORE_UNAVAILABLE（metrics.addr 空）与
//     E_BROWSE_DISABLED / E_DATABASE_NOT_READY / E_NODE_RELAY_OFFLINE；
//   - 501 Not Implemented（面/子面缺席族）：E_EXEC_UNSUPPORTED、
//     E_BROWSER_UNSUPPORTED 与 E_RUNTIME_FACE_UNSUPPORTED（runtime 可选
//     子面——node admin/container logs——或引擎面缺席；honest failure，
//     无降级路径）；
//   - 400/404/409/410/413/429/401/403/500 与语义对照（本就对，钉扎防漂移）：
//     E_INVALID_ARGUMENT→400、E_NOT_FOUND→404、E_EVENTS_GONE 与
//     E_UPLOAD_UNAVAILABLE→410（断档/产物已扫：重同步或重传）、
//     E_UPLOAD_TOO_LARGE→413（设计内的 payload 超限形态，webhook 413 同款；
//     Q-11 限额错位教训——不得死于不透明 RESOURCE_EXHAUSTED）、
//     E_QUEUE_FULL 与 E_QUOTA_EXCEEDED 与 E_RATE_LIMITED→429（Retry-After
//     提取仍按 429 生效——E_RATE_LIMITED 携带 RetryInfo detail）、
//     E_METRICS_RESULT_TOO_LARGE→429（指标结果超体/超序列是调用方查询
//     过宽；H8 §5.7 钉 429）、E_METRICS_QUERY_TIMEOUT→504（provider 上界
//     到期，语义是网关超时而非请求错误）、
//     E_UNAUTHENTICATED 与 E_INVALID_SIGNATURE→401、E_FORBIDDEN 与
//     E_INVALID_INVITATION→403、E_INTERNAL→500。
//
// gRPC code 的 gateway.DefaultCodeToHTTP 继续兜底无信封的机械错误
// （Canceled→499、DeadlineExceeded→504 等）。
var errcodeToHTTP = map[string]int{
	"E_ALREADY_EXISTS":            http.StatusConflict,
	"E_BROWSER_UNSUPPORTED":       http.StatusNotImplemented,
	"E_BROWSE_DISABLED":           http.StatusServiceUnavailable,
	"E_CATALOG_UNAVAILABLE":       http.StatusServiceUnavailable,
	"E_DATABASE_NOT_READY":        http.StatusServiceUnavailable,
	"E_CHANGE_FROZEN":             http.StatusConflict,
	"E_APP_HOOK_OWNED":            http.StatusConflict,
	"E_CONFLICT":                  http.StatusConflict,
	"E_EVENTS_GONE":               http.StatusGone,
	"E_EXEC_UNSUPPORTED":          http.StatusNotImplemented,
	"E_FORBIDDEN":                 http.StatusForbidden,
	"E_GITHUB_UPSTREAM":           http.StatusBadGateway,
	"E_IDEMPOTENCY_KEY_CONFLICT":  http.StatusConflict,
	"E_INTERNAL":                  http.StatusInternalServerError,
	"E_INVALID_ARGUMENT":          http.StatusBadRequest,
	"E_INVALID_INVITATION":        http.StatusForbidden,
	"E_INVALID_SIGNATURE":         http.StatusUnauthorized,
	"E_LOG_STORE_UNAVAILABLE":     http.StatusServiceUnavailable,
	"E_MASTER_KEY_UNAVAILABLE":    http.StatusServiceUnavailable,
	"E_METRICS_QUERY_TIMEOUT":     http.StatusGatewayTimeout,
	"E_METRICS_RESULT_TOO_LARGE":  http.StatusTooManyRequests,
	"E_METRICS_STORE_UNAVAILABLE": http.StatusServiceUnavailable,
	"E_NODE_RELAY_OFFLINE":        http.StatusServiceUnavailable,
	"E_DATABASE_ROTATE_FAILED":    http.StatusConflict,
	"E_NOT_CANCELLABLE":           http.StatusConflict,
	"E_NOT_FOUND":                 http.StatusNotFound,
	"E_NO_BASELINE":               http.StatusConflict,
	"E_PLATFORM_BACKUP_FAILED":    http.StatusConflict,
	"E_QUEUE_FULL":                http.StatusTooManyRequests,
	"E_QUOTA_EXCEEDED":            http.StatusTooManyRequests,
	"E_RATE_LIMITED":              http.StatusTooManyRequests,
	"E_REGISTRY_UNAVAILABLE":      http.StatusServiceUnavailable,
	"E_RUNTIME_FACE_UNSUPPORTED":  http.StatusNotImplemented,
	"E_SECRET_UNAVAILABLE":        http.StatusConflict,
	"E_TEMPLATE_INVALID":          http.StatusBadRequest,
	"E_UNAUTHENTICATED":           http.StatusUnauthorized,
	"E_UPLOAD_TOO_LARGE":          http.StatusRequestEntityTooLarge,
	"E_UPLOAD_UNAVAILABLE":        http.StatusGone,
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
//   - 内部错误出口纪律（P0-3）：E_INTERNAL（及 Internal/Unknown 机械错误）
//     经 apperrstatus.SanitizeExit 单点收口——与 gRPC 拦截器同源（REST 面经 gRPC
//     段转发时错误已在 gRPC 出口收口，error_id 复用，幂等不重复落日志）；
//   - 信封还原：apperr 错误经 status detail 携带完整信封（errcode、
//     处置提示、docs、context）；无 detail 的机械错误走退化信封
//     （Internal/Unknown 回落在册 E_INTERNAL，其余 code 诚实留空）；
//   - HTTP 状态：在册错误码走 errcodeToHTTP 显式映射，无信封机械错误
//     回落 gRPC code 机械映射（gateway.DefaultCodeToHTTP）；
//   - error_id 经响应头 x-fleetly-error-id 同步承载（与 gRPC trailer 同键；
//     信封 context 仍为主面）；
//   - 429 从 RetryInfo detail 提取 Retry-After（向上取整，至少 1s）。
func newGatewayErrorHandler(logger *slog.Logger) runtime.ErrorHandlerFunc {
	return func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		if logger == nil {
			logger = slog.Default()
		}
		// 单点收口：内部错误在此（或 gRPC 出口已）脱敏 + 铸 id；非内部错误
		// 原样放行（errorID 空串）。where 只进日志（见 SanitizeExit），不
		// 进响应体——r 派生值经此调用不构成响应 taint。
		errorID, sanitized := apperrstatus.SanitizeExit(ctx, logger, err, "http "+r.Method+" "+r.URL.Path)
		st, ok := status.FromError(sanitized)
		if !ok || st == nil {
			// 防御分支：SanitizeExit 已把裸错误收成 status 形态，正常不可达。
			st = status.New(codes.Internal, apperrstatus.RedactedInternalMessage)
		}
		if errorID == "" {
			// REST 面全错误带 error_id（既有行为，非内部错误也铸）。
			errorID = apperrstatus.NewErrorID()
		}
		envelope := apperrstatus.EnvelopeFromGRPCStatus(st, errorID)

		httpStatus := resolveHTTPStatus(st, envelope)
		if httpStatus == http.StatusTooManyRequests {
			if retryAfter, ok := retryAfterSeconds(st); ok {
				w.Header().Set("Retry-After", retryAfter)
			}
		}

		w.Header().Set(apperrstatus.ErrorIDHeader, errorID)
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
		//nolint:gosec // G705 误报：gosec 把 SanitizeExit 的 where 参数（r.Method/r.URL.Path，仅落服务端日志）保守汇入返回值污点；data 实为 err 信封的 JSON 序列化（Content-Type: application/json），非 HTML 注入面
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
