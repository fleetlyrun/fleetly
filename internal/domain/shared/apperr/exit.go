package apperr

// 出口纪律单点（P0-3 E_INTERNAL 三面统一）：E_INTERNAL 且仅 E_INTERNAL 在
// 出口处收口——message 统一脱敏文案、铸造 error_id、原始 cause 全文只进
// 服务端日志（slog，error_id 关联）。gRPC 拦截器与 REST gateway 错误路由
// 共用本单点；非 E_INTERNAL 错误（哨兵/业务码的 message 本就面向用户）
// 原样放行、零影响。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RedactedInternalMessage 是 E_INTERNAL 的对外统一文案（原始消息只落服务
// 端日志，不泄内部细节——SQL/回滚复合文本等 cause 不得上线）。
const RedactedInternalMessage = "internal server error"

// ErrorIDHeader 是 error_id 的传输承载键：gRPC 面 outgoing trailer metadata
// 与 REST 面响应头同名同值（error_id 同时落在信封 context，proto 消费方免
// 读头）。
const ErrorIDHeader = "x-fleetly-error-id"

// NewErrorID 铸造错误追踪 ID：crypto/rand 128 位十六进制（32 字符），与
// grpcapi gateway.NewErrorID 同形态（REST 面既有形态不动，铸造收进本单源）。
// 熵源故障（极罕见）退化为纳秒时间戳，保证字段仍在。
func NewErrorID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// SanitizeExit 应用内部错误出口纪律并返回对外错误与 error_id（非内部错误
// errorID 为空串、err 原样返回；error 殿后从 ST1008）：
//
//   - 在册 E_INTERNAL（本地 *Error 或 status detail 还原）：message 换脱敏
//     文案、error_id 入信封 context、cause 全文落 Error 级日志；
//   - 无信封的 Internal/Unknown 机械错误（含 grpc-go 对裸错误的 Unknown 兜
//     底）：同款收口为在册 E_INTERNAL 退化信封（与 REST 面既有回落语义一致
//     ——机械失败不发明文档外码）；context.Canceled/DeadlineExceeded 诚实
//     放行（机械语义，非内部错误）；
//   - 幂等：错误已带 error_id（前一出口已收口——REST 面经 gRPC 段转发的
//     常态）时原样放行并复用该 id，不重复铸造、不重复落日志。
func SanitizeExit(ctx context.Context, logger *slog.Logger, err error, where string) (errorID string, out error) {
	if err == nil {
		return "", nil
	}
	if e, ok := FromError(err); ok {
		if e.Code() != "E_INTERNAL" {
			return "", err
		}
		if id := e.ErrorID(); id != "" {
			return id, err
		}
		id := NewErrorID()
		logInternalExit(ctx, logger, where, id, e.Error(), e.Unwrap())
		return id, e.sanitizedStatus(id).Err()
	}
	// 机械错误：ctx 取消/超时是诚实传输语义，不吞成 E_INTERNAL。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	st, _ := status.FromError(err) // 非 status 错误由 grpc 语义回落 Unknown
	if st.Code() != codes.Internal && st.Code() != codes.Unknown {
		return "", err
	}
	id := NewErrorID()
	logInternalExit(ctx, logger, where, id, st.Message(), nil)
	return id, degenerateInternalStatus(id).Err()
}

// sanitizedStatus 构造 E_INTERNAL 的脱敏上线形态：status message 固定
// "E_INTERNAL: internal server error"（与其余码 "CODE: message" 形态一致），
// 信封 detail 保留 code/suggestion/docs/context 并携带 error_id。retryAfter
// 不携带：429 族的 Retry-After 语义与 500 内部错误无交集。
func (e *Error) sanitizedStatus(errorID string) *status.Status {
	st := status.New(e.code.GRPC, e.code.ID+": "+RedactedInternalMessage)
	envelope := e.Envelope(errorID)
	envelope.Message = RedactedInternalMessage
	if withDetail, err := st.WithDetails(envelope); err == nil {
		return withDetail
	}
	return st
}

// degenerateInternalStatus 构造无信封机械错误的 E_INTERNAL 退化上线形态
// （在册回落：suggestion/docs 来自注册表，机械失败不发明文档外码）。
func degenerateInternalStatus(errorID string) *status.Status {
	return New("E_INTERNAL", RedactedInternalMessage).sanitizedStatus(errorID)
}

// logInternalExit 在服务端落 Error 级日志：cause 全文（message + 底层错误
// 链）与 error_id 关联，where 标注出口（gRPC FullMethod / REST path）。
func logInternalExit(ctx context.Context, logger *slog.Logger, where, errorID, message string, cause error) {
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{"error_id", errorID, "where", where, "original_message", message}
	if cause != nil {
		attrs = append(attrs, "cause", cause.Error())
	}
	logger.ErrorContext(ctx, fmt.Sprintf("internal error sanitized at exit: %s", where), attrs...)
}
