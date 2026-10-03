package engine

// 载体卫生清扫面（收尾批 E29；janitor 保留窗节拍的消费口）。engine 拥有
// Task 域载体生命周期与 ns 解析真源（taskTeam），清扫的编排判定住本包，
// 节拍与预算由装配层 janitor 给定——与 retention janitor 的 DB/blob 清扫
// 同一文化：删除面集中，节拍限流在调用方。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// SweepOrphanSecretCarriers 清扫非现役的受管 Secret 载体（RuntimeHygiene
// 子面透传；现役集由 Provider 依服务引用自判定，E29-1）。Runtime 未实现
// 该子面时静默跳过（与 Inspector 的降级文化一致，返回 0,nil）。maxDelete
// 是单次删除预算（janitor 节拍限流防 API 风暴）。
func (e *Engine) SweepOrphanSecretCarriers(ctx context.Context, maxDelete int) (int, error) {
	h := capability.FacesOf(e.runtime).Hygiene // 清扫子面（FacesOf 协商点）
	if h == nil || maxDelete <= 0 {
		return 0, nil
	}
	return h.SweepOrphanSecrets(ctx, maxDelete)
}

// SweepTerminalTaskCarriers 对窗内收口的终态 Task 逐个拆除隔离域残留载体
// （runtime.Remove；E29-2 兜底面）。正常链路的残留收敛是终态收口拍自身的
// 空集 Ensure（task.go 收口次序：Ensure 成功才落终态迁移）——本扫兜的
// 两类残余：升级前"先迁移后 Ensure"缺陷期的存量（swarm 0/1 形态），与
// 未来未知路径的防御纵深。有界三重：窗内候选集（finished_at >= 截点）、
// 单拍行数上限、每 Task 一次 ServiceList 级 Remove（无载体即一次列表调
// 用）——已收敛行是幂等 no-op，重复成本一次列表读。
//
// 单 Task 失败只记日志继续（清扫面的可用性真源是"扫完"，sweepRevokedOwners
// 同款裁决）；返回成功拆除的 Task 数。window<=0 或 limit<=0 = 本拍停用。
func (e *Engine) SweepTerminalTaskCarriers(ctx context.Context, window time.Duration, limit int) (int, error) {
	if window <= 0 || limit <= 0 {
		return 0, nil
	}
	cutoff := state.FormatTime(e.clock.Now().Add(-window))
	rows, err := e.tasks.ListRecentlyFinished(ctx, e.db.Runner(), cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("terminal carrier sweep: list recently finished: %w", err)
	}
	swept := 0
	for i := range rows {
		t := &rows[i]
		if t.State != task.StateCompleted && t.State != task.StateFailed && t.State != task.StateDrained {
			continue // repo 面冗余防御：deleted 无残留面（DeleteTask 先收口后落账）
		}
		team, err := e.projectTeam(ctx, t.ProjectID)
		if err != nil {
			if !errors.Is(err, state.ErrNotFound) {
				e.log.Error("terminal carrier sweep: resolve team", "task", t.ID, "err", err)
			}
			continue
		}
		stepCtx, cancel := e.boundedStep(ctx)
		ns := capability.NamespaceRef{Team: team, Project: t.ProjectID, Task: t.ID}
		err = e.runtime.Remove(stepCtx, ns)
		cancel()
		if err != nil {
			e.log.Error("terminal carrier sweep: remove", "task", t.ID, "err", err)
			continue
		}
		swept++
	}
	return swept, nil
}
