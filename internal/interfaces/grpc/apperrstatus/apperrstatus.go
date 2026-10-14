// Package apperrstatus 是 apperr 信封的 gRPC 传输承接面（P1a-3a 归层：
// domain/shared/apperr 纯化为数据类型后，ErrorResponse 信封与 grpc status
// 的构造/还原整体落位传输适配层）。消费面三处同源引用：
//
//   - gRPC 出口拦截器与 REST gateway 错误路由（assembly，经 SanitizeExit
//     单点收口）；
//   - CLI（cmd/fleetly，FromError 解码 status detail 还原信封）——CLI 是
//     输入适配器，依赖 interfaces 层共享件与拦截器同一解码源，不另造镜像。
//
// 归层理由：ErrorResponse 是传输契约（genproto），status detail 携带是
// grpc-go 专有机制，均非 domain 语义；信封字段本身（code/message/
// suggestion/context/errorID）留在 domain/shared/apperr，本包只做投影
// （构造）与还原（解码）。
package apperrstatus

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/errcode"
)

// grpcCodeFor 返回错误码的 gRPC 传输码（注册表真源；New/Restore 均保证
// 在册，回落 Unknown 分支正常不可达）。
func grpcCodeFor(e *apperr.Error) codes.Code {
	if c, ok := errcode.Get(e.Code()); ok {
		return c.GRPC
	}
	return codes.Unknown
}

// ToGRPCStatus 构造携带信封 detail 的完整 status（原 *apperr.Error 方法
// 形态，移出 domain 后的函数承接）；detail 序列化失败（理论上不可达：
// 消息均为简单标量）时退化为纯 status。retryAfter 非空时追加 RetryInfo
// detail（gateway 429 出口的 Retry-After 源）。
func ToGRPCStatus(e *apperr.Error) *status.Status {
	st := status.New(grpcCodeFor(e), e.Error())
	if withDetail, err := st.WithDetails(Envelope(e, "")); err == nil {
		st = withDetail
	}
	if d, ok := e.RetryAfter(); ok {
		if withDetail, err := st.WithDetails(&errdetails.RetryInfo{
			RetryDelay: durationpb.New(d),
		}); err == nil {
			return withDetail
		}
	}
	return st
}

// Envelope 构造对外错误信封（errorID 为空时回落错误自身还原值——转发链
// 复用同一 id；两者皆空不落 error_id 键）。
func Envelope(e *apperr.Error, errorID string) *sharedv1.ErrorResponse {
	envelope := &sharedv1.ErrorResponse{
		Code:       e.Code(),
		Message:    e.Message(),
		Suggestion: e.Suggestion(),
		Docs:       errcode.DocsURL(e.Code()),
		Context:    make(map[string]string, len(e.ContextPairs())+1),
	}
	for _, p := range e.ContextPairs() {
		envelope.Context[p.Key] = p.Value
	}
	if errorID == "" {
		errorID = e.ErrorID()
	}
	if errorID != "" {
		envelope.Context["error_id"] = errorID
	}
	return envelope
}

// FromGRPCStatus 从 gRPC status 还原 *Error（无信封 detail 返回 false）。
func FromGRPCStatus(st *status.Status) (*apperr.Error, bool) {
	if st == nil {
		return nil, false
	}
	for _, d := range st.Details() {
		env, ok := d.(*sharedv1.ErrorResponse)
		if !ok {
			continue
		}
		var pairs []apperr.Pair
		var errorID string
		for k, v := range env.GetContext() {
			if k == "error_id" {
				errorID = v // 追踪锚还原（出口收口的 id 经 detail 往返保持）
				continue
			}
			pairs = append(pairs, apperr.Pair{Key: k, Value: v})
		}
		e, registered := apperr.Restore(env.GetCode(), env.GetMessage(), env.GetSuggestion(), pairs, errorID)
		if !registered {
			// 文档外码不采信：还原为带上下文的内部错误（不发明注册表外语义）。
			return apperr.New("E_INTERNAL", "internal server error").
				WithContext("received_unregistered_code", env.GetCode()), true
		}
		return e, true
	}
	return nil, false
}

// FromError 是 errors.As 风格的便捷还原（本地 *Error 或 gRPC 状态链）。
func FromError(err error) (*apperr.Error, bool) {
	if err == nil {
		return nil, false
	}
	if e, ok := err.(*apperr.Error); ok {
		return e, true
	}
	if st, ok := status.FromError(err); ok {
		return FromGRPCStatus(st)
	}
	return nil, false
}

// EnvelopeFromGRPCStatus 是 gateway 统一出口：优先还原信封 detail；无
// detail 时输出退化信封——code 仅在 Internal/Unknown 时回落 "E_INTERNAL"
// （机械失败不发明文档外码），message 原样（Internal/Unknown 的脱敏与
// error_id 铸造由 SanitizeExit 单点在出口先行完成，本函数只投影）。
func EnvelopeFromGRPCStatus(st *status.Status, errorID string) *sharedv1.ErrorResponse {
	if e, ok := FromGRPCStatus(st); ok {
		return Envelope(e, errorID)
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
