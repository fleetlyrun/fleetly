package fleetlygrpc

// volumes purge（B7，ADR-0094 决策 5 / design §2.4 升级正典③）：一次性
// 数据销毁动作——物理卷删除（容量回收动机）+ 行 tombstone（DeleteVolume
// 语义延伸：tombstone 保 never-reuse 名纪律与审计链，不引入硬删面）。
//
// 执行序镜像 DeleteDatabase（先拆物理面、后落账）：Runtime 物理卷删除
// （幂等——载体缺席 = no-op，断点重入可独立重放）→ 引用预检复检 +
// tombstone + 事件 volume.purged + 审计一事务。物理删除先行 + 幂等重放
// 使中断重跑自然收口：物理已删而落账失败的重入 = no-op 删除 + 落账。
//
// wide 档确认（ADR-0086 风险三档）：ConfirmableVerbs 成员（拦截器通用验
// 票 + 本面内容绑定）：RequestDigest 绑 purge 清单本体（卷行 id+名+pinned
// 节点+容量行）——预览后世界漂移即 MISMATCH；成功路径烧票，失败票保留
// 可重试（UpdateDatabase 同律）。skip 通道（--yes）不验票不烧票，审计
// proof=skipped。
//
// 容量披露（ADR-0092 探明裁定）：RuntimeVolumePurge.VolumeCarrierUsage
// live 回报（swarm = docker volume inspect UsageData、k3s = PVC
// status.capacity）——两在册 Runtime 均可回报；不可得（未绑定/无使用数据/
// 载体缺席）usage_known=false 诚实缺席，不猜。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// previewVerbPurge 是 purge 预览铸票的兑换动词锚（票面 Verb 与
// ConfirmableVerbs 键同串）。
const previewVerbPurge = "/fleetly.structure.v1.VolumesService/PurgeVolume"

// eventVolumePurged 是 purge 的事件名（eventcode 注册表成员）。
const eventVolumePurged = "volume.purged"

// purgeFaces 解析 Runtime 的 purge 执行面（幂等删除 + 容量回报双半边）。
// 面缺席受理精确失败——破坏性动作不适用降级文化（fail-closed）。
func (s *Services) purgeFaces() (capability.RuntimeVolumePurge, error) {
	purge := capability.FacesOf(s.Runtime).VolumePurge
	if purge == nil {
		return nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED",
			"the configured runtime does not implement physical volume purge (RuntimeVolumePurge face absent)")
	}
	return purge, nil
}

// volumePurgeNamespace 解析卷所在 Project 的隔离域锚（Team+Project——
// k3s PVC 按 Project 命名空间、swarm 卷名全局忽略 ns）。
func (s *Services) volumePurgeNamespace(ctx context.Context, row *volume.Volume) (capability.NamespaceRef, error) {
	p, err := s.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return capability.NamespaceRef{}, apperr.New("E_INTERNAL", "project %s resolution failed for volume purge", row.ProjectID).WithCause(err)
	}
	return capability.NamespaceRef{Team: p.TeamID, Project: row.ProjectID}, nil
}

// volumeUsage 是容量行的双形（bytes 有意义 ⇔ known）。
type volumeUsage struct {
	bytes int64
	known bool
}

// probeVolumeUsage 探物理卷当前占用（现行 id 键优先；缺席回落历史名键
// ——pre-ADR-0097 载体的清理面，VolumeCarrierExists 双探测同款）。两形
// 皆缺席 = (0, false)（purge 幂等同判）。
func (s *Services) probeVolumeUsage(ctx context.Context, purge capability.RuntimeVolumePurge, ns capability.NamespaceRef, row *volume.Volume) (volumeUsage, error) {
	if bytes, ok, err := purge.VolumeCarrierUsage(ctx, ns, row.ID); err != nil {
		return volumeUsage{}, err
	} else if ok {
		return volumeUsage{bytes: bytes, known: true}, nil
	}
	bytes, ok, err := purge.VolumeCarrierUsage(ctx, ns, row.Name)
	if err != nil {
		return volumeUsage{}, err
	}
	return volumeUsage{bytes: bytes, known: ok}, nil
}

// purgeDigest 是 purge 票面 RequestDigest 的单源公式（预览铸票与执行面
// 复验同参数调用，漂移即 MISMATCH）：清单锚（行 id+名+pinned 节点）+
// 容量行（known=false 折 "-"）。
func purgeDigest(row *volume.Volume, usage volumeUsage) string {
	usageLine := "-"
	if usage.known {
		usageLine = fmt.Sprintf("%d", usage.bytes)
	}
	return sha256Hex([]byte(strings.Join([]string{
		"volume=" + row.ID,
		"name=" + row.Name,
		"pinned=" + row.PinnedNodeID,
		"usage=" + usageLine,
	}, "\n")))
}

