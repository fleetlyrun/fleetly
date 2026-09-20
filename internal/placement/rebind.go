package placement

// 显式换点与迁移 runbook（multi-node §2.6/§2.8、D-MN-10，E1-7）：
//
//	Rebind（UpdatePlacement 的核心裁决）：目标节点存在且 ready（直读）；
//	有卷应用 data_ack 必填（restored|discarded——缺省 → E_VOLUME_NODE_
//	MISMATCH，前哨语义前置到换点面）；discarded 需 confirm（→
//	E_PLACEMENT_MOVE_REQUIRES_ACK）；落库 = 绑定换绑（bound）+ 卷行换绑
//	（prev_platform_node_id 登记源节点）+ placement.changed 事件 + 审计
//	（同事务，fail-closed）。换点不自动部署——后续由用户发起部署收敛。
//
//	MigrationPlan（GetPlacementMigrationPlan 的数据面）：restic 迁移
//	runbook 的服务端生成（平台半）——真实卷名/节点名填充；平台不编排
//	远端数据移动（restic 步骤由用户在两节点执行，D-MN-10）。
//
// 错误码全部复用注册表既有码（E_PLACEMENT_* 八码 + E_VOLUME_NODE_MISMATCH
// ——multi-node §5.2「词面已多节点就绪」），零新码。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// DataAck 词表（UpdatePlacement.data_ack 的取值域；proto 侧以 buf.validate
// 同表约束）。
const (
	// DataAckRestored 声明数据已按迁移 runbook 恢复到目标节点。
	DataAckRestored = "restored"
	// DataAckDiscarded 声明放弃源节点数据（admin + confirm；源节点副本
	// 成为残留，由 ListVolumes 的 residual 标记指引清理）。
	DataAckDiscarded = "discarded"
)

// RebindInput 是一次显式换点输入。
type RebindInput struct {
	// AppID 是应用平台 ID。
	AppID string
	// Node 是目标节点（唯一显示名或平台 ID；底座节点 ID 宽松接受）。
	Node string
	// DataAck 是数据处置声明（""|restored|discarded；有卷应用必填）。
	DataAck string
	// Confirm 是破坏性确认（discarded 必填）。
	Confirm bool
	// Actor 是审计操作主体（api 面传入 human/ai_agent + token 语义）。
	Actor string
}

// RebindResult 是换点裁决结果（绑定与卷行的落库后投影）。
type RebindResult struct {
	Placement state.Placement
	Volumes   []state.Volume
	// FromNode 是换点前的绑定锚（空 = 无既有绑定）。
	FromNode string
	// Discarded 报告本次以 discarded 处置数据（residual 清理指引生效）。
	Discarded bool
}

