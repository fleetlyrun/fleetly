package engine

import (
	"context"
	"fmt"

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
		list, err := e.deployments.ListByApp(ctx, e.db.Runner(), appID)
		if err != nil {
			return nil, err
		}
		// ListByApp 新→旧：首个 succeeded 即最新成功基线。
		for i := range list {
			if list[i].State == deployment.StateSucceeded {
				toRevisionID = list[i].ToRevision
				break
			}
		}
		if toRevisionID == "" {
			return nil, fmt.Errorf("engine: app %s has no successful baseline to roll back to", appID)
		}
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
