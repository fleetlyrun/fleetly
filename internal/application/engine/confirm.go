package engine

// Confirm Ticket 的 engine 受理半边（B1，设计稿 2026-10-30 §1.4 甲'方案；
// ADR 编号随 W5 补录）：两段分工的内容绑定腿——拦截器（governance.
// ConfirmGuard）完成通用验票（存在/TTL/purpose/Verb/请求者）后，④
// request_digest 与本临界区冻结体 marshalSpec 规范形比对、⑤ base 多锚与
// 当前行比对、烧票全部下沉 Submit 的 appMu 受理临界区：
//
//	合并 → 合成 → 冻结（产出 rev）→ [appMu] ④digest 比对 → ⑤base 比对
//	（admission 事务 checks 内，与落行同快照）→ admission 落库 → [事务成
//	功后仍在 appMu] 烧票 + LedgerOnly 审计行。
//
// 甲'裁决的动机（独立审查轮三方收敛）：handler 侧验票迫使合成双跑（两
// 跑之间 webhook 落新冻结体即击穿「确认的正是展示的」）；「先验后烧」
// 两步原语有 TOCTOU 同票双执行——单临界区一次关闭两类洞。并发同票第二
// 请求在 appMu 外排队，进锁后 peek 撞 tombstone（已确认过）。
//
// 烧票契约（设计稿 §1.4）：discard 仅在 admission 相落库成功后——冻结
// 成功而 admission 拒（queue full）不烧、票保留，重试经冻结体内容寻址
// 复用（同 digest 命中既有 rev，④仍成立）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 哨兵错误（engine 零 apperr：api 层映射 errcode）。
var (
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

// confirmPlan 是一次 Submit 受理的确认消费计划（appMu 内 peek 后产物）：
// skip 与 ticket 互斥两形态，audit 携带 LedgerOnly 审计行所需的票面锚。
type confirmPlan struct {
	skip   bool
	ticket string
	peeked *governance.ConfirmTicketPayload
}

// ledgerFact 铸 LedgerOnly 审计行（kernel FactRow 双拍先例 deployspec.go
// revision.create：确认审计是取证面事实，无流面孪生——「确认过」「跳过」
// 「无凭证拒绝」三形态取证面可辨，设计稿 §1.2 审计 A8）。deploymentID
// 是受理落库的部署行（deduplicated 时为命中既有行）。
func (p *confirmPlan) ledgerFact(ctx context.Context, deploymentID string) writes.FactRow {
	fr := writes.FactRow{
		Aggregate: "deployment", ID: deploymentID,
		Action: "confirm.redeem", Resource: "deployment/" + deploymentID,
		Actor:  authn.ActorFromContext(ctx),
		Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行——确认兑换无流面事件（操作者动词的
		// 流面事实由 deployment.create 承载，此处只补「带票/跳过」取证）。
		LedgerOnly: true,
	}
	if p.skip {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return fr
	}
	if p.peeked == nil {
		return fr
	}
	b := p.peeked.Base
	fr.AfterFP = fmt.Sprintf("proof=redeemed; verb=%s; preview_digest=%s; request_digest=%s; base_frozen=%s; base_succeeded=%s; var_env=%s; hook_row=%s",
		p.peeked.Verb, p.peeked.PreviewDigest, p.peeked.RequestDigest,
		b.LatestFrozenDigest, b.LatestSucceededDigest, b.VarEnvDigest, b.HookRowVersion)
	return fr
}

// peekConfirmTicket 执行 appMu 内的票面复核（④ 前置半边）：拦截器验过
// 的票在锁内重 peek（peek 不删，锁外/锁内两次 peek 无害），辨因与目标
// 锚 → 哨兵。
func (e *Engine) peekConfirmTicket(input *ConfirmInput, appID string) (*confirmPlan, error) {
	if input == nil {
		return nil, nil // 非强制面路径（webhook/git 天然豁免——不经 Deploy 动词）
	}
	if input.Skip {
		return &confirmPlan{skip: true}, nil
	}
	if e.confirmTickets == nil {
		// 装配缺口防御（正常装配恒注入）：带票受理在无店面上继续会静默
		// 失去内容绑定——编程错误防线，非用户面校验。
		return nil, fmt.Errorf("engine: confirm ticket store is not wired (programming error in assembly)")
	}
	payload, status := e.confirmTickets.PeekConfirm(input.Ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, ErrConfirmStale
	case governance.ConfirmPeekConsumed:
		return nil, fmt.Errorf("%w: the ticket was already redeemed (single-use)", ErrConfirmMismatch)
	case governance.ConfirmPeekUnknown:
		return nil, fmt.Errorf("%w: the ticket is not a valid confirmation ticket", ErrConfirmMismatch)
	}
	// 目标锚：票绑目标 App（跨目标重放拒绝）。
	if payload.TargetApp != appID {
		return nil, fmt.Errorf("%w: ticket targets app %s, not %s", ErrConfirmMismatch, payload.TargetApp, appID)
	}
	// ④ request_digest 的比对在 admission 事务内、⑤ base 之后执行（辨因
	// 序：世界先核对——base 漂移即 STALE，变量/hook 行变化改写合成结果
	// 使 digest 必变的归腿由此不误判 MISMATCH，设计稿 §1.4 #7；世界一致
	// 而 digest 不符才是请求被改 → MISMATCH）。见 Submit 的 2.5 步。
	return &confirmPlan{ticket: input.Ticket, peeked: &payload}, nil
}

// checkConfirmBase 是 ⑤ base 多锚比对（admission 事务 checks 内执行——
// 与落行同快照，锁内所见即终局）：最新冻结体/LatestSucceeded/活跃变量
// 环境摘要/hook 行版本任一漂移即 STALE（服务端合成输入漂移归此腿，
// 不误判 MISMATCH，独立审查轮 #7）。
//
// selfDigest 是本请求冻结体的 digest：最新冻结体锚的漂移判定排除
// 「自己就是最新」的形态——带票 Deploy 自身的冻结（或 queue-full 重试的
// 内容寻址复用）使最新冻结体恰为本请求载荷，不是「别人改了世界」。
// 外来冻结体（webhook 在预览与确认之间落的新 rev）既非票面锚也非
// self → STALE（双跑竞态腿，甲'裁决的场景半边）。
func (e *Engine) checkConfirmBase(ctx context.Context, s transact.Stores, appID, projectID, selfDigest string, want governance.ConfirmBase) error {
	now, err := confirmBaseNow(ctx, s, appID, projectID)
	if err != nil {
		return err
	}
	frozenAnchorOK := now.LatestFrozenDigest == want.LatestFrozenDigest || now.LatestFrozenDigest == selfDigest
	if !frozenAnchorOK || now.LatestSucceededDigest != want.LatestSucceededDigest ||
		now.VarEnvDigest != want.VarEnvDigest || now.HookRowVersion != want.HookRowVersion {
		return fmt.Errorf("%w: baseline anchors moved since the preview (frozen/succeeded/var-env/hook)", ErrConfirmStale)
	}
	return nil
}

// confirmBaseNow 采集当前时刻的 base 多锚（与 admission 同事务快照）。
// 各锚缺失态（无冻结行/无成功部署/无变量/无 hook）以空串入摘要——铸票
// 面对空基线（首次部署）同样空串，比对闭合。
func confirmBaseNow(ctx context.Context, s transact.Stores, appID, projectID string) (governance.ConfirmBase, error) {
	var base governance.ConfirmBase
	if latest, err := s.Revisions.Latest(ctx, appID); err == nil {
		base.LatestFrozenDigest = latest.Digest
	} else if !errors.Is(err, state.ErrNotFound) {
		return base, err
	}
	if d, err := s.Deployments.LatestSucceeded(ctx, appID); err == nil {
		if rev, rerr := s.Revisions.Get(ctx, d.ToRevision); rerr == nil {
			base.LatestSucceededDigest = rev.Digest
		} else if !errors.Is(rerr, state.ErrNotFound) {
			return base, rerr
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		return base, err
	}
	// 变量环境摘要 = 合成后环境（Shared 底 + App 顶、App 胜出——与
	// SubmitSpec 三级合成同向；摘要函数单源 governance.ConfirmVarEnvDigest）。
	env := map[string]string{}
	if vars, err := s.SharedVariables.ListActive(ctx, projectID); err != nil {
		return base, err
	} else if len(vars) > 0 {
		for _, v := range vars {
			env[v.Name] = v.Value
		}
	}
	if vars, err := s.AppVariables.ListActive(ctx, appID); err != nil {
		return base, err
	} else if len(vars) > 0 {
		for _, v := range vars {
			env[v.Name] = v.Value
		}
	}
	base.VarEnvDigest = governance.ConfirmVarEnvDigest(env)
	if h, err := s.Hooks.Get(ctx, appID); err == nil {
		base.HookRowVersion = h.UpdatedAt // 行版本锚（UpdatedAt 即行内容换代戳）
	} else if !errors.Is(err, state.ErrNotFound) {
		return base, err
	}
	return base, nil
}

// discardConfirmTicket 烧票（admission 相落库成功后、仍在 appMu 内调用）。
// 烧失败（票窗内被并发消费等异常态）不回滚已落库的受理——记日志：
// 单临界区下不会发生（同 App 排队、跨 App 撞目标锚），此处是防御日志面。
func (e *Engine) discardConfirmTicket(plan *confirmPlan) {
	if plan == nil || plan.skip || plan.ticket == "" {
		return
	}
	if !e.confirmTickets.DiscardConfirm(plan.ticket) {
		e.log.Error("confirm: discard after admission failed (ticket not redeemable)", "ticket_len", len(plan.ticket))
	}
}

// revisionDigest 计算冻结体规范形的 digest（⑤最新冻结体锚的 self 半边）。
func revisionDigest(specBody []byte) string {
	if len(specBody) == 0 {
		return ""
	}
	return revision.Digest(specBody)
}

// confirmLedger 在 admission 的去重命中分支落 confirm 审计行（该分支不经
// commitWrite facts——deduplicated 也是受理成功，票面取证照落；无
// confirm 面零行为）。
func (e *Engine) confirmLedger(ctx context.Context, s transact.Stores, plan *confirmPlan, deploymentID string) error {
	if plan == nil {
		return nil
	}
	return writes.Sequence(ctx, e.factRepos(s), func() writes.FactRow {
		return plan.ledgerFact(ctx, deploymentID)
	})
}

// BindConfirmTickets 回注确认票据面（组合根闭合点：engine 先于 Services
// 构造的装配序下，Services 持有的进程内票据店在服务面构造后回注——两段
// 分工（拦截器验票 + engine 烧票）共享单店）。必须在服务器启动受理前
// 调用（此后只读）。
func (e *Engine) BindConfirmTickets(store governance.ConfirmTicketStore) {
	e.confirmTickets = store
}