// Rebind 执行显式换点（校验 → 同事务落库 → 事件 + 审计）。目标校验失败
// 不产生任何写入（fail-closed 的校验段与落库段分离）。
func (r *Resolver) Rebind(ctx context.Context, in RebindInput) (RebindResult, error) {
	cands, err := r.candidates(ctx)
	if err != nil {
		return RebindResult{}, err
	}
	target, err := r.resolveRef(ctx, cands, in.Node)
	if err != nil {
		return RebindResult{}, err
	}
	if target.platformID == "" {
		return RebindResult{}, apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"target node %s has no platform identity (fleetly.node-id label missing; identity anchoring incomplete)",
			target.describe()).
			WithContext("node", target.hostname)
	}
	if !target.ready() {
		return RebindResult{}, apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"target node %s is not ready (state=%s availability=%s): rebinding requires a ready target",
			target.describe(), target.state, target.availability).
			WithContext("node", target.platformID)
	}

	// 数据处置校验（有卷应用 data_ack 必填——放置专项 §2.2 换点语义的
	// 前哨前置；放置专项 §2「数据安全处置必须是用户显式声明」D-PLC-5）。
	vols, err := r.store.ListAppVolumes(ctx, in.AppID)
	if err != nil {
		return RebindResult{}, fmt.Errorf("placement: read volume registry: %w", err)
	}
	hasVolumes := false
	for _, v := range vols {
		if v.Status == state.VolumeActive {
			hasVolumes = true
			break
		}
	}
	if hasVolumes && in.DataAck == "" {
		return RebindResult{}, apperr.New("E_VOLUME_NODE_MISMATCH",
			"app has active volumes: a data disposition is required for a cross-node rebind (data-restored after the restore flow, or discard with --confirm-destructive)").
			WithContext("node", target.platformID)
	}
	if in.DataAck != "" && in.DataAck != DataAckRestored && in.DataAck != DataAckDiscarded {
		// 防御分支（proto 校验已限制取值域；直连 gRPC 的纵深）。
		return RebindResult{}, apperr.New("E_PLACEMENT_MOVE_REQUIRES_ACK",
			"data_ack %q is not in {restored, discarded}", in.DataAck).
			WithContext("node", target.platformID)
	}
	if in.DataAck == DataAckDiscarded && !in.Confirm {
		return RebindResult{}, apperr.New("E_PLACEMENT_MOVE_REQUIRES_ACK",
			"discarding volume data on the source node is destructive: pass confirm (CLI: --confirm-destructive) after explicit review").
			WithContext("node", target.platformID)
	}

	// 落库：绑定换绑 + 卷行换绑 + placement.changed（+ volume.discarded）
	// 事件 + 审计同一事务（multi-node §2.6「同事务，fail-closed」）。
	prev, err := r.store.GetPlacement(ctx, in.AppID)
	hasPrev := err == nil
	switch {
	case err == nil:
	case errors.Is(err, state.ErrPlacementNotFound):
	default:
		return RebindResult{}, fmt.Errorf("placement: read placement: %w", err)
	}
	fromNode := ""
	if hasPrev {
		fromNode = prev.PlatformNodeID
	}

	var bound state.Placement
	err = r.store.InTx(ctx, func(tx *state.Tx) error {
		source := state.PlacementSourcePlatform
		labelRef := ""
		if hasPrev {
			source = prev.Source
			labelRef = prev.LabelRef
		}
		p, err := tx.BindPlacement(ctx, state.PlacementWrite{
			AppID:          in.AppID,
			PlatformNodeID: target.platformID,
			Source:         source,
			LabelRef:       labelRef,
			State:          state.PlacementBound,
		})
		if err != nil {
			return err
		}
		bound = p
		if hasVolumes {
			if _, err := tx.RebindAppVolumes(ctx, in.AppID, target.platformID); err != nil {
				return err
			}
		}
		if _, err := tx.AppendEvent(ctx, state.Event{
			Name:    "placement.changed",
			Subject: "app:" + in.AppID,
			Payload: `{"app_id":"` + in.AppID + `","from":"` + fromNode +
				`","to":"` + target.platformID + `","data_ack":"` + in.DataAck + `"}`,
		}); err != nil {
			return err
		}
		if in.DataAck == DataAckDiscarded && hasVolumes {
			if _, err := tx.AppendEvent(ctx, state.Event{
				Name:    "volume.discarded",
				Subject: "app:" + in.AppID,
				Payload: `{"app_id":"` + in.AppID + `","from":"` + fromNode +
					`","note":"source-node volume copies become residual cleanup targets"}`,
			}); err != nil {
				return err
			}
		}
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       actorOf(in.Actor),
			Action:      "placement.rebind",
			Target:      "app:" + in.AppID,
			Result:      "ok",
			DiffSummary: state.DiffSummary("from", fromNode, "to", target.platformID, "data_ack", in.DataAck), // MG-6 构造器
		})
	})
	if err != nil {
		return RebindResult{}, fmt.Errorf("placement: persist rebind: %w", err)
	}

	updated, err := r.store.ListAppVolumes(ctx, in.AppID)
	if err != nil {
		return RebindResult{}, fmt.Errorf("placement: read volume registry: %w", err)
	}
	return RebindResult{
		Placement: bound,
		Volumes:   updated,
		FromNode:  fromNode,
		Discarded: in.DataAck == DataAckDiscarded && hasVolumes,
	}, nil
}

// actorOf 回落审计操作主体（audit_log 的 actor 非空 CHECK；api 面显式传
// 入，直连调用缺省按 human 记账）。
func actorOf(actor string) string {
	if actor == "" {
		return "human"
	}
	return actor
}

// MigrationStep 是迁移 runbook 的一步（title = 短语；detail = 可复制命令/
// 说明文本，英文文案纪律）。
type MigrationStep struct {
	Title  string
	Detail string
}

// MigrationPlan 是 restic 迁移 runbook（服务端生成的步骤文档，D-MN-10
// 平台半：真实卷名/节点名填充；平台不编排远端数据移动）。
type MigrationPlan struct {
	AppID   string
	AppName string
	// FromNode/ToNode 是源与目标节点的人读形态（hostname (platformID)）。
	FromNode string
	ToNode   string
	// Volumes 是涉及的 active 卷（key 字典序）。
	Volumes []state.Volume
	// Steps 是顺序步骤（含命令与提示）。
	Steps []MigrationStep
	// Warnings 是计划级警示（如目标节点当前非 ready）。
	Warnings []string
}

