// Package apperr 是携带 edgefleet 错误信封的应用错误类型。
//
// 错误信封契约（发布专项 §2.7、proto/edgefleet/shared/v1/error.proto）：
// {code, message, phase, deployment_id, suggestion, context, docs} 七字段；
// code 必须来自 errcode 注册表（唯一真源，只增不复用）。gRPC 侧以
// status detail（*sharedv1.ErrorResponse）携带信封；REST 侧由 gateway
// HTTPErrorHandler 解出信封渲染 snake_case JSON。
//
// 兜底取舍（待 T0.5 冻结确认）：无信封 detail 的错误（传输层/框架层错误）
// 退化为「code 留空串 + message 保底」的通用信封，HTTP 状态由 grpc code
// 机械映射——不发明文档外码、不挪用既有码语义。
package apperr

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	sharedv1 "github.com/edgesets/edgefleet/genproto/edgefleet/shared/v1"
	"github.com/edgesets/edgefleet/internal/errcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error 是携带完整错误信封的应用错误。零值不可用；经 New/With* 构造。
type Error struct {
	code         string
	message      string
	phase        string
	deploymentID string
	suggestion   string
	docs         string
	context      map[string]string
	cause        error
}

// New 构造注册表内错误码的应用错误：suggestion/docs 取注册表默认
// （可经 WithSuggestion 覆盖文案）。code 未注册时 panic（构造期 fail-fast，
// 保证信封 code 恒为注册表内码）；W_ 警告码同样 panic——警告是资源/计划
// 上的标注（HTTP=0），永不作为错误返回。
func New(code, messageFormat string, args ...any) *Error {
	c, ok := errcode.Get(code)
	if !ok {
		panic(fmt.Sprintf("apperr: 错误码 %q 未注册（信封 code 只允许注册表内码）", code))
	}
	if strings.HasPrefix(code, "W_") {
		panic(fmt.Sprintf("apperr: 警告码 %s 不得构造为错误（警告不作为 HTTP/gRPC 错误返回）", code))
	}
	return &Error{
		code:       c.ID,
		message:    fmt.Sprintf(messageFormat, args...),
		suggestion: c.Suggestion,
		docs:       c.Docs(),
	}
}

// WithPhase 附加发布阶段（如 resolve/build/deploy/serve）。
func (e *Error) WithPhase(phase string) *Error { e.phase = phase; return e }

// WithDeploymentID 关联部署 ID。
func (e *Error) WithDeploymentID(id string) *Error { e.deploymentID = id; return e }

// WithSuggestion 覆盖注册表默认建议文案。
func (e *Error) WithSuggestion(s string) *Error { e.suggestion = s; return e }

// WithContext 附加结构化上下文键值（machine-readable，同名覆盖）。
func (e *Error) WithContext(key, value string) *Error {
	if e.context == nil {
		e.context = make(map[string]string)
	}
	e.context[key] = value
	return e
}

// WithCause 记录底层原因错误（errors.Unwrap 链保留）。
func (e *Error) WithCause(cause error) *Error { e.cause = cause; return e }

// Error 实现 error 接口："CODE: message"；无码（信封还原路径）时仅 message。
func (e *Error) Error() string {
	if e.code == "" {
		return e.message
	}
	return e.code + ": " + e.message
}

// Unwrap 暴露底层原因（errors.Is/As 沿链判定）。
func (e *Error) Unwrap() error { return e.cause }

// Is 支持 errors.Is：同为 *Error 且 code 相等（非空）即判定同一错误类别；
// 否则沿 Unwrap 链由 errors 包判定。
func (e *Error) Is(target error) bool {
	var t *Error
	if errors.As(target, &t) {
		return e.code != "" && e.code == t.code
	}
	return false
}

// Code 返回稳定错误码（退化信封为空串）。
func (e *Error) Code() string { return e.code }

// Message 返回人读信息。
func (e *Error) Message() string { return e.message }

// Context 返回结构化上下文（只读用途）。
func (e *Error) Context() map[string]string { return e.context }

// HTTPStatus 返回该错误的默认 HTTP 状态：注册码按注册表映射，无码兜底
// 500（退化信封在 gateway 侧另经 GRPCCodeToHTTP 机械映射）。
func (e *Error) HTTPStatus() int {
	if e.code == "" {
		return http.StatusInternalServerError
	}
	if s := errcode.HTTPStatus(e.code); s != 0 {
		return s
	}
	return http.StatusInternalServerError
}

