package fleetlygrpc

// UpdatePubSub / PreviewUpdatePubSub 的受理面（B4c，ADR-0095 决策 5/8/9 三
// 类型对齐——翻 B4b「单车道无确认面」取舍为同构；design §3.3）：与
// database 类型 UpdateDatabase/PreviewUpdateDatabase 同一形态（updateAdmission
// 单源受理段 + ConfirmableVerbs 验票/烧票时序），kind 差异收在：
//
//   - 模板包 = pubsubtemplate（B6a kafka 起类型内双车道齐形：dynamic_keys 实值
//     流入预演〔zero_touch〕与执行〔卫星 UpdatePubSub 热应用〕；nats 全
//     static——dynamic_keys 恒空）；
//   - 票面动词锚 = PubSubsService/UpdatePubSub；digest 前缀 = pubsub=；
//   - 事件/审计 aggregate = pubsub（eventPubSubUpdated）。
//
// 票据时序（Deploy 的 appMu 先例同款）：验票内容绑定在 engine 受理钩子
// （utilityMu 锁内、读行之后、执行之前）完成，烧票在执行成功后仍在锁内
// 调用——同库并发同票的第二请求排队后 peek 即见 tombstone，单用途票不
// 被双兑；E_CONFIRMATION_STALE 与 E_CONFIRMATION_MISMATCH 按票据腿分流。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/dataservices"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// previewVerbPubSubUpdate 是 pubsub 预览铸票的兑换动词锚（票面 Verb 与
// ConfirmableVerbs 键同串）。
const previewVerbPubSubUpdate = "/fleetly.structure.v1.PubSubsService/UpdatePubSub"

// UpdatePubSub 受理（B4c）：受理段（不可变轴/合并期望态/ValidateSettings/
// 资源范围/票面预检）→ engine 卫星单车道执行（utilityMu 锁内受理钩子完成
// status 门复核与票面内容绑定；成功烧票）→ 审计/事件/confirm 取证行一事
// 务 → 回显行与 static 注记。
func (svc *PubSubsService) UpdatePubSub(ctx context.Context, req *structurev1.UpdatePubSubRequest) (*structurev1.UpdatePubSubResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitPubSubUpdate(ctx, req, row)
	if err != nil {
		return nil, err
	}
	// 受理临界区钩子（engine 在 utilityMu 锁内调用）：status 门在锁内行上
	// 复核 + 确认票内容绑定（Peek——不烧；烧票 redeem 由 engine 在执行成
	// 功后仍持锁时调用）。skip 通道不验票不烧票（--yes 逃生门）。
	ticket := req.GetConfirmation().GetTicket()
	admit := func(locked *pubsub.PubSub) (func(), error) {
		if locked.Status != pubsub.StatusRunning {
			return nil, apperr.New("E_CONFLICT",
				"pubsub %s is %s; update requires a running pubsub (a roll is in progress or the instance is not ready)", locked.Name, locked.Status).
				WithSuggestion("Retry once the pubsub reports running (`fleetly pubsubs get` shows status); updates during a roll are refused.")
		}
		if plan.digestOf == nil {
			return nil, nil // skip 通道：无票面绑定物。
		}
		digest, derr := plan.digestOf(locked)
		if derr != nil {
			return nil, derr
		}
		redeem, aerr := svc.bindPubSubUpdateTicket(ctx, ticket, locked, digest)
		if aerr != nil {
			return nil, aerr
		}
		return redeem, nil
	}
	result, err := svc.s.Data.UpdatePubSub(ctx, row.ID, dataservices.PubSubUpdateRequest{
		Settings: plan.settingsRequest, Resources: plan.resourcesRequest, Admit: admit,
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "pubsub %s not found", req.GetPubsubId())
		case errors.Is(err, api.ErrSettingsApplyRejected):
			return nil, apperr.New("E_SETTINGS_APPLY_FAILED", "%s", err.Error())
		}
		// 受理钩子拒绝已带精确码（E_CONFLICT/E_CONFIRMATION_*）——apperr
		// 原样上抛。
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, apperr.New("E_INTERNAL", "pubsub update failed").WithCause(err)
	}
	// 先变更后留痕：confirm 取证行（有票面时）+ 事件 + 审计一事务。
	confirmFact := svc.pubsubUpdateConfirmFact(ctx, req, row)
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventPubSubUpdated, Aggregate: "pubsub", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "pubsub.update", Resource: "pubsub/" + row.ID,
			Detail:  string(mustJSON(map[string]any{"static_applied": result.StaticRolled, "dynamic_applied": result.DynamicApplied})),
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
	if err := svc.s.commit(ctx, writeFact{Facts: facts}); err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	updated, err := svc.s.PubSubs.Get(ctx, row.ID)
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	return &structurev1.UpdatePubSubResponse{Pubsub: pubsubMsg(updated), StaticApplied: result.StaticRolled, DynamicApplied: result.DynamicApplied}, nil
}