// MigrationPlan 生成 <app> 换点至 <to> 的 restic 迁移 runbook（只读，不
// 触发任何状态变更；换点本身走 Rebind——runbook 第 4 步）。
func (r *Resolver) MigrationPlan(ctx context.Context, appID, appName, to string) (MigrationPlan, error) {
	cands, err := r.candidates(ctx)
	if err != nil {
		return MigrationPlan{}, err
	}
	target, err := r.resolveRef(ctx, cands, to)
	if err != nil {
		return MigrationPlan{}, err
	}
	if target.platformID == "" {
		return MigrationPlan{}, apperr.New("E_PLACEMENT_NODE_UNAVAILABLE",
			"target node %s has no platform identity (fleetly.node-id label missing)", target.describe()).
			WithContext("node", target.hostname)
	}

	plan := MigrationPlan{
		AppID:    appID,
		AppName:  appName,
		ToNode:   target.describe(),
		Volumes:  []state.Volume{},
		Steps:    []MigrationStep{},
		Warnings: []string{},
	}
	if prev, err := r.store.GetPlacement(ctx, appID); err == nil {
		for _, c := range cands {
			if c.platformID == prev.PlatformNodeID {
				plan.FromNode = c.describe()
				break
			}
		}
		if plan.FromNode == "" {
			plan.FromNode = prev.PlatformNodeID + " (hostname unknown: node not in direct snapshot)"
		}
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return MigrationPlan{}, fmt.Errorf("placement: read placement: %w", err)
	}

	vols, err := r.store.ListAppVolumes(ctx, appID)
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("placement: read volume registry: %w", err)
	}
	for _, v := range vols {
		if v.Status == state.VolumeActive {
			plan.Volumes = append(plan.Volumes, v)
		}
	}

	if !target.ready() {
		plan.Warnings = append(plan.Warnings, "Target node "+target.describe()+
			" is currently not ready (state="+target.state+" availability="+target.availability+
			"): finish the migration only after it recovers, or pick another target.")
	}

	// runbook 步骤（multi-node §2.8；restic repo 与镜像版本是用户参数——
	// 平台不编排远端数据移动，D-MN-10）。
	if len(plan.Volumes) > 0 {
		plan.Steps = append(plan.Steps, MigrationStep{
			Title: "Stop writes (maintenance window)",
			Detail: "On the manager: docker service scale fleetly-" + appName +
				"-<service>=0 for each service that mounts these volumes. Drift detection will honestly alert during the window; that is expected maintenance semantics.",
		})
		for _, v := range plan.Volumes {
			plan.Steps = append(plan.Steps, MigrationStep{
				Title: "Backup volume " + v.Name + " on the source node",
				Detail: "On " + plan.FromNode + ": docker run --rm -v " + v.Name +
					":/data -v <repo>:/repo restic:<version> backup /data — <repo> is your restic repository (S3 after E3, or SFTP/local path), restic:<version> is a restic image you trust (the platform does not pin one).",
			})
			plan.Steps = append(plan.Steps, MigrationStep{
				Title: "Restore volume " + v.Name + " on the target node",
				Detail: "On " + plan.ToNode + ": docker run --rm -v " + v.Name +
					":/data -v <repo>:/repo restic:<version> restore --target /data <latest-snapshot-id>",
			})
		}
	}
	plan.Steps = append(plan.Steps, MigrationStep{
		Title: "Rebind the app to " + plan.ToNode,
		Detail: "fleetly placement rebind " + appName + " --node " + target.describe() +
			" --data-restored --confirm-destructive (the platform validates the target and records the previous node; the rebind does not deploy)",
	})
	plan.Steps = append(plan.Steps, MigrationStep{
		Title: "Deploy and verify",
		Detail: "fleetly deploy " + appName + " — the first deploy passes the volume preflight (data declared restored); after verification, clean up residual volumes on the source node (docker volume rm " +
			volumeNameList(plan.Volumes) + "), guided by the residual markers in fleetly volumes list.",
	})
	return plan, nil
}

// volumeNameList 是逗号分隔的卷名清单（runbook 清理步骤用）。
func volumeNameList(vols []state.Volume) string {
	if len(vols) == 0 {
		return "<none>"
	}
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.Name)
	}
	return strings.Join(names, " ")
}
