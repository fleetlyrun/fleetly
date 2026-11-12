package structuregrpc

import (
	"context"
	"errors"
	"fmt"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"google.golang.org/protobuf/encoding/protojson"
	"strings"
)

// ---- Volumes ----

func (svc *VolumesService) CreateVolume(ctx context.Context, req *structurev1.CreateVolumeRequest) (*structurev1.CreateVolumeResponse, error) {
	// name 的 dns 标签形态检查（validName）与 project_id/name 必填组合检查
	// 已退役为 CreateVolumeRequest 的 protovalidate CEL 注解（P4-1 批 1
	// Run B1）：validate 拦截器在 handler 之前执法，组合必填拆 per-field
	// 文案；正则字面单源见 entityid.NamePatternSource（descriptor 对齐
	// 测试执法）。
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &volume.Volume{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		PinnedNodeID: req.GetPinnedNodeId(),
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。事件归属进事务前解析（R08-M8）。
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventVolumeCreated, Aggregate: "volume", ID: row.Name,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "volume.create", Resource: "volume/" + row.Name, AfterFP: row.PinnedNodeID,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{parentProjectAlive(req.GetProjectId())},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Volumes.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	return &structurev1.CreateVolumeResponse{Volume: volumeMsg(*row)}, nil
}

// DeleteVolume 收口删除（IA v3 二期⑤b）：受理位引用预检（事务内——活跃
// App 最新冻结 Spec 的卷附件按平台卷名匹配 + 同名 Database 挂靠卷）任一
// 命中即 E_CONFLICT；删除 = tombstone 一事务（事件 + 审计）。底层编排器
// 卷不随删（swarm 命名卷残留是文档化文化、k3s PVC 卡 ns 排空——数据兜底
// 永不级联；调用方在确认页披露）。
func (svc *VolumesService) DeleteVolume(ctx context.Context, req *structurev1.DeleteVolumeRequest) (*structurev1.DeleteVolumeResponse, error) {
	// id 必填检查已退役为 DeleteVolumeRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	row, err := svc.deps.Volumes.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	if err := svc.deps.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	refs, err := svc.deps.volumeReferences(ctx, row)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		return nil, apperr.New("E_CONFLICT",
			"volume %s is still referenced: %s - detach it from the referencing apps (redeploy without the volume attachment) or delete the database first",
			row.Name, strings.Join(refs, ", "))
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventVolumeDeleted, Aggregate: "volume", ID: row.Name,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "volume.delete", Resource: "volume/" + row.Name,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Volumes.SoftDelete(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	return &structurev1.DeleteVolumeResponse{}, nil
}

// volumeReferences 枚举卷的引用方描述（App 名清单 + Database 挂靠名；空 =
// 未挂载）。卷锚 = 平台卷名（Attachment.volume_id 装 Name——引擎 pinVolumes/
// materials 同口径），不是行 ID。
func (d Deps) volumeReferences(ctx context.Context, vol *volume.Volume) ([]string, error) {
	var refs []string
	apps, err := d.Apps.ListByProject(ctx, vol.ProjectID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "app")
	}
	for i := range apps {
		rev, err := d.Revisions.Latest(ctx, apps[i].ID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 从未部署的 App 无冻结 Spec，无引用
			}
			return nil, grpcapi.MapStateError(err, "spec")
		}
		var spec specv1.AppSpec
		if err := protojson.Unmarshal(rev.Spec, &spec); err != nil {
			return nil, apperr.New("E_INTERNAL", "app spec: frozen revision is not valid protojson (%v)", err)
		}
		for _, process := range spec.GetProcesses() {
			for _, attachment := range process.GetVolumes() {
				if attachment.GetVolumeId() == vol.Name {
					refs = append(refs, "app "+apps[i].Name)
					break
				}
			}
		}
	}
	databases, err := d.Databases.ListByProject(ctx, vol.ProjectID, 0, 200)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "database")
	}
	for i := range databases {
		if databases[i].Name == vol.Name {
			refs = append(refs, "database "+databases[i].Name+" (carrier data volume)")
		}
	}
	return refs, nil
}

func (svc *VolumesService) ListVolumes(ctx context.Context, req *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error) {
	if err := svc.deps.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.deps.Volumes.ListByProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	out := &structurev1.ListVolumesResponse{}
	for _, row := range list {
		out.Volumes = append(out.Volumes, volumeMsg(row))
	}
	return out, nil
}

// previewVerbPurge 是 purge 预览铸票的兑换动词锚（票面 Verb 与
// ConfirmableVerbs 键同串）。
const previewVerbPurge = "/fleetly.structure.v1.VolumesService/PurgeVolume"

// eventVolumePurged 是 purge 的事件名（eventcode 注册表成员）。
const eventVolumePurged = "volume.purged"

// purgeFaces 解析 Runtime 的 purge 执行面（幂等删除 + 容量回报双半边）。
// 面缺席受理精确失败——破坏性动作不适用降级文化（fail-closed）。
func (d Deps) purgeFaces() (capability.RuntimeVolumePurge, error) {
	purge := capability.FacesOf(d.Runtime).VolumePurge
	if purge == nil {
		return nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED",
			"the configured runtime does not implement physical volume purge (RuntimeVolumePurge face absent)")
	}
	return purge, nil
}

