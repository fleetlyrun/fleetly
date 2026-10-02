// Package apperr 是 fleetly 应用错误类型：errcode 注册表条目 + 处置提示 +
// 结构化上下文，经 gRPC status detail 携带 fleetly.shared.v1.ErrorResponse
// 信封上线（REST 由 gateway 统一渲染，CLI 经 RenderError 渲染 stderr）。
//
// 用法：handler 裸 `return apperr.New("E_XXX", "...")`——*Error 实现
// GRPCStatus()，grpc-go 原样采用其 status 与 detail。
package apperr

import (
	"fmt"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/model/errcode"
)

// Error 是带注册表语义的应用错误；零值不可用，经 New 构造。
type Error struct {
	code       errcode.Code
	message    string
	suggestion string // 空串 = 用注册表默认
	context    []kv   // 保序键值对（含 error_id 等运行时锚）
	cause      error
	retryAfter *time.Duration // 非 nil 时 status 附加 RetryInfo detail（REST 429 的 Retry-After 源）
}

type kv struct{ Key, Value string }

// New 构造应用错误；code 未注册或为空即 panic（fail-fast：拼写漂移不该
// 等到首个请求暴露）。message 是 format 字符串，args 走 fmt.Sprintf。
func New(codeID, format string, args ...any) *Error {
	c, ok := errcode.Get(codeID)
	if !ok {
		panic(fmt.Sprintf("apperr: unregistered errcode %q (registry is append-only; see internal/model/errcode/codes.go)", codeID))
	}
	return &Error{code: c, message: fmt.Sprintf(format, args...)}
}

// WithContext 附加 machine-readable 上下文（链式，返回同一 *Error）。
func (e *Error) WithContext(key, value string) *Error {
	e.context = append(e.context, kv{key, value})
	return e
}

// WithSuggestion 覆盖注册表默认处置提示。
func (e *Error) WithSuggestion(s string) *Error {
	e.suggestion = s
	return e
}

// WithCause 包装底层错误（errors.Is/As 链保持连通）。
func (e *Error) WithCause(cause error) *Error {
	e.cause = cause
	return e
}

// WithRetryAfter 附加 gRPC RetryInfo detail（链式）。REST 面由 gateway 的
// 429 出口提取为 Retry-After 头（向上取整至少 1s）；机器面读信封
// retry_after_seconds 上下文。
func (e *Error) WithRetryAfter(d time.Duration) *Error {
	e.retryAfter = &d
	return e
}

// Error 实现 error 接口：稳定形态 "E_XXX: message"。
func (e *Error) Error() string { return e.code.ID + ": " + e.message }

// Code 返回注册表错误码 ID。
func (e *Error) Code() string { return e.code.ID }

// Message 返回人读错误信息。
func (e *Error) Message() string { return e.message }

// Suggestion 返回处置提示（覆盖优先）。
func (e *Error) Suggestion() string {
	if e.suggestion != "" {
		return e.suggestion
	}
	return e.code.Suggestion
}

// Unwrap 还原底层错误（WithCause 链）。
func (e *Error) Unwrap() error { return e.cause }

// Is 支持按码判等：apperr.New("E_X", ...) 之间 errors.Is 为真。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.code.ID == e.code.ID
}

// GRPCStatus 使 *Error 直接满足 gRPC 传输接口：handler 裸返回即可，
// 信封以 status detail 上线（grpc-go 对实现该方法的 error 原样采用）。
func (e *Error) GRPCStatus() *status.Status {
	return e.ToGRPCStatus()
}

// ToGRPCStatus 构造携带信封 detail 的完整 status；detail 序列化失败
// （理论上不可达：消息均为简单标量）时退化为纯 status。retryAfter 非空时
// 追加 RetryInfo detail（gateway 429 出口的 Retry-After 源）。
func (e *Error) ToGRPCStatus() *status.Status {
	st := status.New(e.code.GRPC, e.Error())
	if withDetail, err := st.WithDetails(e.Envelope("")); err == nil {
		st = withDetail
	}
	if e.retryAfter != nil {
		if withDetail, err := st.WithDetails(&errdetails.RetryInfo{
			RetryDelay: durationpb.New(*e.retryAfter),
		}); err == nil {
			return withDetail
		}
	}
	return st
}

// Envelope 构造对外错误信封（errorID 为空时不落 error_id 键）。
func (e *Error) Envelope(errorID string) *sharedv1.ErrorResponse {
	envelope := &sharedv1.ErrorResponse{
		Code:       e.code.ID,
		Message:    e.message,
		Suggestion: e.Suggestion(),
		Docs:       errcode.DocsURL(e.code.ID),
		Context:    make(map[string]string, len(e.context)+1),
	}
	for _, p := range e.context {
		envelope.Context[p.Key] = p.Value
	}
	if errorID != "" {
		envelope.Context["error_id"] = errorID
	}
	return envelope
}

// FromGRPCStatus 从 gRPC status 还原 *Error（无信封 detail 返回 false）。
func FromGRPCStatus(st *status.Status) (*Error, bool) {
	if st == nil {
		return nil, false
	}
	for _, d := range st.Details() {
		env, ok := d.(*sharedv1.ErrorResponse)
		if !ok {
			continue
		}
		c, registered := errcode.Get(env.GetCode())
		if !registered {
			// 文档外码不采信：还原为带上下文的内部错误（不发明注册表外语义）。
			return New("E_INTERNAL", "internal server error").
				WithContext("received_unregistered_code", env.GetCode()), true
		}
		e := &Error{code: c, message: env.GetMessage()}
		if env.GetSuggestion() != "" {
			e.suggestion = env.GetSuggestion()
		}
		for k, v := range env.GetContext() {
			if k == "error_id" {
				continue
			}
			e.context = append(e.context, kv{k, v})
		}
		return e, true
	}
	return nil, false
}

// FromError 是 errors.As 风格的便捷还原（本地 *Error 或 gRPC 状态链）。
func FromError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if e, ok := err.(*Error); ok {
		return e, true
	}
	if st, ok := status.FromError(err); ok {
		return FromGRPCStatus(st)
	}
	return nil, false
}

// EnvelopeFromGRPCStatus 是 gateway 统一出口：优先还原信封 detail；无
// detail 时输出退化信封——code 仅在 Internal/Unknown 时回落 "E_INTERNAL"
// （机械失败不发明文档外码），message 原样（Internal/Unknown 的脱敏由
// 调用方 gateway 错误处理完成）。
func EnvelopeFromGRPCStatus(st *status.Status, errorID string) *sharedv1.ErrorResponse {
	if e, ok := FromGRPCStatus(st); ok {
		return e.Envelope(errorID)
	}
	envelope := &sharedv1.ErrorResponse{
		Message: st.Message(),
		Context: map[string]string{"error_id": errorID},
	}
	if st.Code() == codes.Internal || st.Code() == codes.Unknown {
		envelope.Code = "E_INTERNAL" // 在册回落；其余机械 code 留空（诚实：无注册表语义）
		envelope.Docs = errcode.DocsURL("E_INTERNAL")
	}
	return envelope
}
