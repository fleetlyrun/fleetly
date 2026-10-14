// Package apperr 是 fleetly 应用错误类型（domain 纯数据形态）：errcode
// 注册表条目 + 处置提示 + 结构化上下文 + 追踪锚（errorID）。域内用法：
// handler 裸 `return apperr.New("E_XXX", "...")`。
//
// P1a-3a 起 gRPC status / ErrorResponse 信封的构造与还原移传输适配层
// （internal/interfaces/grpc/apperrstatus：拦截器与 gateway 的 SanitizeExit
// 单点、CLI 的 FromError 解码），本包零传输依赖（无 genproto/grpc
// import）——信封字段本身是领域数据，携带方式是传输细节。
package apperr

import (
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/errcode"
)

// Error 是带注册表语义的应用错误；零值不可用，经 New 构造。
type Error struct {
	code       errcode.Code
	message    string
	suggestion string         // 空串 = 用注册表默认
	context    []Pair         // 保序键值对（含 error_id 等运行时锚）
	cause      error          // WithCause 链（errors.Is/As 连通）
	retryAfter *time.Duration // 非 nil 时传输面附加 RetryInfo detail（REST 429 的 Retry-After 源）
	errorID    string         // 出口收口铸造的追踪 id（E_INTERNAL 专用；转发链还原复用，不重铸）
}

// Pair 是结构化上下文键值对（保序）。
type Pair struct{ Key, Value string }

// New 构造应用错误；code 未注册或为空即 panic（fail-fast：拼写漂移不该
// 等到首个请求暴露）。message 是 format 字符串，args 走 fmt.Sprintf。
func New(codeID, format string, args ...any) *Error {
	c, ok := errcode.Get(codeID)
	if !ok {
		panic(fmt.Sprintf("apperr: unregistered errcode %q (registry is append-only; see internal/model/errcode/codes.go)", codeID))
	}
	return &Error{code: c, message: fmt.Sprintf(format, args...)}
}

// Restore 依信封字段重建 *Error（传输侧 detail 往返的还原构造，
// apperrstatus.FromGRPCStatus 的入编入口）：codeID 必须在册——未注册返回
// false（文档外码不采信，由调用方回落 E_INTERNAL）。errorID 为信封
// context 还原的追踪锚（缺席传空串）。
func Restore(codeID, message, suggestion string, context []Pair, errorID string) (*Error, bool) {
	c, ok := errcode.Get(codeID)
	if !ok {
		return nil, false
	}
	e := &Error{code: c, message: message, errorID: errorID}
	if suggestion != "" {
		e.suggestion = suggestion
	}
	e.context = append(e.context, context...)
	return e, true
}

// WithContext 附加 machine-readable 上下文（链式，返回同一 *Error）。
func (e *Error) WithContext(key, value string) *Error {
	e.context = append(e.context, Pair{key, value})
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

// WithRetryAfter 附加建议退避（链式）。REST 面由 gateway 的 429 出口提取
// 为 Retry-After 头（向上取整至少 1s）；机器面读信封 retry_after_seconds
// 上下文。
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

// ErrorID 返回出口收口铸造的错误追踪 id（信封 context error_id 的还原值；
// 未收口/非 E_INTERNAL 为空串）。
func (e *Error) ErrorID() string { return e.errorID }

// ContextPairs 返回结构化上下文（保序；只读约定——信封投影按序消费，
// 调用方不得改写）。
func (e *Error) ContextPairs() []Pair { return e.context }

// RetryAfter 返回建议退避窗口（WithRetryAfter 未设即 false；传输面据此
// 附加 RetryInfo detail）。
func (e *Error) RetryAfter() (time.Duration, bool) {
	if e.retryAfter == nil {
		return 0, false
	}
	return *e.retryAfter, true
}

// Is 支持按码判等：apperr.New("E_X", ...) 之间 errors.Is 为真。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.code.ID == e.code.ID
}
