package placement

// 库实例放置入口（E4 数据库托管，managed-databases §2.1 复用清单「放置绑
// 定」行 + §2.3 操作表）：与 app 的 Resolve 同款三层概念模型，但绑定内嵌
// db_instances.platform_node_id（D-DB-1：不写 placements 表——该表以
// app_id 为主键），选点评分复用同一候选核（candidates() 直读全量节点快
// 照，D-MN-7）与三因子（multi-node §2.6/D-MN-7）：数据引力 > 已钉数少 >
// 平台 ID 字典序——数据引力读卷注册表的 database 归属行、已钉数 = app
// placements 计数 + db_instances 在役绑定计数（两类有状态绑定同账，节点
// 容量口径不失真）。
//
// 卷钉住语义照用（stateful-placement）：库模板恒带数据卷 → 绑定恒存在、
// 有卷强制钉住（constraint 编译同 ConstraintFor）；既有绑定保持——v0.2
// 无任何显式换库点机制（自动迁移不做，§7），绑定变更零路径 = 「仅显式
// 机制改变绑定」的构造性满足。绑定落库（SetDatabaseNodeBinding）由收敛
// 器（internal/database）执行——本入口是纯裁决（Resolve 语义，非 Apply）。

import (
	"context"
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DatabaseInput 是一次库实例放置解析输入。
type DatabaseInput struct {
	// InstanceID/InstanceName 是库实例平台 ID（卷注册表 database 归属寻址）
	// 与实例名（错误信息可读性）。
	InstanceID   string
	InstanceName string
	// ExistingBinding 是既有绑定锚（db_instances.platform_node_id；空 =
	// 未绑定——首次收敛选点）。
	ExistingBinding string
}

// DatabaseDecision 是库实例放置裁决书（卷恒存在 → Bind 恒真；PlatformNodeID
// 与 Constraint 是收敛器的执行输入）。
type DatabaseDecision struct {
	InstanceID string
	// KeptExisting 报告既有绑定被保持（与 app 侧「绑定优先」同语义）。
	KeptExisting bool
	// PlatformNodeID 是绑定锚（平台节点 ID n_<ULID>）。
	PlatformNodeID string
	// Constraint 是数据卷服务的钉住约束（node.labels.fleetly.node-id ==
	// <平台ID>；与 app 侧 ConstraintFor 同公式）。
	Constraint string
}

// ResolveDatabase 解析库实例放置（不落库）：既有绑定保持；未绑定走自动
// 选点（候选 = 已锚定 + ready + active；评分 = 数据引力〔卷注册表
// database 归属行所在节点〕> 已钉数少〔placements + db_instances 绑定〕>
// 平台 ID 字典序）。无候选 → E_PLACEMENT_NO_ELIGIBLE_NODE（与 app 侧同码
// 同型前哨）。
func (r *Resolver) ResolveDatabase(ctx context.Context, in DatabaseInput) (DatabaseDecision, error) {
	// 既有绑定保持（绑定优先——库实例生命周期内绑定不变，v0.2 无换点机制）。
	if in.ExistingBinding != "" {
		return DatabaseDecision{
			InstanceID:     in.InstanceID,
			KeptExisting:   true,
			PlatformNodeID: in.ExistingBinding,
			Constraint:     ConstraintFor(in.ExistingBinding),
		}, nil
	}

	cands, err := r.candidates(ctx)
	if err != nil {
		return DatabaseDecision{}, err
	}
	pool := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if c.platformID != "" && c.ready() {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return DatabaseDecision{}, apperr.New("E_PLACEMENT_NO_ELIGIBLE_NODE",
			"no candidate for database placement (anchored+ready+active set is empty; direct snapshot has %d node(s))", len(cands)).
			WithContext("candidates", describeCandidates(cands))
	}

	// 数据引力：库实例卷注册表（owner=database，权威 SQLite）所在节点优先。
	gravity := map[string]bool{}
	vols, err := r.store.ListOwnerVolumes(ctx, state.VolumeOwnerDatabase, in.InstanceID)
	if err != nil {
		return DatabaseDecision{}, fmt.Errorf("placement: read database volume registry: %w", err)
	}
	for _, v := range vols {
		if v.Status == state.VolumeActive && v.PlatformNodeID != "" {
			gravity[v.PlatformNodeID] = true
		}
	}
	// 已钉数：app placements 权威计数 + 库实例在役绑定计数（两类有状态
	// 绑定同一节点容量账）。
	pinnedCount, err := r.store.PlacementCountByNode(ctx)
	if err != nil {
		return DatabaseDecision{}, fmt.Errorf("placement: read pinned counts: %w", err)
	}
	dbBindings, err := r.store.DatabaseBindingCountByNode(ctx)
	if err != nil {
		return DatabaseDecision{}, fmt.Errorf("placement: read database binding counts: %w", err)
	}
	for node, n := range dbBindings {
		pinnedCount[node] += n
	}

	// 三因子排序（与 autoPick 同公式：稳定序、同分平台 ID 字典序定序）。
	sort.SliceStable(pool, func(i, j int) bool {
		a, b := pool[i], pool[j]
		if ga, gb := gravity[a.platformID], gravity[b.platformID]; ga != gb {
			return ga // 数据引力优先
		}
		if pinnedCount[a.platformID] != pinnedCount[b.platformID] {
			return pinnedCount[a.platformID] < pinnedCount[b.platformID] // 已钉数少优先
		}
		return a.platformID < b.platformID // 平台 ID 字典序
	})
	chosen := pool[0]
	return DatabaseDecision{
		InstanceID:     in.InstanceID,
		PlatformNodeID: chosen.platformID,
		Constraint:     ConstraintFor(chosen.platformID),
	}, nil
}
