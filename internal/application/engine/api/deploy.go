package api

// 部署受理面 DTO 与哨兵（真源自 engine 根包迁出，P3-7 输入面契约收口）：
// 四源归一化/旗标互斥/hook 闸门留在 api 层，本包只承载受理请求形状、
// 判定附注与错误锚——engine 零 apperr，errcode 映射住 interfaces 层。

import (
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
)

// MergeMode 是冻结前与当前冻结体的合并方向（ADR-0072 所有权分裂）。
type MergeMode uint8

const (
	// MergeNone 直投不合并（非 hook-owned 或 override_hook 全量接管）。
	MergeNone MergeMode = iota
	// MergeMaterial push/refresh：新材料 + 冻结运行时——MergeHookOwned(提交件, 冻结体)。
	MergeMaterial
	// MergeOverlay spec_file overlay：冻结材料 + 提交件运行时（提交件
	// source 忽略——防旧 ref 构建旧 commit）——MergeHookOwned(冻结体, 提交件)。
	MergeOverlay
)

// SubmitSpecRequest 是规格受理请求（四源归一化/旗标互斥/hook 闸门留在
// api——请求形状执法 + engine 零 apperr；engine 执合成/冻结/admission）。
type SubmitSpecRequest struct {
	// AppID 是目标 App 行 ID。
	AppID string
	// Spec 是 api 归一化产物（未合成平台变量）。
	Spec *specv1.AppSpec
	// SourceText 是冻结原文（compose yaml；其余形态空）。
	SourceText string
	// HookOwned 是 api 闸门判定产物（Merge != MergeNone 时必须 true——防御断言）。
	HookOwned bool
	// Merge 是冻结前与当前冻结体的合并方向。
	Merge MergeMode
	// Submit 非 nil = 冻结后走 admission；nil = 只冻结不部署。
	Submit *SubmitOptions
	// Confirmation 是 B1 确认票的受理入参透传（设计稿 §1.2：confirmation
	// 是 SubmitSpec 的入参；§1.4 甲'：④⑤内容绑定与烧票在 Submit 的
	// appMu 临界区内——冻结体产出之后）。只在 Submit != nil 时消费；
	// 只冻结形态携带 = 编程错误（预览管线铸票在 W2a，不占本入参）。
	Confirmation *ConfirmInput
}

// SubmitOptions 是 admission 透传面（幂等键/commit 去重锚/显式抢占）。
type SubmitOptions struct {
	IdempotencyKey string
	CommitSHA      string
	Supersede      bool
}

// FrozenSpecError 是冻结体损坏（protojson 反序列化失败）的类型化错误：
// 文案逐字节保持 api 原形态（"<site>: frozen revision is not valid
// protojson (<err>)"，site = "hook merge" | "hook-owned overlay"）——api
// 层映射 E_INTERNAL 时以 err.Error() 原文上信封（不变式⑦）。
type FrozenSpecError struct {
	Site string
	Err  error
}

func (e *FrozenSpecError) Error() string {
	return fmt.Sprintf("%s: frozen revision is not valid protojson (%v)", e.Site, e.Err)
}

func (e *FrozenSpecError) Unwrap() error { return e.Err }

// AdmissionError 标记 SubmitSpec 的 admission 相错误（不变式⑥双事务的映
// 射孪面）：api 按 phase 域插值——冻结相 what="revision"、admission 相
// what="deployment"（mapStateError 的 NotFound/Conflict 文案槽），文案
// 逐字节零漂移；哨兵判定经 Unwrap 照常穿透。
type AdmissionError struct {
	Err error
}

func (e *AdmissionError) Error() string { return e.Err.Error() }

func (e *AdmissionError) Unwrap() error { return e.Err }

// 部署受理面哨兵错误（API 层映射 errcode；engine 自身不依赖 apperr）。
var (
	// ErrQueueFull 是 admission 排队容量已满（ADR-0016：queue 满显式反馈，
	// 不是 409——请求没错，是背压）。
	ErrQueueFull = errors.New("engine: deployment queue is full")
	// ErrNotCancellable 是目标 Deployment 已终态（取消只作用于排队/在途）。
	ErrNotCancellable = errors.New("engine: deployment already finished")
	// ErrActiveDeployment 是 DeleteApp 收口的锁内预检命中活跃部署（ADR-0023
	// 修订：拒绝于拆载体等一切副作用之前；API 层映射 E_CONFLICT）。
	ErrActiveDeployment = errors.New("engine: app has active deployments")
	// ErrNoSuccessfulBaseline 是 Rollback 无成功基线（首次部署无回滚对象；
	// API 层映射 E_NO_BASELINE——Q-13：判定走 errors.Is，不靠文案匹配）。
	ErrNoSuccessfulBaseline = errors.New("engine: no successful baseline to roll back to")
	// ErrCrossProjectRefNotApproved 是部署受理命中未批准的跨 Project 网络
	// 引用（ADR-0013 附录 A.3 fail-closed：declare+approve 或移除引用；
	// API 层映射 E_CONFLICT）。
	ErrCrossProjectRefNotApproved = errors.New("engine: cross-project network reference is not approved")
	// ErrPreDeployNetworkUnknown 是 preDeployJobs 受理命中项目内不存在的
	// 裸网名（B12 P3-5 fail-closed：建网/taskGroup:/project: 引用/删附件；
	// API 层映射 E_INVALID_ARGUMENT）。
	ErrPreDeployNetworkUnknown = errors.New("engine: pre-deploy job attaches an unknown network")
	// ErrConfirmStale 是基线漂移（含服务端输入漂移——变量/hook 行变化）
	// 或票过期：世界已不是预览时的世界，重预览。
	ErrConfirmStale = errors.New("engine: confirmation ticket is stale against the current baseline")
	// ErrConfirmMismatch 是请求与票面指纹不符（④）或票不可用（伪造/已
	// 烧/目标不符）。
	ErrConfirmMismatch = errors.New("engine: confirmation ticket does not match the submitted request")
)

// ConfirmInput 是确认票的 engine 受理入参（confirmation 的引擎视图；
// 拦截器通用验票已过，此处只做内容绑定）。
type ConfirmInput struct {
	// Ticket 是 Change Preview 铸造的票据标识。
	Ticket string
	// Skip 是 --yes 逃生门：不验票不烧票，受理照常（审计 proof=skipped）。
	Skip bool
	// Semantics 是本请求的服务端提交语义位（④指纹输入半边：Deploy =
	// supersede/override_hook；Rollback = 恒 supersede）。
	Semantics governance.ConfirmSemantics
}

// PreDeployTaskID 返回部署当前锚定的 job Task（API/CLI 暴露面：无 job 或
// 已完成返回空）。
func PreDeployTaskID(d *deployment.Deployment) string {
	_, taskID, anchored := d.PreDeployAnchor()
	if !anchored {
		return ""
	}
	return taskID
}