// bindPubSubUpdateTicket 是 pubsub Update 的票面内容绑定（utilityMu 锁内
// 调用；bindUpdateTicket 同形——kind 词差异）。
func (svc *PubSubsService) bindPubSubUpdateTicket(ctx context.Context, ticket string, row *pubsub.PubSub, digest string) (func(), error) {
	payload, status := svc.s.ConfirmTickets().PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket for updating pubsub %s has expired", row.Name).
			WithSuggestion("Run the update preview again and confirm the fresh ticket.")
	default:
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the update preview; tickets are single-use.")
	}
	if payload.Verb != previewVerbPubSubUpdate || payload.TargetApp != row.ID {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different update target").
			WithSuggestion("Mint a fresh preview ticket for this pubsub.")
	}
	if payload.RequestDigest != digest {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current desired state of pubsub %s", row.Name).
			WithSuggestion("The pubsub row moved since the preview (settings or resources). Run the preview again and confirm the fresh ticket.")
	}
	return func() { svc.s.ConfirmTickets().DiscardConfirm(ticket) }, nil
}

// pubsubUpdateConfirmFact 铸 pubsub Update 确认的 LedgerOnly 取证行
// （updateConfirmFact 同形）。
func (svc *PubSubsService) pubsubUpdateConfirmFact(ctx context.Context, req *structurev1.UpdatePubSubRequest, row *pubsub.PubSub) *writes.FactRow {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	fr := writes.FactRow{
		Aggregate: "pubsub", ID: row.ID,
		Action: "confirm.redeem", Resource: "pubsub/" + row.ID,
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（pubsub.updated 的流面事实由主 fact 承载）。
		LedgerOnly: true,
	}
	if conf.GetSkip() {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return &fr
	}
	if payload, status := svc.s.ConfirmTickets().PeekConfirm(conf.GetTicket()); status == governance.ConfirmPeekHit {
		fr.AfterFP = fmt.Sprintf("proof=redeemed; verb=%s; preview_digest=%s; request_digest=%s",
			payload.Verb, payload.PreviewDigest, payload.RequestDigest)
	}
	return &fr
}

// pubsubUpdateAdmission 是 Update 受理的合并视图（updateAdmission 同构）。
type pubsubUpdateAdmission struct {
	tpl              pubsubtemplate.Template
	nextSettings     map[string]string
	settingsRequest  map[string]string
	resourcesRequest *dataservices.ResourceUpdate
	digestOf         func(row *pubsub.PubSub) (string, error)
}

