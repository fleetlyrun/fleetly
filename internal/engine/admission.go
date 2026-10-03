package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// SubmitRequest 是 admission 输入（Revision 已由 API 层冻结；engine 只接
// Deployment 生命周期）。
type SubmitRequest struct {
	AppID          string
	RevisionID     string // 目标 Revision
	IdempotencyKey string // 可选；活跃期去重
	CommitSHA      string // 可选；git 触发的 commit 去重锚
	Supersede      bool   // 显式抢占在途部署（ADR-0016）
	Kind           string // 可选；来源标注（KindRollback：审计标注 + first_boot 游标直落 done——回滚永不重跑 firstBootJobs，ADR-0030 决策 5）
}

// Submit 走 admission 判定（ADR-0016，判定全在单事务内）：
//
//  0. App 存活判定（ADR-0023 统一口径）：已删 App 一律不存在，事务内
//     拒绝（E_NOT_FOUND）——与 DeleteApp 最终事务的 ActiveByApp 复查
//     互为对偶：单连接事务串行下，删除与受理的先后在此闭合，删后
//     deploy 不再重建载体；
//  1. 同幂等键（活跃）→ 返回既有（去重）；
//  2. 同 App 同 commit（活跃）→ 返回既有（webhook 重复投递去重）；
//  3. 每 App 排队容量 → ErrQueueFull（背压反馈，非冲突）；
//  4. 显式 supersede → 在途（preparing..observing/rolling-back）行转
//     superseded；默认不抢占在途；
//  5. latest-wins：更早的 queued 行合并（转 superseded，被新请求取代）；
//  6. 落 queued 行（Generation = App 内单调 +1）+ deployment.queued 事件
//     + 审计，同事务。
//
// 409 语义（收窄）：幂等键并发冲突由活跃唯一索引兜底（此处事务串行化后
// 不会发生），保留给未来互斥资源锁；本面不产生 409。
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*deployment.Deployment, error) {
	// App 级互斥（N0.1 P1-3）：与基线重放/收口共享——admission 落行与
	// 重放的复查被串行化（在途重放 Ensure 期间受理排队，锁内复查所见
	// 即终局）。
	appMu := e.lockApp(req.AppID)
	appMu.Lock()
	defer appMu.Unlock()

	var out *deployment.Deployment
	var supersededIDs []string
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		// 0. App 存活判定（API/webhook 的预读只是快速失败面；权威判定在此）。
		appRow, err := e.apps.Get(ctx, tx, req.AppID)
		if err != nil {
			return err
		}
		// 0.5 跨 Project 引用受理预检（ADR-0013 附录 A.3 fail-closed 第一道；
		// prepare 的投影预检是第二道）：未 approved 的引用拒绝入队——
		// 排队中途被撤销仍会在 prepare/投影失败至终态，双道闭合。
		if err := e.CheckPeerRefs(ctx, tx, appRow.ProjectID, req.RevisionID); err != nil {
			return err
		}
		// 0.6 firstBootJobs 裸网名在场性预检（B12 P3-5 fail-closed）：项目内
		// 无该网的裸网名拒绝入队——typo 在受理位显式失败，不冻结进 spec
		// 等 job 超时才暴露。
		if err := e.CheckFirstBootNetworks(ctx, tx, appRow.ProjectID, req.RevisionID); err != nil {
			return err
		}

		// 1. 幂等键去重（键作用域 = App，B10：异 App 同键各自独立受理）。
		if req.IdempotencyKey != "" {
			existing, err := e.deployments.FindActiveByIdempotencyKey(ctx, tx, req.AppID, req.IdempotencyKey)
			if err == nil {
				out = existing
				return nil
			}
			if !errors.Is(err, state.ErrNotFound) {
				return err
			}
		}

		active, err := e.deployments.ActiveByApp(ctx, tx, req.AppID)
		if err != nil {
			return err
		}
		var queued, inFlight []*deployment.Deployment
		for i := range active {
			d := &active[i]
			// 2. commit 去重（活跃同 commit → 既有）。
			if req.CommitSHA != "" && d.CommitSHA == req.CommitSHA {
				out = d
				return nil
			}
			if d.State == deployment.StateQueued {
				queued = append(queued, d)
			} else {
				inFlight = append(inFlight, d)
			}
		}

		// 3. 排队容量（在途不占排队位）。
		if len(queued) >= e.opts.QueueCapacity {
			return fmt.Errorf("%w: %d queued for app %s (capacity %d)",
				ErrQueueFull, len(queued), req.AppID, e.opts.QueueCapacity)
		}

		gen, err := e.deployments.NextGeneration(ctx, tx, req.AppID)
		if err != nil {
			return err
		}
		newID := ulid.Make().String()

		// 4. 显式 supersede 抢占在途（至多一条；观察窗与发布权即时移交——
		// 在途 Generation 由本新 Deployment 收口）。被抢占部署锚定的
		// firstBootJobs 在事务后 best-effort 强停（ADR-0030 决策 6——
		// StopTask 自开事务，不得嵌套）。
		if req.Supersede {
			for _, d := range inFlight {
				if err := e.transit(ctx, tx, d,
					deployment.ActiveStatesNoQueued(), deployment.StateSuperseded,
					func(m *deployment.Deployment) { m.SupersededBy = newID }); err != nil {
					return err
				}
				supersededIDs = append(supersededIDs, d.ID)
			}
		}
		// 5. latest-wins：既有 queued 全部让位（active 按 id 升序 → queued 亦然）。
		for _, d := range queued {
			if err := e.transit(ctx, tx, d,
				[]deployment.State{deployment.StateQueued}, deployment.StateSuperseded,
				func(m *deployment.Deployment) { m.SupersededBy = newID }); err != nil {
				return err
			}
		}

		// 6. 受理落行 + 事件 + 审计。
		baseline, err := e.lastDeployedRevision(ctx, tx, req.AppID)
		if err != nil {
			return err
		}
		d := &deployment.Deployment{
			ID: newID, AppID: req.AppID,
			FromRevision:   baseline,
			ToRevision:     req.RevisionID,
			State:          deployment.StateQueued,
			Generation:     gen,
			IdempotencyKey: req.IdempotencyKey,
			CommitSHA:      req.CommitSHA,
		}
		if req.Kind == KindRollback {
			// 回放部署直落 done 游标（ADR-0030 决策 5）：回滚（自动/显式）
			// 永不重跑 firstBootJobs——迁移已应用，重跑反而破坏。release 的
			// driveFirstBootJobs 见 done 游标直接进 carrier 子相位。
			d.FirstBoot = deployment.FirstBootDone
		}
		// 6. 受理落行 + 事件 + 审计（commitWrite 序列真源）。审计带来源
		// 标注（transit 的 AfterFP 后缀同款形态）：Kind 不再是死参数——
		// 回放部署在审计流里可辨（"; kind=rollback"）。
		afterFP := d.ToRevision
		if req.Kind != "" {
			afterFP = fmt.Sprintf("%s; kind=%s", d.ToRevision, req.Kind)
		}
		if err := e.commitWrite(ctx, tx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error {
				return e.deployments.Create(ctx, tx, d)
			},
			events: []func() eventFact{func() eventFact {
				return eventFact{name: eventStateName(d.State), aggregate: "deployment", id: d.ID, payload: deploymentEventPayloadJSON(d)}
			}},
			audits: []auditFact{{action: "deployment.create", resource: "deployment/" + d.ID, afterFP: afterFP, actorCtx: true}},
		}); err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range supersededIDs {
		if fresh, err := e.deployments.Get(ctx, e.db.Runner(), id); err == nil {
			e.abandonFirstBootJobs(ctx, fresh)
		} else {
			e.log.Error("submit: supersede abandon lookup", "deployment", id, "err", err)
		}
	}
	e.loop.Kick()
	return out, nil
}

