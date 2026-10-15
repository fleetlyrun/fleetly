// errors.go 是 SDK 面的 apperr 信封投影（P4-4 批 4，P0-3 信封纪律对齐）。
//
// wire 契约：服务端错误以 gRPC status detail 携带 fleetly.shared.v1.
// ErrorResponse 上行（proto/ 是契约唯一真源，gateway 将同一信封渲染为
// snake_case JSON）。解码形态与 internal/interfaces/grpc/apperrstatus 的
// FromGRPCStatus 同构（error_id 自 context 提升为追踪锚）；但本模块是公开
// SDK（禁 import internal/**），故不引 errcode 注册表，信封解码按 wire
// 自足实现——与 apperrstatus 的唯一语义差异：未注册码不回落 E_INTERNAL，
// 而是原样投影（信任服务端"只在册码出口"的纪律；投影器对 wire 诚实，
// 不在消费端发明注册表外语义，也不因注册表漂移而改写服务端原文）。
package fleetly

import (
	"google.golang.org/grpc/status"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
)

// errorIDContextKey 是 error_id 在信封 context 中的承载键（出口单点铸造
// 时写入；与 REST 面响应头 x-fleetly-error-id 同值——proto 消费方免读头）。
const errorIDContextKey = "error_id"

// Error 是 SDK 面的 apperr 信封投影（code/error_id/suggestion 三件套 +
// docs/context）。
type Error struct {
	Code       string
	Message    string
	Suggestion string
	Docs       string
	ErrorID    string            // context["error_id"] 提升（SanitizeExit 出口铸造的追踪锚）
	Context    map[string]string // error_id 键外的其余上下文对
}

// Error 实现 error："CODE: message" 单行——与服务端 status message 的 wire
// 形态一致（apperr.Error() 同款拼接）；code 缺席（信封退化投影）时诚实
// 回落 message 原文。
func (e *Error) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// FromError 从错误链还原信封投影（errors.As 风格；nil-safe；无信封
// detail → (nil, false)）。gRPC status 上的非信封 detail（RetryInfo 等）
// 跳过继续找——与服务端出口可能并挂多 detail 的形态相容。
func FromError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if e, ok := err.(*Error); ok {
		return e, true
	}
	st, ok := status.FromError(err)
	if !ok {
		// 非 status 错误（本地构造的普通 error）：无信封可投影。
		return nil, false
	}
	for _, d := range st.Details() {
		env, ok := d.(*sharedv1.ErrorResponse)
		if !ok {
			continue
		}
		out := &Error{
			Code:       env.GetCode(),
			Message:    env.GetMessage(),
			Suggestion: env.GetSuggestion(),
			Docs:       env.GetDocs(),
			Context:    make(map[string]string, len(env.GetContext())),
		}
		for k, v := range env.GetContext() {
			if k == errorIDContextKey {
				out.ErrorID = v // 追踪锚提升（与 apperrstatus 解码同构）
				continue
			}
			out.Context[k] = v
		}
		return out, true
	}
	return nil, false
}
