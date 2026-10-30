package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Rollback 是回滚的一等动词（F0.11，CONTEXT.md Replay 词条：重新应用某条
// 旧 Revision——回滚的唯一实现）：创建一条以目标 Revision 为终点的
// Deployment（显式抢占在途：回放不应排在失败尝试之后）。
//
// toRevisionID 为空时回放到上一成功基线（最近一次 succeeded 的
// to_revision）；无成功基线返回明确错误（首次部署无回滚对象）。
// 第二返回件是 Submit 的判定附注（P10）——回放也是创建型受理，响应面
// 与 Deploy 同形态。
//
// confirm 是 B1 单目标验票（设计稿 §1.2 重裁：票绑目标 id + 当前
// revision digest + 操作者）：非 nil 时经 Submit 的 appMu 临界区复用
// ④digest/⑤base 内容绑定与烧票（④锚 = 目标 Revision 冻结体；提交语义
// 位恒 supersede——Rollback 固定抢占在途）。nil = 内部/测试路径。
func (e *Engine) Rollback(ctx context.Context, appID, toRevisionID string, confirm *ConfirmInput) (*deployment.Deployment, *Admission, error) {
	if toRevisionID == "" {
		// 最新成功基线（ADR-0022）：LatestSucceeded = succeeded 行按 ULID
		// 创建序取最新——与既逐行扫描取首个 succeeded 的语义相同（列表分页
		// 化后不再全量扫，见 deployment.ListByApp 的 ADR-0026 形态）。
		base, err := e.repos.Deployments.LatestSucceeded(ctx, appID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, nil, fmt.Errorf("%w: app %s", ErrNoSuccessfulBaseline, appID)
			}
			return nil, nil, err
		}
		toRevisionID = base.ToRevision
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