// Envelope 构造错误信封 proto（七字段；空值字段由 marshaler 语义决定
// 是否输出）。
func (e *Error) Envelope() *sharedv1.ErrorResponse {
	return &sharedv1.ErrorResponse{
		Code:         e.code,
		Message:      e.message,
		Phase:        e.phase,
		DeploymentId: e.deploymentID,
		Suggestion:   e.suggestion,
		Context:      e.context,
		Docs:         e.docs,
	}
}

// ToGRPCStatus 转为携带信封 detail 的 gRPC status：grpc code 由注册表
// HTTP 映射机械推导，detail 为 *sharedv1.ErrorResponse。
func (e *Error) ToGRPCStatus() *status.Status {
	st := status.New(HTTPToGRPCCode(e.HTTPStatus()), e.message)
	withDetail, err := st.WithDetails(e.Envelope())
	if err != nil {
		// detail 附加失败（理论不可达：proto 类型已注册）退化为无 detail
		// status，对端走退化信封路径。
		return st
	}
	return withDetail
}

// GRPCStatus 使 *Error 直接满足 gRPC 传输的错误接口（grpc status 包对
// 实现 GRPCStatus() *status.Status 的错误原样采用，detail 随之上线），
// handler 可直接 return *Error。
func (e *Error) GRPCStatus() *status.Status { return e.ToGRPCStatus() }

// FromGRPCStatus 从 gRPC status 还原 *Error：找到 *sharedv1.ErrorResponse
// detail 时返回 (err, true)——注册码恢复注册表默认 suggestion/docs（detail
// 内值优先）；无 detail 返回 (nil, false)，由调用方走退化信封。
func FromGRPCStatus(st *status.Status) (*Error, bool) {
	if st == nil {
		return nil, false
	}
	for _, d := range st.Details() {
		env, ok := d.(*sharedv1.ErrorResponse)
		if !ok {
			continue
		}
		e := &Error{
			code:         env.GetCode(),
			message:      env.GetMessage(),
			phase:        env.GetPhase(),
			deploymentID: env.GetDeploymentId(),
			suggestion:   env.GetSuggestion(),
			docs:         env.GetDocs(),
			context:      env.GetContext(),
		}
		if c, ok := errcode.Get(e.code); ok {
			if e.suggestion == "" {
				e.suggestion = c.Suggestion
			}
			if e.docs == "" {
				e.docs = c.Docs()
			}
		}
		return e, true
	}
	return nil, false
}

// FromError 是 FromGRPCStatus 的 error 入口（非 status 错误返回 false）。
func FromError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	st, ok := status.FromError(err)
	if !ok {
		return nil, false
	}
	return FromGRPCStatus(st)
}

// EnvelopeFromGRPCStatus 是 gateway 错误链的统一出口：有信封 detail 时
// 返回信封与注册表 HTTP 状态；无 detail 走退化信封——code 留空串（不发明
// 文档外码、不挪用既有码语义），message 取 status 原文保底，HTTP 状态由
// grpc code 机械映射。兜底形态待 T0.5 契约冻结确认。
func EnvelopeFromGRPCStatus(st *status.Status) (*sharedv1.ErrorResponse, int) {
	if st == nil {
		return &sharedv1.ErrorResponse{Message: "internal server error"}, http.StatusInternalServerError
	}
	if e, ok := FromGRPCStatus(st); ok {
		return e.Envelope(), e.HTTPStatus()
	}
	return &sharedv1.ErrorResponse{Message: st.Message()}, GRPCCodeToHTTP(st.Code())
}

// HTTPToGRPCCode 把注册表 HTTP 状态机械映射为 gRPC code（信封承载契约，
// 传输 code 仅求稳定可逆；409/410 统一 FailedPrecondition——冲突/前置失效
// 语义，细分由信封 code 表达）。
func HTTPToGRPCCode(httpStatus int) codes.Code {
	switch httpStatus {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusConflict, http.StatusGone:
		return codes.FailedPrecondition
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	case http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	default:
		return codes.Internal
	}
}

// GRPCCodeToHTTP 是 HTTPToGRPCCode 的传输侧逆向映射（gateway 对无信封
// 错误的兜底；与 torchwood grpcCodeToHTTP 同形）。
func GRPCCodeToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return 499
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
