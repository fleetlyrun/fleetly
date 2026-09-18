package placement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 部署前哨（stateful-placement §2.5 deploy_preflight）：新部署快速失败、
// 不排队——
//
//	绑定节点非 ready → E_PLACEMENT_NODE_UNAVAILABLE（快速失败）
//	绑定节点已移除（底座快照缺失）→ E_PLACEMENT_NODE_GONE
//	卷数据节点 ≠ 目标节点 → E_VOLUME_NODE_MISMATCH（数据安全前哨，把空卷
//	  事故变成 409；单机下目标恒等于绑定，跨节点路径保留随 v0.2）
//
// 直读纪律：节点状态取自底座直读快照（决策路径禁读观测缓存）。

// Preflight 执行部署前哨检查：绑定存在且节点不可用即错误。无绑定（无卷
// 应用自由调度）通过；blocked/unresolved 记录直接不可用（app blocked 的
// 派生来源，state-model §2.10）。
func (r *Resolver) Preflight(ctx context.Context, appID string) error {
	p, err := r.store.GetPlacement(ctx, appID)
	switch {
	case errors.Is(err, state.ErrPlacementNotFound):
		return nil // 无绑定无前哨（自由调度）
	case err != nil:
		return fmt.Errorf("placement: read placement: %w", err)
	}
	if p.State == state.PlacementBlocked || p.State == state.PlacementUnresolved {
		return apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"应用绑定当前不可用（state=%s reason=%s）：新部署快速失败，不排队", p.State, p.Reason).
			WithContext("node", p.PlatformNodeID).
			WithContext("state", string(p.State))
	}
	if err := r.VerifyVolumesOnNode(ctx, appID, p.PlatformNodeID); err != nil {
		return err
	}

	// 直读底座：绑定节点 ready 性（platform ID → runtime ref → 底座快照）。
	ref, err := r.store.GetRuntimeNodeRef(ctx, p.PlatformNodeID)
	if errors.Is(err, state.ErrRefNotFound) {
		// 映射缺失 = 身份锚未完成/节点重建中：无法验证即视为不可用
		//（保守：不猜测，快速失败）。
		return apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"绑定节点 %s 缺少底座映射（身份锚未完成或节点重建中）", p.PlatformNodeID).
			WithContext("node", p.PlatformNodeID)
	} else if err != nil {
		return fmt.Errorf("placement: read runtime node ref: %w", err)
	}
	n, err := r.nodeBySwarmID(ctx, ref.SwarmNodeID)
	switch {
	case errors.Is(err, errNodeMissing):
		return apperr.New("E_PLACEMENT_NODE_GONE",
			"绑定节点 %s（%s）已不在底座节点快照中：人工恢复数据后 rebind，或确认丢弃",
			p.PlatformNodeID, ref.SwarmNodeID).
			WithContext("node", p.PlatformNodeID)
	case err != nil:
		return err
	}
	if !ready(n) {
		return apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"绑定节点 %s 非 ready（state=%s availability=%s）：等待恢复或确认数据后 rebind",
			n.Hostname, n.State, n.Availability).
			WithContext("node", p.PlatformNodeID).
			WithContext("hostname", n.Hostname)
	}
	return nil
}

// VerifyVolumesOnNode 是卷数据前哨：任一 active 卷的数据节点 ≠ 目标节点即
// E_VOLUME_NODE_MISMATCH（§2.5：约束满足 ≠ 数据在位）。单机部署目标恒为
// 绑定节点 = 本机，短路成立但代码路径完整保留（v0.2 跨节点复用）。
// 无节点归属记录的卷（历史行）跳过——不猜测，登记面已可见。
func (r *Resolver) VerifyVolumesOnNode(ctx context.Context, appID, targetPlatformNodeID string) error {
	volumes, err := r.store.ListAppVolumes(ctx, appID)
	if err != nil {
		return fmt.Errorf("placement: list volumes: %w", err)
	}
	var mismatched []string
	for _, v := range volumes {
		if v.Status != state.VolumeActive || v.PlatformNodeID == "" {
			continue
		}
		if v.PlatformNodeID != targetPlatformNodeID {
			mismatched = append(mismatched, v.Name+" (data@"+v.PlatformNodeID+")")
		}
	}
	if len(mismatched) == 0 {
		return nil
	}
	sort.Strings(mismatched)
	return apperr.New("E_VOLUME_NODE_MISMATCH",
		"卷数据节点 ≠ 部署目标节点 %s：%s——声明数据处置（data-restored/discard）后重试",
		targetPlatformNodeID, strings.Join(mismatched, ", ")).
		WithContext("node", targetPlatformNodeID).
		WithContext("volumes", strings.Join(mismatched, ","))
}

// errNodeMissing 是直读快照中缺节点的内部哨兵。
var errNodeMissing = errors.New("node missing from direct snapshot")

// nodeBySwarmID 直读底座全量节点快照并按 swarm node ID 定位。
func (r *Resolver) nodeBySwarmID(ctx context.Context, swarmNodeID string) (state.SubstrateNode, error) {
	nodes, err := r.docker.ListNodeObservations(ctx)
	if err != nil {
		return state.SubstrateNode{}, fmt.Errorf("placement: list nodes: %w", err)
	}
	for _, n := range nodes {
		if n.SwarmNodeID == swarmNodeID {
			return n, nil
		}
	}
	return state.SubstrateNode{}, errNodeMissing
}

// ready 是底座 ready 语义：state=ready 且 availability=active（drain/pause
// 均不可部署——§2.6 drain 同 DOWN）。
func ready(n state.SubstrateNode) bool {
	return n.State == "ready" && n.Availability == "active"
}
