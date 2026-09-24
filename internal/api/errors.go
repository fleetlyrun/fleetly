package api

import (
	"errors"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 状态层哨兵 → gRPC status 的统一映射（api 面错误语义的唯一登记点）。
//
// 稳定码取舍：资源未找到（app/deployment/env/token）在 errcode 注册表
// 无对应码，本阶段 internal/errcode 不在允许改动清单——统一走**信封退化
// 形态**（code 空 + grpc code 机械映射：NotFound→404、FailedPrecondition→
// 409），与鉴权 401/403 同口径；如需稳定码须走注册表加码流程（遗留记录）。
// apperr 产出的错误（E_COMPOSE_*、E_ROLLBACK_NO_TARGET 等）原样透传
//（*apperr.Error 实现 GRPCStatus()，detail 信封随之上线）。

// notFound 构造退化信封 NotFound。
func notFound(message string) error {
	return statusEnvelope(codes.NotFound, message)
}

// conflict 构造业务冲突信封（409）。X-5：携带最小 ErrorResponse detail
// （{"conflict": message}——B1 脱敏判定只认「无 detail」形态，携带 detail
// 即声明 message 是服务端构造的业务文案而非底层错误透传，REST 面原文
// 保留；无 detail 的 FailedPrecondition 会被误伤成固定文案 "internal
// error"）。空码 + FailedPrecondition 经 EnvelopeFromGRPCStatus 机械映射
// 回 409；gRPC/CLI 面原文与 detail 同上。
func conflict(message string) error {
	st := status.New(codes.FailedPrecondition, message)
	withDetail, err := st.WithDetails(&sharedv1.ErrorResponse{
		Message: message,
		Context: map[string]string{"conflict": message},
	})
	if err != nil {
		// detail 附加失败（理论不可达：proto 类型已注册）退化为纯 status
		// ——REST 面走退化信封路径（B1 兜底仍在）。
		return st.Err()
	}
	return withDetail.Err()
}

// statusInvalidArgument 构造退化信封无效请求（400——客户端可修正的输入
// 错误，如服务过滤名不存在）。
func statusInvalidArgument(message string) error {
	return statusEnvelope(codes.InvalidArgument, message)
}

// mapAppErr 把 app 读取哨兵映射为 api 语义（ErrAppNotFound → 404；
// tombstoned → 409 冲突——deleting/deleted 上不可继续业务写）。
func mapAppErr(err error, name string) error {
	switch {
	case errors.Is(err, state.ErrAppNotFound):
		return notFound("app not found: " + name)
	case errors.Is(err, state.ErrAppTombstoned):
		return conflict("app is tombstoned (deleting/deleted): " + name)
	default:
		return err
	}
}

// resolveApp 已迁往 ownership.go（v0.3 W2-S4：资源引用解析与角色门同域——
// 可见域感知的解析、E_APP_AMBIGUOUS 候选列、限定形 team/prj/app 均在解析
// 单点收口；本文件保留 state 哨兵 → gRPC status 的映射原语）。
