package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
)

// Rollback 是回滚的一等动词（F0.11，CONTEXT.md Replay 词条：重新应用某条
// 旧 Revision——回滚的唯一实现）：创建一条以目标 Revision 为终点的
// Deployment（显式抢占在途：回放不应排在失败尝试之后）。
//
// toRevisionID 为空时回放到上一成功基线（最近一次 succeeded 的
// to_revision）；无成功基线返回明确错误（首次部署无回滚对象）。
func (e *Engine) Rollback(ctx context.Context, appID, toRevisionID string) (*deployment.Deployment, error) {
	if toRevisionID == "" {
		// 最新成功基线（ADR-0022）：LatestSucceeded = succeeded 行按 ULID
		// 创建序取最新——与既逐行扫描取首个 succeeded 的语义相同（列表分页
		// 化后不再全量扫，见 deployment.ListByApp 的 ADR-0026 形态）。
		base, err := e.deployments.LatestSucceeded(ctx, e.db.Runner(), appID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, fmt.Errorf("%w: app %s", ErrNoSuccessfulBaseline, appID)
			}
			return nil, err
		}
		toRevisionID = base.ToRevision
	}
	rev, err := e.revisions.Get(ctx, e.db.Runner(), toRevisionID)
	if err != nil {
		return nil, fmt.Errorf("engine: rollback target revision: %w", err)
	}
	if rev.AppID != appID {
		return nil, fmt.Errorf("engine: revision %s belongs to app %s, not %s", toRevisionID, rev.AppID, appID)
	}
	return e.Submit(ctx, SubmitRequest{
		AppID: appID, RevisionID: toRevisionID, Supersede: true, Kind: KindRollback,
	})
}

// SubmitRequest.Kind 取值（审计与事件 payload 的来源标注）。
const (
	// KindRollback 标注回放部署（`fleetly rollback` 一等动词）。
	KindRollback = "rollback"
)