// purgeVolumeCarrier 执行物理卷删除（幂等）：现行 id 键 + 历史名键双形
// 各删一次（各自 no-op 安全——NotFound 不计错）。历史名键是 pre-ADR-0097
// 载体的清理面（升级序 runbook 的迁移指引落点）；跨项目同名历史载体撞
// 键是名键时代的已知病灶（ADR-0097 立法动机），此处按名清历史残留与
// 该病灶同一语义域，v1 接受（staging flag-day 后干净态）。
func (s *Services) purgeVolumeCarrier(ctx context.Context, purge capability.RuntimeVolumePurge, ns capability.NamespaceRef, row *volume.Volume) error {
	if err := purge.PurgeVolumeCarrier(ctx, ns, row.ID); err != nil {
		return apperr.New("E_INTERNAL", "volume %s physical purge failed", row.Name).WithCause(err)
	}
	if err := purge.PurgeVolumeCarrier(ctx, ns, row.Name); err != nil {
		return apperr.New("E_INTERNAL", "volume %s physical purge failed", row.Name).WithCause(err)
	}
	return nil
}

// requirePurgeable 是 purge 受理的引用预检（与 DeleteVolume 同谓词）：
// 被 App 冻结 Spec 或活跃同名 Database 挂靠即 E_CONFLICT——purge 是销毁
// 动作，引用面必须先解（重部署摘挂载或删库）。
func (s *Services) requirePurgeable(ctx context.Context, row *volume.Volume) error {
	refs, err := s.volumeReferences(ctx, row)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return apperr.New("E_CONFLICT",
			"volume %s is still referenced: %s - detach it from the referencing apps (redeploy without the volume attachment) or delete the database first",
			row.Name, strings.Join(refs, ", "))
	}
	return nil
}

// owningDatabase 披露卷的挂靠库关联（预览清单的「关联库或独立卷」行）：
// 同名最新行含 tombstone——活跃行命中早已被引用预检拒绝，达本面的关联
// 行恒为已删库（升级正典③的典型形态）；行缺席 = 独立卷。
func (s *Services) owningDatabase(ctx context.Context, row *volume.Volume) (id, name string, err error) {
	db, derr := s.Databases.GetByNameAnyState(ctx, row.ProjectID, row.Name)
	if derr != nil {
		if errors.Is(derr, state.ErrNotFound) {
			return "", "", nil // 独立卷：无同名库行
		}
		return "", "", derr
	}
	return db.ID, db.Name, nil
}

