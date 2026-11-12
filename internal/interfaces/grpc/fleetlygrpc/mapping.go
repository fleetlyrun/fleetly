package fleetlygrpc

import (
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// mapStateError 是 grpcapi.MapStateError 的既名委托（共享 transport kernel）。
func mapStateError(err error, what string) error { return grpcapi.MapStateError(err, what) }

// mapValidationError 是 grpcapi.MapValidationError 的既名委托。
func mapValidationError(err error) error { return grpcapi.MapValidationError(err) }

// ---- 行 ↔ proto 映射（时间戳与状态原样：存储即契约） ----

// validName 已删除（P4-1 批 1 Run B2）：最后三个调用点
//（CreateNotificationChannel/CreateTask/CreateSchedule 的 name）退役后
// 零调用——dns 标签形状执法单源住 descriptor 注解，字节一致由 entityid
// 包的 descriptor 对齐测试执法。

// listLimit 是 grpcapi.ListLimit 的既名委托（存量 handler 迁移期）。
func listLimit(n int32) int { return grpcapi.ListLimit(n) }

// mapTaskQuotaError 是 Task 配额哨兵的 errcode 面（create/scale/fire 同源）。
func mapTaskQuotaError(err error) error {
	return apperr.New("E_QUOTA_EXCEEDED",
		"this request would exceed the project task quota (%d tasks / %d desired concurrency)",
		api.MaxTasksPerProject, api.MaxTaskConcurrencyPerProject).
		WithCause(err).
		WithSuggestion("Scale down other tasks in the project, or delete finished tasks to free quota.")
}

// structureEventPayload 是 grpcapi.StructureEventPayload 的既名别名
// （schemareg 与存量事件构造点共用）。
type structureEventPayload = grpcapi.StructureEventPayload
