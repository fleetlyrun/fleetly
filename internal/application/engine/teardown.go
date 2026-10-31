package engine

// ADR-0023 App 删除语义的 engine 面：TeardownApp 收口（Runtime.Remove 拆
// 域内载体 + 缓存清理）。副作用与审计/tombstone 不可同事务——API 层按
// "先收口后落账"编排，失败即整体失败（App 保持可操作，可重试删除）。

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
)

// TeardownApp 拆除 App 的全部 Runtime 载体（幂等；Remove 对不存在对象不
// 报错）并清归属/期望/drift/观测缓存。孤儿原则维持：Remove 只拆 fleetly
// 标记域，非管辖载体只登记不自动删（CONTEXT.md Orphan）。与 admission/
// 基线重放共享 App 级互斥（N0.1 P1-3）：收口期间受理与重放排队。
//
// confirm 是 B1 拆除确认票（设计稿 §1.2 delete 族 + §1.4 teardown 票
// 绑定）：非 nil 时**验票、写（拆载体）、烧同一把 appMu 内**——peek 复核
// （目标锚 + 全锚〔app 行版本 + hook 行 digest + route 行 ID 集摘要〕+
// ④级联清单 digest 重算）先行，拆载体成功后烧票。nil/skip = 豁免腿
// （--yes 或内部路径）。
func (e *Engine) TeardownApp(ctx context.Context, appID string, confirm *ConfirmInput) error {
	appMu := e.lockApp(appID)
	appMu.Lock()
	defer appMu.Unlock()

	// B1 票检（appMu 内、拆载体等一切副作用之前）：peek 不删——烧票在
	// 拆载体成功后（写失败票保留，重试删除不必重预览）。
	plan, cerr := e.peekTeardownConfirm(ctx, appID, confirm)
	if cerr != nil {
		return cerr
	}

	// 锁内预检（ADR-0023 修订）：拆载体前复查**驱动权**（R02-M4）。
	// 谓词与 converger 同口径（DrivingByApp = 活跃态 + failed 待自动回滚
	// 窗）——ActiveByApp 不含 failed 待回滚行，单独使用会放行"驱动器马上
	// 要回滚重放"的 App，让回卷在收口后重建载体（无主孤儿）。与 Submit
	// 共享 appMu，此处所见即受理终局——API 无锁预检到收口之间受理的部署
	// 在一切副作用之前拒绝（否则拆掉的载体要靠在途部署重放自愈，白承受
	// 一次可用性抖动）。
	driving, err := e.repos.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		return err
	}
	if len(driving) > 0 {
		return fmt.Errorf("%w: app %s has %d driving deployment(s)", ErrActiveDeployment, appID, len(driving))
	}

	a, err := e.repos.Apps.Get(ctx, appID)
	if err != nil {
		return err
	}
	team, _, err := e.appTeam(ctx, appID)
	if err != nil {
		return fmt.Errorf("resolve app: %w", err)
	}
	ns := capability.NamespaceRef{Team: team, Project: a.ProjectID, App: a.ID}
	// 维护互斥读半边（R13-L1）：Remove 是载体写动词，与 Ensure 族及网络
	// 重建（写半边 detach→rm→create→attach 全序）串行化——重建窗内不得
	// 插入拆载体（半拆网 × 半拆 App 交错会把 re-attach 面对已逝归属）。
	// 锁等待不占步预算（排队语义：读锁在带界 ctx 派生之前获取）。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（B15-1，批 3 判定的反转）：API 请求路径的 Remove 挂死会
	// 卡住 API 调用本身（与收敛环卡死同害——docker hang 时 DeleteApp 的
	// gRPC 永不返回，且 appMu 被握死堵住同 App 全部受理/重放）。带
	// ManagedStepTimeout 硬上限，超时如实上抛（App 保持可操作，可重试删除）。
	rctx, rcancel := e.boundedStep(ctx)
	removeErr := e.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("runtime remove: %w", removeErr)
	}

	// 缓存收口：先收集该 App 名下的 Workload 集，再逐面清（drift/稳态
	// 签名只按 workloadID 键，跨 App 不得误删）。
	wids := e.obs.forgetOwner(appOwner(appID))
	e.expect.mu.Lock()
	delete(e.expect.expected, appOwner(appID))
	e.expect.mu.Unlock()
	e.drift.mu.Lock()
	for _, wid := range wids {
		delete(e.drift.sig, wid)
	}
	e.drift.mu.Unlock()
	e.drift.stoppedMu.Lock()
	for _, wid := range wids {
		delete(e.drift.stoppedSig, wid)
	}
	e.drift.stoppedMu.Unlock()
	// 收敛域记账全清（R16-L2 + R02-L7）：memo/resolved/fails/failSig/
	// posture/cooldown/eventSeen/projMemo/replay 全部随 App 作废——已删 App
	// 的冷却记账会让内置 converge-failed 永久 firing、doctor 永久 warn，
	// 且这些表对已删 App 泄漏。随后重钉 teardown 标记（ADR-0023/ADR-0082：
	// expect 缓存缺席无法区分「重启冷窗」与「删除收口后」，标记才是竞态
	// 闭合面，且必须在 forgetOwner 之后、同一 appMu 临界区内落位；Submit
	// 受理成功清标记）。
	alertResolved := e.converge.forgetApp(appID)
	e.converge.markTeardown(appID)
	// 最后一个冷却 App 被删 = 内置告警解除沿（与冷却释放同款沿处理——
	// 告警状态与记账单源，不靠重启清场）。
	if alertResolved {
		e.convergeAlertEdge(ctx, alertrule.StateOK, e.clock.Now())
	}
	// B1 烧票：拆载体（写动作）成功后、仍在 appMu 内（验票、写、烧同一
	// 把锁）。审计行随 API 层 tombstone 事务落（app.delete 的伴生
	// LedgerOnly 行——tombstone 落账失败时票已烧属重预览可接受面，写
	// 副作用不回滚是既有编排语义）。
	e.discardConfirmTicket(plan)
	return nil
}

