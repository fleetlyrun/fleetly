// Package grpcapi 是 gRPC 输入面的共享 transport kernel：错误映射、行级
// 授权、分页归一与受理检查等跨业务模块的横切面。业务 handler 按限界上下文
// 分住各自包；本包只依赖 application/domain 契约与 genproto，不 import 任何
// handler 包，避免反向依赖。
package grpcapi

import (
	"errors"

	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// MapStateError 把 repo/engine 哨兵与 capability 面（指标查询拒绝等）错误
// 映射为 apperr 信封（API 层唯一出口）。已是 *apperr.Error 的错误原样穿透
// （handler 在事务 fn 内组合的应用错误不二次包裹——二次包裹会把 E_CONFLICT
// 之类吞成 E_INTERNAL）。
func MapStateError(err error, what string) error {
	var qe *capability.MetricsQueryError
	var fse *api.FrozenSpecError
	var sub *api.AdmissionError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &sub):
		// SubmitSpec 的 admission 相错误（P3-7 下沉批）：what 按 phase 归位
		// "deployment"（冻结相 "revision"）——哨兵穿透 Unwrap 照常命中，
		// 仅 NotFound/Conflict 的域插值槽归位，文案逐字节零漂移。
		return MapStateError(sub.Err, "deployment")
	case errors.Is(err, state.ErrNotFound):
		return apperr.New("E_NOT_FOUND", "%s not found", what).WithCause(err)
	case errors.Is(err, state.ErrAlreadyExists):
		// 唯一约束命中（批 0 复核回归：与 CAS/FK 冲突重新分立）——gRPC
		// AlreadyExists，REST 409；重名重试语义不静默退化成 400。
		return apperr.New("E_ALREADY_EXISTS", "%s already exists", what).WithCause(err)
	case errors.Is(err, state.ErrConflict):
		// 中性冲突文案（Q-12）：ErrConflict 承载 CAS 前置不符与 FK
		// RESTRICT（唯一约束命中已分流 E_ALREADY_EXISTS）——文案只说
		// "被并发改动或仍被引用"，具体原因留在 cause 链。
		return apperr.New("E_CONFLICT",
			"conflict: %s was changed concurrently or is still referenced; refresh and retry", what).WithCause(err)
	case errors.Is(err, api.ErrQueueFull):
		return apperr.New("E_QUEUE_FULL", "deployment queue is full for this app").WithCause(err)
	case errors.Is(err, api.ErrNotCancellable):
		return apperr.New("E_NOT_CANCELLABLE", "deployment already finished").WithCause(err)
	case errors.Is(err, api.ErrTaskTerminal):
		// Task 域哨兵的防御 fallback（ADR-0068 拓扑注记）：Task 哨兵的唯一
		// owner 是 mapTaskVerbError（生命周期动词路径）；本函数的现役
		// "task" 域调用点全为 state 读（Get/List），这三分支不可达——
		// 新增 Task 哨兵不得落此，落 mapTaskVerbError。文案自持不委托
		// （委托回 mapTaskVerbError 会与其尾部 MapStateError 回退成环）。
		return apperr.New("E_CONFLICT", "task already reached a terminal state; create a new task instead").WithCause(err)
	case errors.Is(err, api.ErrTaskNotRenewable):
		return apperr.New("E_INVALID_ARGUMENT", "one-shot tasks carry no owner lease; renew targets resident tasks").WithCause(err)
	case errors.Is(err, api.ErrNotResident):
		return apperr.New("E_INVALID_ARGUMENT", "this verb targets resident tasks only").WithCause(err)
	case errors.Is(err, api.ErrCrossProjectRefNotApproved):
		// 受理预检的 strict 拒绝（ADR-0013 附录 A.3）——可编程分支而非
		// E_INTERNAL（staging 真机实证哨兵漏映射，2026-10-02）。
		return apperr.New("E_INVALID_ARGUMENT", "%s (declare and approve the network peer, or remove the reference)", err.Error()).WithCause(err)
	case errors.Is(err, api.ErrPreDeployNetworkUnknown):
		// preDeployJobs 裸网名受理预检（B12 P3-5）——同上可编程分支。
		return apperr.New("E_INVALID_ARGUMENT", "%s", err.Error()).WithCause(err)
	case errors.Is(err, api.ErrVolumeUnresolved):
		// 卷附件 name 形解析失败（ADR-0097 决策 2：受理咽喉期解析成卷行
		// id，不在册即拒——同上可编程分支）。
		return apperr.New("E_INVALID_ARGUMENT", "%s", err.Error()).WithCause(err)
	case errors.Is(err, api.ErrLegacyVolumeCarrier):
		// 存量 adoption 显式拒绝（ADR-0097 决策 3：docker 卷不可改名，
		// v1 不做同批搬移动词）——E_CONFLICT 带迁移指引。
		return apperr.New("E_CONFLICT", "%s", err.Error()).
			WithSuggestion("The runtime still holds this volume under its pre-upgrade name-keyed carrier. Move the data to a volume created after the upgrade (backup/restore or a manual copy), or remove the runtime-side carrier if the data is disposable, then redeploy.").
			WithCause(err)
	case errors.As(err, &fse): // *api.FrozenSpecError
		// 冻结体损坏（P3-7 下沉批）：文案逐字节保持 engine 原形态
		//（"<site>: frozen revision is not valid protojson (<err>)"——api 直产的
		// 原文案随合并执行下沉，经此分支零漂移上信封）。
		return apperr.New("E_INTERNAL", "%s", err.Error()).WithCause(err)
	case errors.As(err, &qe):
		// 指标存储上游 4xx（capability.MetricsQueryError，F-B1）：查询
		// 语法/语义错属调用方错——透传上游原文落 E_INVALID_ARGUMENT，
		// 不吞成 E_INTERNAL 500（staging 实证：缺 group_left 的除法查询
		// 曾整链 500 且 cause 不落盘，误导排查方向）。
		return apperr.New("E_INVALID_ARGUMENT", "metrics query: %s", qe.Message).WithCause(err)
	case errors.Is(err, capability.ErrMetricsResultTooLarge):
		// 结果超体/超序列（H8 阶段 B）：调用方查询过宽，可缩窗重试；
		// 与上游 4xx 的 E_INVALID_ARGUMENT 分立（429）。
		return apperr.New("E_METRICS_RESULT_TOO_LARGE", "%s", err.Error()).WithCause(err)
	case errors.Is(err, capability.ErrMetricsQueryTimeout):
		// provider 上界到期（H8 阶段 B）：网关超时语义（504），客户端
		// 主动取消已在 provider 侧排除、不落本码。
		return apperr.New("E_METRICS_QUERY_TIMEOUT", "%s", err.Error()).WithCause(err)
	default:
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return ae
		}
		return apperr.New("E_INTERNAL", "internal error: %s", err.Error()).WithCause(err)
	}
}

// MapValidationError 把 spec 校验错误映射为字段级 InvalidArgument。
func MapValidationError(err error) error {
	var verr *spec.ValidationError
	if errors.As(err, &verr) {
		return apperr.New("E_INVALID_ARGUMENT", "%s: %s", verr.Field, verr.Reason).WithCause(err)
	}
	return apperr.New("E_INVALID_ARGUMENT", "%s", err.Error()).WithCause(err)
}

// MapAnchorError 把 anchor 解析错误映射为 API 信封：链上哪跳行缺失就报
// 哪跳的 404（anchor.NotFoundError.Resource 是文案单词真源，与分散各
// handler 的 MapStateError 逐字一致）；其余错误走 MapStateError 缺省
// （存储故障 E_INTERNAL）。
func MapAnchorError(err error) error {
	var nf *anchor.NotFoundError
	if errors.As(err, &nf) {
		return MapStateError(err, nf.Resource)
	}
	return MapStateError(err, "resource")
}
