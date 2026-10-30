package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// SubmitRequest 是 admission 输入（Revision 已由 API 层冻结；engine 只接
// Deployment 生命周期）。
type SubmitRequest struct {
	AppID          string
	RevisionID     string // 目标 Revision
	IdempotencyKey string // 可选；活跃期去重
	CommitSHA      string // 可选；git 触发的 commit 去重锚
	Supersede      bool   // 显式抢占在途部署（ADR-0016）
	Kind           string // 可选；来源标注（KindRollback：审计标注 + pre_deploy 游标直落 done——回滚永不重跑 preDeployJobs，ADR-0030 决策 5）
	// Confirm 是 B1 确认票消费入参（nil = 非强制面路径：webhook/git 触发
	// 与内部受理天然豁免——设计稿 §1.2 豁免族）。非 nil 时 ④digest/⑤base
	// 内容绑定与本字段同临界区（appMu）闭合，见 confirm.go。
	Confirm *ConfirmInput
	// ConfirmSpecBody 是④比对的载荷锚：Deploy = 本临界区产出的冻结体
	// marshalSpec 规范形（SubmitSpec 透传 rev.Spec——比对对象是冻结体，
	// 非 handler 侧重算，消灭合成双跑）；Rollback = 目标 Revision 冻结体。
	// Confirm 非 nil 且非 skip 时必填。
	ConfirmSpecBody []byte
}

// AdmissionOutcome 是 admission 判定形态（P10 响应显式化；取值与 proto
// Admission.outcome 一致，kebab-case 用户可见）。
type AdmissionOutcome string

const (
	// AdmissionQueued：新受理，无合并无抢占。
	AdmissionQueued AdmissionOutcome = "queued"
	// AdmissionMerged：latest-wins——既有 queued 请求被本请求合并取代。
	AdmissionMerged AdmissionOutcome = "merged"
	// AdmissionSuperseded：显式 supersede 抢占在途（同批合并的 queued 不另
	// 报——显式动作优先归因）。
	AdmissionSuperseded AdmissionOutcome = "superseded"
	// AdmissionDeduplicated：同幂等键或同 commit 命中活跃既有部署。
	AdmissionDeduplicated AdmissionOutcome = "deduplicated"
)

// Admission 是 Submit 的判定附注（ADR-0016 语义的响应面）：调用方可判定
// "去重命中/排队第几位"。幂等层命中（ADR-0024）在 api 层逐字节重放原始
// 响应、不产生 Admission——幂等层与 admission 去重由此分立。
type Admission struct {
	Outcome  AdmissionOutcome
	Position int    // per-App 串行管线位次（1 = 队头/下一执行）
	Existing string // deduplicated 时命中的既有部署 ID
}

// admissionPosition 算目标部署在活跃集内的串行位次（1 = 队头）：在途部署
// 排在 queued 前；双在途按 Generation 序（per-App 串行下至多一条在途，
// 代次序是防御形态）。被合并/被抢占的行已离场，天然不计位。
func admissionPosition(active []deployment.Deployment, target *deployment.Deployment) int {
	pos := 1
	for i := range active {
		x := &active[i]
		if x.ID == target.ID {
			continue
		}
		switch {
		case x.State != deployment.StateQueued && target.State == deployment.StateQueued:
			pos++
		case x.State != deployment.StateQueued && target.State != deployment.StateQueued && x.Generation < target.Generation:
			pos++
		}
	}
	return pos
}

