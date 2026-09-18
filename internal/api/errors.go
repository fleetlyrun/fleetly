package api

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"

	"github.com/fleetlyrun/fleetly/internal/state"
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

// conflict 构造退化信封冲突（409）。
func conflict(message string) error {
	return statusEnvelope(codes.FailedPrecondition, message)
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

// resolveApp 按名取应用行（api 面统一入口；NotFound 语义归一）。
func resolveApp(ctx context.Context, st *state.Store, name string) (state.App, error) {
	app, err := st.GetAppByName(ctx, name)
	if err != nil {
		return state.App{}, mapAppErr(err, name)
	}
	return app, nil
}
