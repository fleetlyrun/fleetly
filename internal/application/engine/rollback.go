package engine

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
)

// Rollback 是回滚的一等动词（F0.11，CONTEXT.md Replay 词条：重新应用某条
// 旧 Revision——回滚的唯一实现）：创建一条以目标 Revision 为终点的
// Deployment（显式抢占在途：回放不应排在失败尝试之后）。
//
// toRevisionID 为空时回放到上一成功基线——跳过当前生效修订（缺省目标
// 解析的内联实现见函数体内"缺省目标解析"注释块；语义修复于 staging
// 2026-11-01 走查后）：仅一条成功基线且即当前生效、或无任何成功基线
// （首次部署）时返回明确错误（ErrNoSuccessfulBaseline）。
// 第二返回件是 Submit 的判定附注（P10）——回放也是创建型受理，响应面
// 与 Deploy 同形态。
//
// confirm 是 B1 单目标验票（设计稿 §1.2 重裁：票绑目标 id + 当前
// revision digest + 操作者）：非 nil 时经 Submit 的 appMu 临界区复用
// ④digest/⑤base 内容绑定与烧票（④锚 = 目标 Revision 冻结体；提交语义
// 位恒 supersede——Rollback 固定抢占在途）。nil = 内部/测试路径。
func (e *Engine) Rollback(ctx context.Context, appID, toRevisionID string, confirm *ConfirmInput) (*deployment.Deployment, *Admission, error) {
	if toRevisionID == "" {
		// 缺省目标解析（staging 2026-11-01 缺省复读自身缺陷的修复裁决；
		// 内联不另立 FuncDecl——真核熔断线冻结 579，ADR-0088 决策 5）：
		//
		//   - 最新一条部署行（任意状态，序 = 部署行 seq）是 succeeded →
		//     跳过它，取更早的最近 succeeded（成功后反悔回滚：当前生效
		//     修订不是回滚对象，复读自身等价重部署当前生效体，载体与
		//     路由面都不会回到旧基线）。
		//   - 最新行非 succeeded（失败后回滚等）→ 不跳，取最近 succeeded
		//     ——该场景下最近 succeeded 即当前基线，重放它属修复性回卷
		//     （今日行为保持，不得破坏）。
		//   - 仅一条成功基线且即当前生效 → 诚实报错（唯一成功基线即当前
		//     修订，无可回滚对象）；无任何成功基线（含首次部署）→ 沿用
		//     既有文案。两者同哨兵 ErrNoSuccessfulBaseline、API 层同映射
		//     E_NO_BASELINE（Q-13：判定走 errors.Is，零新码）。
		//
		// 实现复用 ListByApp 分页读径（ADR-0026 after_* 游标，新→旧）
		// 逐页扫描，不为单点查询扩 repo 面；回滚是稀有管理动词，分页
		// 开销无度量面。
		const pageLimit = 200 // 与 deployment repo ListByApp 的钳制上限同值
		var after int64
		first, skippedCurrent, resolved := true, false, false
		for !resolved {
			rows, lerr := e.repos.Deployments.ListByApp(ctx, appID, after, pageLimit)
			if lerr != nil {
				return nil, nil, lerr
			}
			if len(rows) == 0 {
				break
			}
			start := 0
			if first {
				first = false
				// 首页首行 = 最新一条部署行（任意状态）：succeeded 即
				// 当前生效修订，跳过。
				if rows[0].State == deployment.StateSucceeded {
					start, skippedCurrent = 1, true
				}
			}
			for _, row := range rows[start:] {
				if row.State == deployment.StateSucceeded {
					toRevisionID, resolved = row.ToRevision, true
					break
				}
			}
			after = rows[len(rows)-1].Seq
		}
		if !resolved {
			if skippedCurrent {
				return nil, nil, fmt.Errorf("%w: app %s: the only successful baseline is the current revision, nothing to roll back to",
					ErrNoSuccessfulBaseline, appID)
			}
			return nil, nil, fmt.Errorf("%w: app %s", ErrNoSuccessfulBaseline, appID)
		}
	}
	rev, err := e.repos.Revisions.Get(ctx, toRevisionID)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: rollback target revision: %w", err)
	}
	if rev.AppID != appID {
		return nil, nil, fmt.Errorf("engine: revision %s belongs to app %s, not %s", toRevisionID, rev.AppID, appID)
	}
	// 语义位单源归一：Rollback 固定抢占在途（Supersede=true 是 Submit 的
	// 本请求事实，非客户端声明）——④指纹的语义半边由此钉死，铸票面
	// （W2a 预览 Rollback）与消费面同参数。
	if confirm != nil && !confirm.Skip {
		sem := confirm.Semantics
		sem.Supersede = true
		confirm = &ConfirmInput{Ticket: confirm.Ticket, Semantics: sem}
	}
	return e.Submit(ctx, SubmitRequest{
		AppID: appID, RevisionID: toRevisionID, Supersede: true, Kind: KindRollback,
		Confirm: confirm, ConfirmSpecBody: rev.Spec,
	})
}

// SubmitRequest.Kind 取值与执行时来源标注（审计与事件 payload 形态：
// AfterFP 的 "; kind=<kind>" 后缀——行上无 Kind 列，kind 只活在审计流）。
const (
	// KindRollback 标注回放部署（`fleetly rollback` 一等动词；受理时已知
	// ——preDeploy 游标随建行直落 done）。
	KindRollback = "rollback"
	// KindRestart 标注声明式重启部署（ADR-0083 步骤 6/7：执行时 seq-only
	// 判定命中后由驱动器就地补标——受理时不知道，判定延迟到对实际最新
	// succeeded 基线重算）。
	KindRestart = "restart"
)
