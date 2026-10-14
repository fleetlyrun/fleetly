package governance

// 冻结执法的流式面（ADR-0019 附录 A.2，F1.10）：仓内首个 client-streaming
// 写面（UploadSource）让流式链从仅 authn 升为 authn→freeze。Team 解析锚
// 在首帧字段（元信息帧），而流拦截器拿不到请求原型——执法点因此落在包装
// stream 的首次 RecvMsg 上：读首帧后解析 Team 并查冻结，拒绝以首帧 Recv
// 错误面呈现（handler 零字节写入即返回；客户端在 Send/CloseAndRecv 上看
// 到同一状态）。非封禁面方法零开销直通。

import (
	"google.golang.org/grpc"
)

// Stream 返回流式冻结拦截器：封禁面动词的首帧 RecvMsg 包装执法。
func (g *FreezeGuard) Stream() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		scope, ok := FrozenVerbs[info.FullMethod]
		if !ok {
			return handler(srv, ss)
		}
		return handler(srv, &freezeRecvStream{ServerStream: ss, guard: g, scope: scope})
	}
}

// freezeRecvStream 包装 ServerStream：首次 RecvMsg 后执行冻结判定，后续
// 原样透传（执法一次；契约把 Team 锚定在首帧——元信息帧）。
type freezeRecvStream struct {
	grpc.ServerStream
	guard   *FreezeGuard
	scope   freezeScope
	checked bool
}

func (s *freezeRecvStream) RecvMsg(m any) error {
	if s.checked {
		return s.ServerStream.RecvMsg(m)
	}
	s.checked = true
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err // 空流/取消：执法未及发生，透传读取错误
	}
	if err := s.guard.enforce(s.Context(), m, s.scope); err != nil {
		return err // 冻结拒绝：以首帧读取错误面呈现（handler 即刻收手）
	}
	return nil
}
