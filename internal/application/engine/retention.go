// retention 是 Revision 保留窗的引擎侧接线（ADR-0101）：冻结事务内圈定
// 活跃集并裁剪。清理语义单源住 persistence 的 Prune；本文件只持 App 侧
// 活跃集锚（Function 侧同律不同锚——current_revision_id 指针，ADR-0102
// 建表时接入同一 Prune）。
package engine

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// protectedRevisionIDs 圈定 App 侧保留窗内不可删的 revision 集（ADR-0101
// 活跃集）：驱动权行（活跃态 + failed 待自动回滚窗，ADR-0082 驱动权口径）
// 的 from/to 锚 ∪ 最近成功基线的 to 锚。蓝绿/双代窗（ADR-0048）旧代由
// 驱动权行的 from 锚覆盖；无成功基线（首次部署）= 空集并集，不视错。
func protectedRevisionIDs(ctx context.Context, s transact.Stores, appID string) ([]string, error) {
	driving, err := s.Deployments.DrivingByApp(ctx, appID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(driving)*2+1)
	add := func(id string) {
		if id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, d := range driving {
		add(d.FromRevision)
		add(d.ToRevision)
	}
	base, err := s.Deployments.LatestSucceeded(ctx, appID)
	switch {
	case err == nil:
		add(base.ToRevision)
	case errors.Is(err, state.ErrNotFound):
		// 首次部署：无成功基线，仅驱动权行锚受保护。
	default:
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}