// admitPubSubUpdate 是预览/执行共用的受理段（admitUpdate 同构：不可变轴 +
// 合并期望态 + ValidateSettings 全量校验 + 资源范围 + 票面 digest 铸造 +
// 坏票快速预检；status 门单源在锁内受理钩子）。
func (svc *PubSubsService) admitPubSubUpdate(ctx context.Context, req *structurev1.UpdatePubSubRequest, row *pubsub.PubSub) (*pubsubUpdateAdmission, error) {
	if req.GetEngine() != "" || req.GetVersion() != "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine and version are immutable after create (request carried engine=%q version=%q); to move to a new engine version, create a new pubsub on the target version restoring from a backup",
			req.GetEngine(), req.GetVersion())
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
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
	var resourcesReq *dataservices.ResourceUpdate
	if req.GetResources() != nil {
		if err := validateDatabaseResources(req.GetResources()); err != nil {
			return nil, err
		}
		resourcesReq = &dataservices.ResourceUpdate{
			CPUMillis: resourcesCPU(req.GetResources()), MemoryMB: resourcesMemory(req.GetResources()),
		}
	}
	nextCPU, nextMemory := row.CPUMillis, row.MemoryMB
	if resourcesReq != nil {
		nextCPU, nextMemory = resourcesReq.CPUMillis, resourcesReq.MemoryMB
	}
	// 跨面联动受理闸（B6a，红队 F-13 三分支的 Update 半边：heap_mb 与
	// memory 皆设时 heap ≤ memory×0.7——CreatePubSub 同判据源；无联动面
	// 的引擎不实现该可选面）。
	if lv, ok := tpl.(pubsubtemplate.LinkageValidator); ok {
		memory := int64(0)
		if nextMemory != nil {
			memory = int64(*nextMemory)
		}
		if err := lv.ValidateLinkage(memory, next); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
		}
	}
	plan := &pubsubUpdateAdmission{
		tpl:              tpl,
		nextSettings:     next,
		settingsRequest:  settingsReq,
		resourcesRequest: resourcesReq,
		digestOf: func(r *pubsub.PubSub) (string, error) {
			return pubsubUpdateDesiredStateDigest(r.ID, next, nextCPU, nextMemory), nil
		},
	}
	if req.GetConfirmation().GetSkip() {
		plan.digestOf = nil // skip 通道：无票面绑定物（拦截器放行在先）。
	}
	// 受理段快速失败预检（锁内为准）：明显坏票挡在 engine 之前（诊断分
	// 流，不做真相源）。
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		digest, derr := plan.digestOf(row)
		if derr != nil {
			return nil, derr
		}
		if _, err := svc.bindPubSubUpdateTicket(ctx, ticket, row, digest); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// PreviewUpdatePubSub 是 UpdatePubSub 的只读预演（B4c，三种数据服务类型同构）：
// admitPubSubUpdate 单源受理段 + settings/resources diff + 车道判定 + 滚动
// 后果 + 铸票。零行写、零工具容器执行；非 running 行仍可预览（执行门照
// 拦）。B6a kafka 起类型内双车道齐形：dynamic 变更 = zero_touch、含 static
// 或资源变更 = replace；nats 全 static 恒 replace（除非零变更）。
func (svc *PubSubsService) PreviewUpdatePubSub(ctx context.Context, req *structurev1.PreviewUpdatePubSubRequest) (*structurev1.PreviewUpdatePubSubResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	row, err := svc.s.PubSubs.Get(ctx, update.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitPubSubUpdate(ctx, update, row)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdatePubSubResponse{}
	var dynamicKeys, staticKeys []string
	for key, class := range knobClassesOf(plan.tpl.Knobs()) {
		old, oldOK := row.Settings[key]
		next, nextOK := plan.nextSettings[key]
		if oldOK == nextOK && old == next {
			continue // 未变键不进场。
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
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbPubSubUpdate,
		TargetApp:     row.ID,
		RequestDigest: digest,
		PreviewDigest: sha256Hex([]byte(strings.Join(diffLines, "\n"))),
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

// pubsubUpdateDesiredStateDigest 是 pubsub Update 票面 RequestDigest 的
// 单源公式（updateDesiredStateDigest 同构——前缀词 pubsub=，预览铸票与
// 执行面锁内复算同参数调用，漂移即 MISMATCH）。
func pubsubUpdateDesiredStateDigest(pubsubID string, settings map[string]string, cpuMillis, memoryMB *int32) string {
	return desiredStateDigest("pubsub", pubsubID, settings, cpuMillis, memoryMB)
}