// PreviewPurgeVolume 是 Purge 的只读预演（B7，ADR-0089 wide 档）：purge
// 清单（卷名/载体键/pinned 节点/关联库或独立卷）+ 容量行 + 铸票。零行写、
// 零物理删除。
func (svc *VolumesService) PreviewPurgeVolume(ctx context.Context, req *structurev1.PreviewPurgeVolumeRequest) (*structurev1.PreviewPurgeVolumeResponse, error) {
	row, err := svc.s.Volumes.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.s.requirePurgeable(ctx, row); err != nil {
		return nil, err
	}
	purge, err := svc.s.purgeFaces()
	if err != nil {
		return nil, err
	}
	ns, err := svc.s.volumePurgeNamespace(ctx, row)
	if err != nil {
		return nil, err
	}
	usage, err := svc.s.probeVolumeUsage(ctx, purge, ns, row)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "volume %s capacity probe failed", row.Name).WithCause(err)
	}
	out := &structurev1.PreviewPurgeVolumeResponse{
		Name: row.Name, CarrierKey: row.ID, PinnedNodeId: row.PinnedNodeID,
		UsageBytes: usage.bytes, UsageKnown: usage.known,
	}
	if dbID, dbName, derr := svc.s.owningDatabase(ctx, row); derr != nil {
		return nil, mapStateError(derr, "database")
	} else if dbID != "" {
		out.DatabaseId, out.DatabaseName = dbID, dbName
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbPurge,
		TargetApp:     row.ID,
		RequestDigest: purgeDigest(row, usage),
		PreviewDigest: sha256Hex([]byte(fmt.Sprintf("%s/%s/%d", out.GetName(), out.GetCarrierKey(), out.GetUsageBytes()))),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	out.Ticket = ticket
	//nolint:gosec // TTL 是 600s 常量（confirmTicketTTL），int32 安全。
	out.TicketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// PurgeVolume 受理（B7）：引用预检 → 票面内容绑定（Peek 不烧；成功后烧）
// → 容量探取（回收量回执）→ 物理卷删除（幂等）→ tombstone + 事件 +
// 审计一事务。执行序镜像 DeleteDatabase（先拆物理面、后落账）。
func (svc *VolumesService) PurgeVolume(ctx context.Context, req *structurev1.PurgeVolumeRequest) (*structurev1.PurgeVolumeResponse, error) {
	row, err := svc.s.Volumes.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.s.requirePurgeable(ctx, row); err != nil {
		return nil, err
	}
	purge, err := svc.s.purgeFaces()
	if err != nil {
		return nil, err
	}
	ns, err := svc.s.volumePurgeNamespace(ctx, row)
	if err != nil {
		return nil, err
	}
	usage, err := svc.s.probeVolumeUsage(ctx, purge, ns, row)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "volume %s capacity probe failed", row.Name).WithCause(err)
	}
	// 票面内容绑定（skip 通道无票面绑定物；失败路径不烧票——保留可重试）。
	ticket := req.GetConfirmation().GetTicket()
	if ticket != "" {
		if err := svc.s.bindPurgeTicket(ctx, ticket, row, usage); err != nil {
			return nil, err
		}
	}
	// 物理卷删除（幂等）→ 落账（事件 + 审计 + confirm 取证行一事务）。
	if err := svc.s.purgeVolumeCarrier(ctx, purge, ns, row); err != nil {
		return nil, err
	}
	confirmFact := purgeConfirmFact(ctx, req, row)
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventVolumePurged, Aggregate: "volume", ID: row.Name,
			Payload: mustJSON(structureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "volume.purge", Resource: "volume/" + row.ID,
			Detail: string(mustJSON(map[string]any{
				"carrier_key":  row.ID,
				"usage_known":  usage.known,
				"purged_bytes": usage.bytes,
			})),
			AfterFP: row.PinnedNodeID,
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	facts := []func() writes.FactRow{fact}
	if confirmFact != nil {
		cf := *confirmFact
		facts = append([]func() writes.FactRow{func() writes.FactRow { return cf }}, facts...)
	}
	if err := svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Volumes.SoftDelete(ctx, req.GetId())
		},
		Facts: facts,
	}); err != nil {
		return nil, mapStateError(err, "volume")
	}
	if ticket != "" {
		svc.s.ConfirmTickets().DiscardConfirm(ticket)
	}
	return &structurev1.PurgeVolumeResponse{
		Name: row.Name, CarrierKey: row.ID,
		PurgedBytes: usage.bytes, UsageKnown: usage.known,
	}, nil
}

// bindPurgeTicket 是 purge 的票面内容绑定：Peek（不烧）核对 Verb/目标/
// RequestDigest（绑当前行与容量——预览后世界漂移即 MISMATCH）。烧票由
// 调用方在落账成功后执行（失败路径票保留可重试）。
func (s *Services) bindPurgeTicket(ctx context.Context, ticket string, row *volume.Volume, usage volumeUsage) error {
	payload, status := s.ConfirmTickets().PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket for purging volume %s has expired", row.Name).
			WithSuggestion("Run the purge preview again and confirm the fresh ticket.")
	default:
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the purge preview; tickets are single-use.")
	}
	if payload.Verb != previewVerbPurge || payload.TargetApp != row.ID {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different purge target").
			WithSuggestion("Mint a fresh preview ticket for this volume.")
	}
	if payload.RequestDigest != purgeDigest(row, usage) {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current state of volume %s", row.Name).
			WithSuggestion("The volume (pinning or capacity) moved since the preview. Run the preview again and confirm the fresh ticket.")
	}
	return nil
}

// purgeConfirmFact 铸 purge 确认的 LedgerOnly 取证行（updateConfirmFact
// 同款：票面锚经 peek 取不删；skip 通道落 proof=skipped）。
func purgeConfirmFact(ctx context.Context, req *structurev1.PurgeVolumeRequest, row *volume.Volume) *writes.FactRow {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	fr := writes.FactRow{
		Aggregate: "volume", ID: row.ID,
		Action: "confirm.redeem", Resource: "volume/" + row.ID,
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（volume.purged 的流面事实由主 fact 承载——
		// updateConfirmFact 同款）。
		LedgerOnly: true,
	}
	if conf.GetSkip() {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return &fr
	}
	return &fr
}
