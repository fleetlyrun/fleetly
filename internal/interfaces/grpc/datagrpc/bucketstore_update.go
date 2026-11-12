package datagrpc

import (
	"context"
	"errors"
	"fmt"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"sort"
	"strings"
)

// UpdateBucketStore 受理：受理段（不可变轴/合并期望态/ValidateSettings/
// 资源范围/票面预检）→ engine 卫星单车道执行（utilityMu 锁内受理钩子完成
// status 门复核与票面内容绑定；成功烧票）→ 审计/事件/confirm 取证行一事
// 务 → 回显行与 static 注记。
func (svc *BucketStoresService) UpdateBucketStore(ctx context.Context, req *structurev1.UpdateBucketStoreRequest) (*structurev1.UpdateBucketStoreResponse, error) {
	row, err := svc.deps.BucketStores.Get(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket_store")
	}
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitBucketStoreUpdate(ctx, req, row)
	if err != nil {
		return nil, err
	}
	ticket := req.GetConfirmation().GetTicket()
	admit := func(locked *bucketstore.BucketStore) (func(), error) {
		if locked.Status != bucketstore.StatusRunning {
			return nil, apperr.New("E_CONFLICT",
				"bucket store %s is %s; update requires a running bucket store (a roll is in progress or the instance is not ready)", locked.Name, locked.Status).
				WithSuggestion("Retry once the bucket store reports running (`fleetly buckets get` shows status); updates during a roll are refused.")
		}
		if plan.digestOf == nil {
			return nil, nil // skip 通道：无票面绑定物。
		}
		digest, derr := plan.digestOf(locked)
		if derr != nil {
			return nil, derr
		}
		redeem, aerr := svc.bindBucketStoreUpdateTicket(ctx, ticket, locked, digest)
		if aerr != nil {
			return nil, aerr
		}
		return redeem, nil
	}
	result, err := svc.deps.Data.UpdateBucketStore(ctx, row.ID, contract.BucketStoreUpdateRequest{
		Settings: plan.settingsRequest, Resources: plan.resourcesRequest, Admit: admit,
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "bucket store %s not found", req.GetBucketStoreId())
		case errors.Is(err, api.ErrSettingsApplyRejected):
			return nil, apperr.New("E_SETTINGS_APPLY_FAILED", "%s", err.Error())
		}
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, apperr.New("E_INTERNAL", "bucket store update failed").WithCause(err)
	}
	confirmFact := svc.bucketStoreUpdateConfirmFact(ctx, req, row)
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketStoreUpdated, Aggregate: "bucket_store", ID: row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "bucket_store.update", Resource: "bucket_store/" + row.ID,
			Detail:  string(grpcapi.MustJSON(map[string]any{"static_applied": result.StaticRolled})),
			AfterFP: row.Engine + " " + row.Name,
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
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{Facts: facts}); err != nil {
		return nil, grpcapi.MapStateError(err, "bucket_store")
	}
	updated, err := svc.deps.BucketStores.Get(ctx, row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket_store")
	}
	return &structurev1.UpdateBucketStoreResponse{BucketStore: bucketStoreMsg(updated), StaticApplied: result.StaticRolled}, nil
}

// bindBucketStoreUpdateTicket 是 bucket store Update 的票面内容绑定
// （utilityMu 锁内调用；bindPubSubUpdateTicket 同形——kind 词差异）。
func (svc *BucketStoresService) bindBucketStoreUpdateTicket(ctx context.Context, ticket string, row *bucketstore.BucketStore, digest string) (func(), error) {
	payload, status := svc.deps.Tickets.PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket for updating bucket store %s has expired", row.Name).
			WithSuggestion("Run the update preview again and confirm the fresh ticket.")
	default:
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the update preview; tickets are single-use.")
	}
	if payload.Verb != previewVerbBucketStoreUpdate || payload.TargetApp != row.ID {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different update target").
			WithSuggestion("Mint a fresh preview ticket for this bucket store.")
	}
	if payload.RequestDigest != digest {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current desired state of bucket store %s", row.Name).
			WithSuggestion("The bucket store row moved since the preview (settings or resources). Run the preview again and confirm the fresh ticket.")
	}
	return func() { svc.deps.Tickets.DiscardConfirm(ticket) }, nil
}