// peekTeardownConfirm 是拆除票的锁内复核：peek 辨因（哨兵同 Submit 半边）
// + 目标 App 锚 + 全锚比对（app 行版本 + hook 行版本 + route 行 ID 集摘要
// ——设计稿 §1.4 teardown 票绑定；W2a 补齐 app/route 两锚：预览后新增的
// route/hook 若不锚，确认时会被清单从未展示地删除）+ ④级联清单 digest
// 重算（TeardownCascade 铸验共享单源——载体集漂移腿）。锚在票面为空 =
// 铸票时该对象缺席，比对仍无条件全等（空==空闭合，空↔非空即 STALE——
// checkConfirmBase 空串全等同形态，修复批 F2）。漂移 = 预览后有行/清单
// 变化 → STALE。
func (e *Engine) peekTeardownConfirm(ctx context.Context, appID string, confirm *ConfirmInput) (*confirmPlan, error) {
	if confirm == nil || confirm.Skip {
		if confirm != nil {
			return &confirmPlan{skip: true}, nil
		}
		return nil, nil
	}
	if e.confirmTickets == nil {
		return nil, fmt.Errorf("engine: confirm ticket store is not wired (programming error in assembly)")
	}
	payload, status := e.confirmTickets.PeekConfirm(confirm.Ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, ErrConfirmStale
	case governance.ConfirmPeekConsumed:
		return nil, fmt.Errorf("%w: the ticket was already redeemed (single-use)", ErrConfirmMismatch)
	case governance.ConfirmPeekUnknown:
		return nil, fmt.Errorf("%w: the ticket is not a valid confirmation ticket", ErrConfirmMismatch)
	}
	if payload.TargetApp != appID {
		return nil, fmt.Errorf("%w: ticket targets app %s, not %s", ErrConfirmMismatch, payload.TargetApp, appID)
	}
	// 全锚比对（与铸票面 TeardownAnchors 同源采集）。无条件全等——空==空
	// 自然闭合，空↔非空即 STALE（与 Deploy 侧 checkConfirmBase 的空串全等
	// 形态对齐，修复批 F2：`!= ""` 卫会让铸时缺席的锚被整体跳过，预览后
	// 新增的 hook/route 行被清单从未展示地删除）。
	now, aerr := e.TeardownAnchors(ctx, appID)
	if aerr != nil {
		return nil, aerr
	}
	if now.HookRowVersion != payload.Base.HookRowVersion {
		return nil, fmt.Errorf("%w: hook row changed since the teardown preview", ErrConfirmStale)
	}
	if now.AppRowVersion != payload.Base.AppRowVersion {
		return nil, fmt.Errorf("%w: app row changed since the teardown preview", ErrConfirmStale)
	}
	if now.RouteSetDigest != payload.Base.RouteSetDigest {
		return nil, fmt.Errorf("%w: route rows changed since the teardown preview", ErrConfirmStale)
	}
	// ④ RequestDigest 消费面重算（修复批 F2，只铸不验的洞）：按铸票同参
	// （TeardownCascade/TeardownCascadeDigest 铸验共享单源）重算当前级联
	// 清单指纹与票面比对，不符即 STALE。载体集漂移腿由此闭合——预览后
	// 一次部署成功改变载体集时 app 行版本不动、DrivingByApp 只拦活跃行，
	// 三锚全过而清单已换。
	cascade, cerr := e.TeardownCascade(ctx, appID)
	if cerr != nil {
		return nil, cerr
	}
	if TeardownCascadeDigest(cascade) != payload.RequestDigest {
		return nil, fmt.Errorf("%w: the teardown cascade manifest changed since the preview", ErrConfirmStale)
	}
	return &confirmPlan{ticket: confirm.Ticket, peeked: &payload}, nil
}
