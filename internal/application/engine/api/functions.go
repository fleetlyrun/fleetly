// 保留窗常量与配额定律（ADR-0102 任务④；设计 v3 §10）：配额族 =
// MaxFunctionsPerProject（活跃行数）+ MaxFunctionReplicasPerProject（活跃行
// replicas 之和——Workload 数量真源口径，MaxTaskConcurrency 同义）；
// 单一定律函数 CheckFunctionQuota（CheckTaskQuota 镜像，单源纪律
// ADR-0068/F1.9）。
package api

import (
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

// ErrFunctionQuota 是函数配额哨兵（API 层映射 E_QUOTA_EXCEEDED；真源在
// 契约叶子——P2-2 决策环 defer 语义的 errors.Is 消费方同源，browses
// 配额哨兵先例）。
var ErrFunctionQuota = contract.ErrFunctionQuota

const (
	// MaxFunctionsPerProject 是 per-Project 活跃函数行数上限。
	MaxFunctionsPerProject = 50
	// MaxFunctionReplicasPerProject 是 per-Project 活跃函数 replicas 之和
	// 上限（引擎自发写点同定律 defer 语义——Phase 2 自动伸缩绝不静默超限）。
	MaxFunctionReplicasPerProject = 64
)

// CheckFunctionQuota 是函数配额定律单源（受理位事务内执法）：
// create=true 时 active 达 MaxFunctionsPerProject 即拒；replicas 合
// （sum+delta）达 MaxFunctionReplicasPerProject 即拒。
func CheckFunctionQuota(projectID string, active int, sum, delta int64, create bool) error {
	if create && active >= MaxFunctionsPerProject {
		return fmt.Errorf("%w: project %s holds %d active functions (limit %d); delete functions first",
			ErrFunctionQuota, projectID, active, MaxFunctionsPerProject)
	}
	if sum+delta > MaxFunctionReplicasPerProject {
		return fmt.Errorf("%w: project %s replica sum %d + %d would exceed the limit %d",
			ErrFunctionQuota, projectID, sum, delta, MaxFunctionReplicasPerProject)
	}
	return nil
}