// bucketStoreUpdateConfirmFact 铸 bucket store Update 确认的 LedgerOnly 取证
// 行（pubsubUpdateConfirmFact 同形）。
func (svc *BucketStoresService) bucketStoreUpdateConfirmFact(ctx context.Context, req *structurev1.UpdateBucketStoreRequest, row *bucketstore.BucketStore) *writes.FactRow {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	fr := writes.FactRow{
		Aggregate: "bucket_store", ID: row.ID,
		Action: "confirm.redeem", Resource: "bucket_store/" + row.ID,
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（bucket_store.updated 的流面事实由主 fact 承载）。
		LedgerOnly: true,
	}
	if conf.GetSkip() {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return &fr
	}
	if payload, status := svc.deps.Tickets.PeekConfirm(conf.GetTicket()); status == governance.ConfirmPeekHit {
		fr.AfterFP = fmt.Sprintf("proof=redeemed; verb=%s; preview_digest=%s; request_digest=%s",
			payload.Verb, payload.PreviewDigest, payload.RequestDigest)
	}
	return &fr
}

// bucketStoreUpdateAdmission 是 Update 受理的合并视图（pubsubUpdateAdmission
// 同构）。
type bucketStoreUpdateAdmission struct {
	tpl              bucketstoretemplate.Template
	nextSettings     map[string]string
	settingsRequest  map[string]string
	resourcesRequest *contract.ResourceUpdate
	digestOf         func(row *bucketstore.BucketStore) (string, error)
}

