package api

// 其余输入面哨兵错误（真源自 engine 根包迁出，P3-7 输入面契约收口）：
// 进程/运行时/卷附件边界——API 层 errors.Is 映射 errcode，engine 零 apperr。

import "errors"

var (
	// ErrProcessUnknown 是冻结 Spec 里不存在的进程名（含从未部署形态——
	// 无冻结体即无进程可重启）。k3s 的 pod 驱逐对零命中静默，引擎侧先行
	// 校验是防假成功的唯一防线（deliverygrpc 面 map 为精确错误码）。
	ErrProcessUnknown = errors.New("process: unknown process name")
	// ErrRuntimeNoInspect 是 Runtime 未实现观测扩展接口（RuntimeInspector）
	// 的哨兵（观测面缺席精确拒绝，不静默空集——ADR-0071 决策 1；api 层
	// map 为错误码）。
	ErrRuntimeNoInspect = errors.New("runtime: no workload inspection face")
	// ErrRuntimeNoRestart 是 Runtime Provider 未实现重启扩展接口（capability
	// .RuntimeRestart 可选扩展接口——swarm/k3s 在册，第三 runtime 缺席时
	// 精确拒绝）。
	ErrRuntimeNoRestart = errors.New("component: runtime provider does not implement carrier restart")
	// ErrComponentUnknown 是未知受管 Provider 名（组件重启路由面哨兵；
	// systemgrpc 面 map 为 E_NOT_FOUND）。
	ErrComponentUnknown = errors.New("component: unknown managed provider name")
	// ErrRestartUnsupported 是该 Provider 不受管自宿（无部署形态声明，
	// 无载体可重启；systemgrpc 面 map 为 E_CONFLICT）。
	ErrRestartUnsupported = errors.New("component: provider has no managed workloads to restart")
	// ErrVolumeUnresolved 是卷附件 name 形解析失败的哨兵（卷名不在册/行已
	// 删——受理位与物化位共用判别锚；api 层 map 为 E_INVALID_ARGUMENT 带
	// 指向文案，engine 零 apperr）。
	ErrVolumeUnresolved = errors.New("volume is not registered in the project")
	// ErrLegacyVolumeCarrier 是存量 adoption 哨兵（ADR-0097 决策 3）：ensure
	// 期检测到「旧名载体在场且新键缺席」——docker 卷不可改名，v1 显式拒绝
	// 并带迁移指引（同批数据搬移动词留后续）。app 面 = 部署失败文案；库面 =
	// 收敛环拒绝确保。
	ErrLegacyVolumeCarrier = errors.New("legacy name-keyed volume carrier present")
)