// volumePurgeNamespace 解析卷所在 Project 的隔离域锚（Team+Project——
// k3s PVC 按 Project 命名空间、swarm 卷名全局忽略 ns）。
func (d Deps) volumePurgeNamespace(ctx context.Context, row *volume.Volume) (capability.NamespaceRef, error) {
	p, err := d.Projects.Get(ctx, row.ProjectID)
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
func (d Deps) probeVolumeUsage(ctx context.Context, purge capability.RuntimeVolumePurge, ns capability.NamespaceRef, row *volume.Volume) (volumeUsage, error) {
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
	return grpcapi.SHA256Hex([]byte(strings.Join([]string{
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
func (d Deps) purgeVolumeCarrier(ctx context.Context, purge capability.RuntimeVolumePurge, ns capability.NamespaceRef, row *volume.Volume) error {
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
func (d Deps) requirePurgeable(ctx context.Context, row *volume.Volume) error {
	refs, err := d.volumeReferences(ctx, row)
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
func (d Deps) owningDatabase(ctx context.Context, row *volume.Volume) (id, name string, err error) {
	db, derr := d.Databases.GetByNameAnyState(ctx, row.ProjectID, row.Name)
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
	row, err := svc.deps.Volumes.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	if err := svc.deps.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.deps.requirePurgeable(ctx, row); err != nil {
		return nil, err
	}
	purge, err := svc.deps.purgeFaces()
	if err != nil {
		return nil, err
	}
	ns, err := svc.deps.volumePurgeNamespace(ctx, row)
	if err != nil {
		return nil, err
	}
	usage, err := svc.deps.probeVolumeUsage(ctx, purge, ns, row)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "volume %s capacity probe failed", row.Name).WithCause(err)
	}
	out := &structurev1.PreviewPurgeVolumeResponse{
		Name: row.Name, CarrierKey: row.ID, PinnedNodeId: row.PinnedNodeID,
		UsageBytes: usage.bytes, UsageKnown: usage.known,
	}
	if dbID, dbName, derr := svc.deps.owningDatabase(ctx, row); derr != nil {
		return nil, grpcapi.MapStateError(derr, "database")
	} else if dbID != "" {
		out.DatabaseId, out.DatabaseName = dbID, dbName
	}
	ticket, ttl, err := svc.deps.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          previewVerbPurge,
		TargetApp:     row.ID,
		RequestDigest: purgeDigest(row, usage),
		PreviewDigest: grpcapi.SHA256Hex([]byte(fmt.Sprintf("%s/%s/%d", out.GetName(), out.GetCarrierKey(), out.GetUsageBytes()))),
		Operator:      grpcapi.ConfirmOperator(ctx),
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "ticket")
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
	row, err := svc.deps.Volumes.Get(ctx, req.GetId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	if err := svc.deps.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.deps.requirePurgeable(ctx, row); err != nil {
		return nil, err
	}
	purge, err := svc.deps.purgeFaces()
	if err != nil {
		return nil, err
	}
	ns, err := svc.deps.volumePurgeNamespace(ctx, row)
	if err != nil {
		return nil, err
	}
	usage, err := svc.deps.probeVolumeUsage(ctx, purge, ns, row)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "volume %s capacity probe failed", row.Name).WithCause(err)
	}
	// 票面内容绑定（skip 通道无票面绑定物；失败路径不烧票——保留可重试）。
	ticket := req.GetConfirmation().GetTicket()
	if ticket != "" {
		if err := svc.deps.bindPurgeTicket(ctx, ticket, row, usage); err != nil {
			return nil, err
		}
	}
	// 物理卷删除（幂等）→ 落账（事件 + 审计 + confirm 取证行一事务）。
	if err := svc.deps.purgeVolumeCarrier(ctx, purge, ns, row); err != nil {
		return nil, err
	}
	confirmFact := purgeConfirmFact(ctx, req, row)
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventVolumePurged, Aggregate: "volume", ID: row.Name,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "volume.purge", Resource: "volume/" + row.ID,
			Detail: string(grpcapi.MustJSON(map[string]any{
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
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Volumes.SoftDelete(ctx, req.GetId())
		},
		Facts: facts,
	}); err != nil {
		return nil, grpcapi.MapStateError(err, "volume")
	}
	if ticket != "" {
		svc.deps.DiscardConfirm(ticket)
	}
	return &structurev1.PurgeVolumeResponse{
		Name: row.Name, CarrierKey: row.ID,
		PurgedBytes: usage.bytes, UsageKnown: usage.known,
	}, nil
}

// bindPurgeTicket 是 purge 的票面内容绑定：Peek（不烧）核对 Verb/目标/
// RequestDigest（绑当前行与容量——预览后世界漂移即 MISMATCH）。烧票由
// 调用方在落账成功后执行（失败路径票保留可重试）。
func (d Deps) bindPurgeTicket(ctx context.Context, ticket string, row *volume.Volume, usage volumeUsage) error {
	payload, status := d.PeekConfirm(ticket)
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
