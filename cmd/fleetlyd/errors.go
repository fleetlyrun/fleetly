package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newGatewayErrorHandler 是 gateway 的统一 HTTPErrorHandler（发布专项
// §2.7 错误信封、torchwood errors.go 同形）：
//   - 有信封 detail（apperr 产出的 status）→ 解出 ErrorResponse，HTTP 状态
//     按注册表默认映射，JSON 经 gateway marshaler 输出（UseProtoNames →
//     snake_case 七字段）；
//   - 无 detail（传输层/框架层错误）→ 退化信封：code 留空串（不发明文档
//     外码、不挪用既有码语义，取舍待 T0.5 冻结确认），message 取 status
//     原文保底，HTTP 状态由 grpc code 机械映射。
//
// 非 status 错误（理论不可达：gateway 侧 err 恒为 status）按 Internal
// 兜底并记日志，响应文案固定，不外泄内部细节。
func newGatewayErrorHandler() runtime.ErrorHandlerFunc {
	return func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		st, ok := status.FromError(err)
		if !ok {
			slog.ErrorContext(ctx, "gateway: non-status error degraded to internal envelope",
				"path", r.URL.Path, "method", r.Method, "error", err.Error())
			st = status.New(codes.Internal, "internal server error")
		}
		envelope, httpStatus := apperr.EnvelopeFromGRPCStatus(st)
		w.Header().Set("Content-Type", marshaler.ContentType(envelope))
		w.WriteHeader(httpStatus)
		if body, merr := marshaler.Marshal(envelope); merr != nil {
			slog.ErrorContext(ctx, "gateway: failed to marshal error envelope",
				"path", r.URL.Path, "code", envelope.GetCode(), "error", merr.Error())
			return
		} else if _, werr := w.Write(body); werr != nil {
			slog.ErrorContext(ctx, "gateway: failed to write error envelope",
				"path", r.URL.Path, "error", werr.Error())
		}
	}
}