// admitBucketStoreUpdate 是预览/执行共用的受理段（admitPubSubUpdate 同构）。
func (svc *BucketStoresService) admitBucketStoreUpdate(ctx context.Context, req *structurev1.UpdateBucketStoreRequest, row *bucketstore.BucketStore) (*bucketStoreUpdateAdmission, error) {
	if req.GetEngine() != "" || req.GetVersion() != "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine and version are immutable after create (request carried engine=%q version=%q); a bucket store stays on the engine version it was created with",
			req.GetEngine(), req.GetVersion())
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	next := row.Settings
	var settingsReq map[string]string
	if req.GetSettings() != nil {
		next = req.GetSettings().GetValues()
		settingsReq = next
		if settingsReq == nil {
			settingsReq = map[string]string{}
		}
	}
	if err := dataservicetemplate.ValidateSettings(tpl.Knobs(), next); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
	}
	var resourcesReq *contract.ResourceUpdate
	if req.GetResources() != nil {
		if err := validateDatabaseResources(req.GetResources()); err != nil {
			return nil, err
		}
		resourcesReq = &contract.ResourceUpdate{
			CPUMillis: resourcesCPU(req.GetResources()), MemoryMB: resourcesMemory(req.GetResources()),
		}
	}
	nextCPU, nextMemory := row.CPUMillis, row.MemoryMB
	if resourcesReq != nil {
		nextCPU, nextMemory = resourcesReq.CPUMillis, resourcesReq.MemoryMB
	}
	plan := &bucketStoreUpdateAdmission{
		tpl:              tpl,
		nextSettings:     next,
		settingsRequest:  settingsReq,
		resourcesRequest: resourcesReq,
		digestOf: func(r *bucketstore.BucketStore) (string, error) {
			return bucketStoreUpdateDesiredStateDigest(r.ID, next, nextCPU, nextMemory), nil
		},
	}
	if req.GetConfirmation().GetSkip() {
		plan.digestOf = nil
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		digest, derr := plan.digestOf(row)
		if derr != nil {
			return nil, derr
		}
		if _, err := svc.bindBucketStoreUpdateTicket(ctx, ticket, row, digest); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// PreviewUpdateBucketStore 是 UpdateBucketStore 的只读预演（三种数据服务类型同构）：
// admitBucketStoreUpdate 单源受理段 + settings/resources diff + 车道判定 +
// 滚动后果 + 铸票。零行写、零工具容器执行；非 running 行仍可预览。
func (svc *BucketStoresService) PreviewUpdateBucketStore(ctx context.Context, req *structurev1.PreviewUpdateBucketStoreRequest) (*structurev1.PreviewUpdateBucketStoreResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	row, err := svc.deps.BucketStores.Get(ctx, update.GetBucketStoreId())
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket_store")
	}
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitBucketStoreUpdate(ctx, update, row)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdateBucketStoreResponse{}
	var dynamicKeys, staticKeys []string
	for key, class := range knobClassesOf(plan.tpl.Knobs()) {
		old, oldOK := row.Settings[key]
		next, nextOK := plan.nextSettings[key]
		if oldOK == nextOK && old == next {
			continue
		}
		out.SettingsChanges = append(out.SettingsChanges, &structurev1.DatabaseSettingChange{
			Key: key, From: old, To: next, Class: string(class),
		})
		if class == dataservicetemplate.KnobDynamic {
			dynamicKeys = append(dynamicKeys, key)
		} else {
			staticKeys = append(staticKeys, key)
		}
	}
	sort.Strings(dynamicKeys)
	sort.Strings(staticKeys)
	sort.Slice(out.SettingsChanges, func(i, j int) bool {
		return out.SettingsChanges[i].GetKey() < out.SettingsChanges[j].GetKey()
	})
	nextCPU, nextMemory := row.CPUMillis, row.MemoryMB
	if plan.resourcesRequest != nil {
		nextCPU, nextMemory = plan.resourcesRequest.CPUMillis, plan.resourcesRequest.MemoryMB
	}
	out.ResourcesFrom = resourcesMsg(row.CPUMillis, row.MemoryMB)
	out.ResourcesTo = resourcesMsg(nextCPU, nextMemory)
	resourcesChanged := !resourcesFaceEqual(row.CPUMillis, row.MemoryMB, nextCPU, nextMemory)
	out.DynamicKeys = dynamicKeys
	out.StaticKeys = staticKeys
	out.Rollout = "zero_touch"
	if len(staticKeys) > 0 || resourcesChanged {
		out.Rollout = "replace"
	}
	diffLines := make([]string, 0, len(out.SettingsChanges))
	for _, ch := range out.SettingsChanges {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s:%s", ch.GetKey(), ch.GetFrom(), ch.GetTo(), ch.GetClass()))
	}
	digest, derr := plan.digestOf(row)
	if derr != nil {
		return nil, derr
	}
	ticket, ttl, err := svc.deps.Tickets.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          previewVerbBucketStoreUpdate,
		TargetApp:     row.ID,
		RequestDigest: digest,
		PreviewDigest: grpcapi.SHA256Hex([]byte(strings.Join(diffLines, "\n"))),
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

// bucketStoreUpdateDesiredStateDigest 是 bucket store Update 票面
// RequestDigest 的单源公式（desiredStateDigest 族前缀 bucketstore=——跨类型票
// 不可互兑）。
func bucketStoreUpdateDesiredStateDigest(bucketStoreID string, settings map[string]string, cpuMillis, memoryMB *int32) string {
	return desiredStateDigest("bucketstore", bucketStoreID, settings, cpuMillis, memoryMB)
}

// bucket 面事件名（usage 反扫的字面量锚点）。
const (
	eventBucketCreated = "bucket.created"
	eventBucketUpdated = "bucket.updated"
	eventBucketDeleted = "bucket.deleted"
)

// 票面兑换动词锚。
const (
	previewVerbBucketUpdate = "/fleetly.structure.v1.BucketStoresService/UpdateBucket"
	previewVerbBucketDelete = "/fleetly.structure.v1.BucketStoresService/DeleteBucket"
)

// admitBucketParent 校验父实例与 Bucket 面可用性（silo）。
func (svc *BucketStoresService) admitBucketParent(ctx context.Context, bucketStoreID string) (*bucketstore.BucketStore, bucketstoretemplate.BucketAdminFace, error) {
	row, err := svc.deps.BucketStores.Get(ctx, bucketStoreID)
	if err != nil {
		return nil, nil, grpcapi.MapStateError(err, "bucket store")
	}
	if err := svc.deps.Auth.AuthorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, nil, err
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		return nil, nil, apperr.New("E_INVALID_ARGUMENT", "engine %q left the template registry: %s", row.Engine, err)
	}
	face := bucketstoretemplate.BucketAdminFaceOf(tpl)
	if face == nil {
		// 引擎方言可用性缺席 = 能力面关态（G17：501 族）。
		return nil, nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED",
			"bucket store %s runs engine %q which carries no bucket admin face", row.Name, row.Engine)
	}
	return row, face, nil
}