// Submit 走 admission 判定（ADR-0016，判定全在单事务内；返回值第二件是
// 判定附注 Admission——P10 响应显式化，outcome/position 与判定同事务产
// 出，所见即终局）：
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
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*deployment.Deployment, *Admission, error) {
	// App 级互斥（N0.1 P1-3）：与基线重放/收口共享——admission 落行与
	// 重放的复查被串行化（在途重放 Ensure 期间受理排队，锁内复查所见
	// 即终局）。
	appMu := e.lockApp(req.AppID)
	appMu.Lock()
	defer appMu.Unlock()

	// B1 Confirm Ticket 内容绑定④（设计稿 §1.4 甲'：验票在 appMu 受理
	// 临界区内、冻结体产出之后）：票面复核 + 目标锚比对（peek 不删——
	// 烧票在 admission 相落库成功后）。并发同票第二请求在此排队，进锁后
	// 撞 tombstone（已确认过）。request_digest 的比对在事务内 2.5 步。
	plan, cerr := e.peekConfirmTicket(req.Confirm, req.AppID)
	if cerr != nil {
		return nil, nil, cerr
	}

	var out *deployment.Deployment
	var adm *Admission
	var supersededIDs []string
	err := e.tx.Do(ctx, func(s transact.Stores) error {
		// 0. App 存活判定（API/webhook 的预读只是快速失败面；权威判定在此）。
		appRow, err := s.Apps.Get(ctx, req.AppID)
		if err != nil {
			return err
		}
		// 0.5 跨 Project 引用受理预检（ADR-0013 附录 A.3 fail-closed 第一道；
		// prepare 的投影预检是第二道）：未 approved 的引用拒绝入队——
		// 排队中途被撤销仍会在 prepare/投影失败至终态，双道闭合。
		if err := e.CheckPeerRefs(ctx, s, appRow.ProjectID, req.RevisionID); err != nil {
			return err
		}
		// 0.6 preDeployJobs 裸网名在场性预检（B12 P3-5 fail-closed）：项目内
		// 无该网的裸网名拒绝入队——typo 在受理位显式失败，不冻结进 spec
		// 等 job 超时才暴露。
		if err := e.CheckPreDeployNetworks(ctx, s, appRow.ProjectID, req.RevisionID); err != nil {
			return err
		}

		// 1. 幂等键去重（键作用域 = App，B10：异 App 同键各自独立受理）。
		//    命中即 deduplicated 附注（位次 = 既有部署在活跃集内的位置）。
		//    B1：去重命中也是受理成功——票面审计行照落（锚既有行），烧票
		//    在事务提交后统一执行（confirm.go 烧票契约）。
		if req.IdempotencyKey != "" {
			existing, err := s.Deployments.FindActiveByIdempotencyKey(ctx, req.AppID, req.IdempotencyKey)
			if err == nil {
				active, aerr := s.Deployments.ActiveByApp(ctx, req.AppID)
				if aerr != nil {
					return aerr
				}
				if lerr := e.confirmLedger(ctx, s, plan, existing.ID); lerr != nil {
					return lerr
				}
				out = existing
				adm = &Admission{Outcome: AdmissionDeduplicated, Position: admissionPosition(active, existing), Existing: existing.ID}
				return nil
			}
			if !errors.Is(err, state.ErrNotFound) {
				return err
			}
		}

		active, err := s.Deployments.ActiveByApp(ctx, req.AppID)
		if err != nil {
			return err
		}
		var queued, inFlight []*deployment.Deployment
		for i := range active {
			d := &active[i]
			// 2. commit 去重（活跃同 commit → 既有）。
			if req.CommitSHA != "" && d.CommitSHA == req.CommitSHA {
				if lerr := e.confirmLedger(ctx, s, plan, d.ID); lerr != nil {
					return lerr
				}
				out = d
				adm = &Admission{Outcome: AdmissionDeduplicated, Position: admissionPosition(active, d), Existing: d.ID}
				return nil
			}
			if d.State == deployment.StateQueued {
				queued = append(queued, d)
			} else {
				inFlight = append(inFlight, d)
			}
		}

		// 2.5 B1 内容绑定（设计稿 §1.4 ④⑤，辨因序：世界先核对）：
		//  ⑤ base 多锚比对——最新冻结体/LatestSucceeded/活跃变量环境摘要/
		//  hook 行版本与票面锚全等（与落行同事务快照；最新冻结体锚排除
		//  self 形态，见 checkConfirmBase）。漂移即 STALE：变量/hook 行
		//  变化改写合成结果使 digest 必变的归此腿，不误判 MISMATCH
		// （独立审查轮 #7 的归腿要求——base 先于 digest 判）。
		//  ④ request_digest 与本临界区冻结体规范形比对（含 Semantics）：
		//  世界一致而不符才是请求被改 → MISMATCH（指纹单源 governance.
		//  ConfirmRequestDigest，铸票面同参数）。
		//  去重命中不做两比对（既有行即受理终局，判定无意义）。
		if plan != nil && plan.peeked != nil {
			if berr := e.checkConfirmBase(ctx, s, req.AppID, appRow.ProjectID, revisionDigest(req.ConfirmSpecBody), plan.peeked.Base); berr != nil {
				return berr
			}
			if governance.ConfirmRequestDigest(req.ConfirmSpecBody, req.Confirm.Semantics) != plan.peeked.RequestDigest {
				return ErrConfirmMismatch
			}
		}

		// 3. 排队容量（在途不占排队位）。
		if len(queued) >= e.opts.Deploy.QueueCapacity {
			return fmt.Errorf("%w: %d queued for app %s (capacity %d)",
				ErrQueueFull, len(queued), req.AppID, e.opts.Deploy.QueueCapacity)
		}

		gen, err := s.Deployments.NextGeneration(ctx, req.AppID)
		if err != nil {
			return err
		}
		dseq, err := s.NextSeq(ctx, "deployments")
		if err != nil {
			return err
		}
		newID := entityid.Derive(req.AppID, "d", dseq)

		// 4. 显式 supersede 抢占在途（至多一条；观察窗与发布权即时移交——
		// 在途 Generation 由本新 Deployment 收口）。被抢占部署锚定的
		// preDeployJobs 在事务后 best-effort 强停（ADR-0030 决策 6——
		// StopTask 自开事务，不得嵌套）。
		if req.Supersede {
			for _, d := range inFlight {
				if err := e.transit(ctx, s, d,
					deployment.ActiveStatesNoQueued(), deployment.StateSuperseded,
					func(m *deployment.Deployment) { m.SupersededBy = newID }); err != nil {
					return err
				}
				supersededIDs = append(supersededIDs, d.ID)
			}
		}
		// 5. latest-wins：既有 queued 全部让位（active 按 id 升序 → queued 亦然）。
		for _, d := range queued {
			if err := e.transit(ctx, s, d,
				[]deployment.State{deployment.StateQueued}, deployment.StateSuperseded,
				func(m *deployment.Deployment) { m.SupersededBy = newID }); err != nil {
				return err
			}
		}

		// 6. 受理落行 + 事件 + 审计。
		baseline, baselineGen, err := e.lastDeployedRevision(ctx, s, req.AppID)
		if err != nil {
			return err
		}
		d := &deployment.Deployment{
			ID: newID, AppID: req.AppID,
			FromRevision:   baseline,
			FromGeneration: baselineGen, // 双代窗旧代锚（ADR-0048）：与 from_revision 同行取 gen
			ToRevision:     req.RevisionID,
			State:          deployment.StateQueued,
			Generation:     gen,
			IdempotencyKey: req.IdempotencyKey,
			CommitSHA:      req.CommitSHA,
		}
		if req.Kind == KindRollback {
			// 回放部署直落 done 游标（ADR-0030 决策 5）：回滚（自动/显式）
			// 永不重跑 preDeployJobs——迁移已应用，重跑反而破坏。release 的
			// drivePreDeployJobs 见 done 游标直接进 carrier 子相位。
			d.PreDeploy = deployment.PreDeployDone
		}
		// 6. 受理落行 + 事件 + 审计（commitWrite 序列真源）。审计带来源
		// 标注（transit 的 AfterFP 后缀同款形态）：Kind 不再是死参数——
		// 回放部署在审计流里可辨（"; kind=rollback"）。
		afterFP := d.ToRevision
		if req.Kind != "" {
			afterFP = fmt.Sprintf("%s; kind=%s", d.ToRevision, req.Kind)
		}
		// B1 LedgerOnly 审计行（confirm.go ledgerFact）：带票与 skip 两形态
		// 同拍落库——取证面可辨「确认过」「跳过」；无 confirm 面零行为。
		facts := []func() writes.FactRow{func() writes.FactRow {
			fr := writes.FactRow{
				Name: eventStateName(d.State), Aggregate: "deployment", ID: d.ID,
				Payload: deploymentEventPayloadJSON(d), TeamID: e.eventTeamOfApp(ctx, s, d.AppID),
				Action: "deployment.create", Resource: "deployment/" + d.ID, AfterFP: afterFP,
				Actor:  authn.ActorFromContext(ctx),
				Source: authn.SourceFromContext(ctx),
			}
			return fr
		}}
		if plan != nil {
			facts = append(facts, func() writes.FactRow { return plan.ledgerFact(ctx, d.ID) })
		}
		if err := e.commitWrite(ctx, s, writeFact{
			write: func(ctx context.Context, s transact.Stores) error {
				return s.Deployments.Create(ctx, d)
			},
			facts: facts,
		}); err != nil {
			return err
		}
		// 判定附注：显式抢占 > latest-wins 合并 > 朴素受理；位次 = 剩余
		// 在途数 + 1（supersede 时在途已全部转场，位次恒 1）。
		outcome := AdmissionQueued
		switch {
		case len(supersededIDs) > 0:
			outcome = AdmissionSuperseded
		case len(queued) > 0:
			outcome = AdmissionMerged
		}
		position := 1
		if !req.Supersede {
			position += len(inFlight)
		}
		out = d
		adm = &Admission{Outcome: outcome, Position: position}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// B1 烧票契约（设计稿 §1.4）：discard 仅在 admission 相落库成功后——
	// 冻结成功而 admission 拒（queue full 等）在上方 return err 出锁，票
	// 保留可重试（重试经冻结体内容寻址复用，④仍成立）；deduplicated 命中
	// 也是受理成功，同烧。此刻仍在 appMu 内——并发同票第二请求在锁外
	// 排队，进锁后撞 tombstone。
	e.discardConfirmTicket(plan)
	for _, id := range supersededIDs {
		if fresh, err := e.repos.Deployments.Get(ctx, id); err == nil {
			e.abandonPreDeployJobs(ctx, fresh)
		} else {
			e.log.Error("submit: supersede abandon lookup", "deployment", id, "err", err)
		}
	}
	// ADR-0082：受理成功 = 操作者显式让部署驱动器重建该 App（teardown-
	// abort 语义的唯一重建通道，ADR-0023）——清删除收口标记，交还收敛
	// 复查的重建权判定（appMu 在手，与 TeardownApp 的标记写入互斥）。
	e.converge.clearTeardown(req.AppID)
	e.loop.Kick()
	return out, adm, nil
}

// Cancel 取消排队或在途 Deployment（ADR-0016：排队与在途均可取消）。
// 在途取消是"停止推进"语义：当前 step 收尾后不再迁移。已下发的 Workload
// **由收敛回卷基线**（ADR-0082 §1 行 10：取消的部署从未成为期望态；非成功
// 终态在 transition 前门落重放标记，下一拍 Replay 快信号兑现），N0「不自动
// 拆除」条款在本场景自 ADR-0082 起废止。jobs 等待中的部署取消后，锚定
// job Task best-effort 强停（ADR-0030 决策 6：审计携带操作者）。
func (e *Engine) Cancel(ctx context.Context, id string) (*deployment.Deployment, error) {
	var out *deployment.Deployment
	err := e.tx.Do(ctx, func(s transact.Stores) error {
		d, err := s.Deployments.Get(ctx, id)
		if err != nil {
			return err
		}
		if d.State.Terminal() {
			return fmt.Errorf("%w: deployment %s is %s", ErrNotCancellable, id, d.State)
		}
		if err := e.transit(ctx, s, d,
			deployment.ActiveStates(), deployment.StateCancelled, nil); err != nil {
			return err
		}
		out, err = s.Deployments.Get(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	e.ensureForget(e.delivery.release, id) // 取消是 releasing 的另一出口：物化备忘随相位作废（C17）
	e.abandonPreDeployJobs(ctx, out)
	e.loop.Kick()
	return out, nil
}

// lastDeployedRevision 返回 App 当前基线（最近一次终态成功的 to_revision
// 与其 generation——双代窗的旧代锚，ADR-0048；首次部署为空/0——回滚无
// 对象，失败即终态，领域模型 §4）。查询失败如实上抛（C5：吞错误按"首次"
// 处理会在失败时缺回滚对象——错误面进不了事务，整单拒绝让调用方看到
// 存储故障）。
func (e *Engine) lastDeployedRevision(ctx context.Context, s transact.Stores, appID string) (string, uint64, error) {
	d, err := s.Deployments.LatestSucceeded(ctx, appID)
	if errors.Is(err, state.ErrNotFound) {
		return "", 0, nil // 首次部署：无基线（回滚无对象，失败即终态）
	}
	if err != nil {
		return "", 0, fmt.Errorf("resolve baseline: %w", err)
	}
	return d.ToRevision, d.Generation, nil
}

// transit（四件一的 Deployment 前门）与 transitAndReload 已收口至
// transition.go——序列与规则（原地迁移不发事件、AfterFP 修正）单点拥有。
// deployment 事件的发射面也随之前门化（eventStateName 字面量锚定在
// events.go，usage 反扫不受影响）。
