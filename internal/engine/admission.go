package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
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
	Kind           string // 可选；来源标注（KindRollback 等，进审计）
}

// Submit 走 admission 判定（ADR-0016，判定全在单事务内）：
//
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
	var out *deployment.Deployment
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		// 1. 幂等键去重。
		if req.IdempotencyKey != "" {
			existing, err := e.deployments.FindActiveByIdempotencyKey(ctx, tx, req.IdempotencyKey)
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
		// 在途 Generation 由本新 Deployment 收口）。
		if req.Supersede {
			for _, d := range inFlight {
				if err := e.transit(ctx, tx, d,
					deployment.ActiveStatesNoQueued(), deployment.StateSuperseded,
					func(m *deployment.Deployment) { m.SupersededBy = newID }); err != nil {
					return err
				}
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
		d := &deployment.Deployment{
			ID: newID, AppID: req.AppID,
			FromRevision:   e.lastDeployedRevision(ctx, tx, req.AppID),
			ToRevision:     req.RevisionID,
			State:          deployment.StateQueued,
			Generation:     gen,
			IdempotencyKey: req.IdempotencyKey,
			CommitSHA:      req.CommitSHA,
		}
		if err := e.deployments.Create(ctx, tx, d); err != nil {
			return err
		}
		if err := e.emitDeploymentEvent(ctx, tx, d); err != nil {
			return err
		}
		out = d
		return e.audits.Append(ctx, tx, &audit.Entry{
			ID: ulid.Make().String(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "deployment.create", Resource: "deployment/" + d.ID,
			AfterFP: d.ToRevision,
		})
	})
	if err != nil {
		return nil, err
	}
	e.loop.Kick()
	return out, nil
}

// Cancel 取消排队或在途 Deployment（ADR-0016：排队与在途均可取消）。
// 在途取消是"停止推进"语义：当前 step 收尾后不再迁移；已下发的
// Workload 由后续部署或 Remove 收口（N0 不自动拆除——诚实暴露）。
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
	e.loop.Kick()
	return out, nil
}

// lastDeployedRevision 返回 App 当前基线（最近一次终态成功的 to_revision；
// 首次部署为空——回滚无对象，失败即终态，领域模型 §4）。
func (e *Engine) lastDeployedRevision(ctx context.Context, tx *sql.Tx, appID string) string {
	rows, err := tx.QueryContext(ctx,
		`SELECT to_revision FROM deployments WHERE app_id = ? AND state = 'succeeded'
		 ORDER BY id DESC LIMIT 1`, appID)
	if err != nil {
		return "" // 基线解析失败按"首次"处理会在失败时缺少回滚对象——查不到即无成功基线
	}
	defer rows.Close() //nolint:errcheck // 只读单行，关闭错误无处置面
	if rows.Next() {
		var rev string
		if rows.Scan(&rev) == nil {
			return rev
		}
	}
	return ""
}

// transit 是四件一拍的组合点（状态 CAS + deployment.<state> 事件 + 审计；
// 部署记录无 tombstone）。tx 由调用方事务传入。原地迁移（from 含 to，
// 仅落 deadline/generation 等伴生字段）不发事件——事件是状态迁移的既成
// 事实，状态未变不是迁移；审计照落。
func (e *Engine) transit(ctx context.Context, tx *sql.Tx, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) error {
	if err := e.deployments.Transit(ctx, tx, d.ID, from, to, mut); err != nil {
		return err
	}
	fresh, err := e.deployments.Get(ctx, tx, d.ID)
	if err != nil {
		return err
	}
	if fresh.State != to || !stateIn(from, to) {
		if err := e.emitDeploymentEvent(ctx, tx, fresh); err != nil {
			return err
		}
	}
	return e.audits.Append(ctx, tx, &audit.Entry{
		ID: ulid.Make().String(), Source: audit.SourceSystem,
		Action: "deployment.transit", Resource: "deployment/" + fresh.ID,
		AfterFP: string(fresh.State),
	})
}

// stateIn 报告 to 是否在 from 集（原地迁移判定）。
func stateIn(set []deployment.State, s deployment.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// emitDeploymentEvent 落 deployment.<state> 事件（事件名经 eventStateName
// 映射，字面量锚定在 events.go）。
func (e *Engine) emitDeploymentEvent(ctx context.Context, tx *sql.Tx, d *deployment.Deployment) error {
	_, err := e.outbox.Append(ctx, tx, eventStateName(d.State), "deployment", d.ID, deploymentEventPayloadJSON(d))
	return err
}