// Cancel 取消排队或在途 Deployment（ADR-0016：排队与在途均可取消）。
// 在途取消是"停止推进"语义：当前 step 收尾后不再迁移；已下发的
// Workload 由后续部署或 Remove 收口（N0 不自动拆除——诚实暴露）。
// jobs 等待中的部署取消后，锚定 job Task best-effort 强停（ADR-0030
// 决策 6：审计携带操作者）。
func (e *Engine) Cancel(ctx context.Context, id string) (*deployment.Deployment, error) {
	var out *deployment.Deployment
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		d, err := e.deployments.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if d.State.Terminal() {
			return fmt.Errorf("%w: deployment %s is %s", ErrNotCancellable, id, d.State)
		}
		if err := e.transit(ctx, tx, d,
			deployment.ActiveStates(), deployment.StateCancelled, nil); err != nil {
			return err
		}
		out, err = e.deployments.Get(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	e.ensureForget(e.delivery.release, id) // 取消是 releasing 的另一出口：物化备忘随相位作废（C17）
	e.abandonFirstBootJobs(ctx, out)
	e.loop.Kick()
	return out, nil
}

// lastDeployedRevision 返回 App 当前基线（最近一次终态成功的 to_revision；
// 首次部署为空——回滚无对象，失败即终态，领域模型 §4）。查询失败如实
// 上抛（C5：吞错误按"首次"处理会在失败时缺回滚对象——错误面进不了
// 事务，整单拒绝让调用方看到存储故障）。
func (e *Engine) lastDeployedRevision(ctx context.Context, tx *sql.Tx, appID string) (string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT to_revision FROM deployments WHERE app_id = ? AND state = 'succeeded'
		 ORDER BY id DESC LIMIT 1`, appID)
	if err != nil {
		return "", fmt.Errorf("resolve baseline: %w", err)
	}
	defer rows.Close() //nolint:errcheck // 只读单行，关闭错误无处置面
	if rows.Next() {
		var rev string
		if err := rows.Scan(&rev); err != nil {
			return "", fmt.Errorf("resolve baseline: %w", err)
		}
		return rev, nil
	}
	return "", nil
}

// transit（四件一的 Deployment 前门）与 transitAndReload 已收口至
// transition.go——序列与规则（原地迁移不发事件、AfterFP 修正）单点拥有。
// deployment 事件的发射面也随之前门化（eventStateName 字面量锚定在
// events.go，usage 反扫不受影响）。
