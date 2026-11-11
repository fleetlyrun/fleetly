package fleetlygrpc

// UpdateDatabase / PreviewUpdateDatabase 的受理面（B2d，ADR-0095 决策
// 5/8/9；design §3.3）：期望态变更的双车道动词与其只读预演。受理六件
// （红队修正后口径）：
//
//   - engine/version 不可变：请求含之即 E_INVALID_ARGUMENT 指引升级路径
//     （ADR-0095 决策 8；proto 字段在座是可执法的机器契约锚）；
//   - status != running 拒 E_CONFLICT「滚动进行中/未就绪」（红队 A6：
//     在滚窗 apply 连 VIP 拒）——status 门在 engine 受理钩子内复核（行
//     在 utilityMu 锁内读取——预检与执行之间的 status 漂移同拦）；
//   - ValidateSettings 对**合并后期望态**全量校验（含未变键——声明式
//     幂等；受理位单源）；
//   - resources 范围校验（B2c 上限复用）；
//   - freeze 入 FrozenVerbs（拦截器执法）；写类 DECLARATION（writeclass
//     表）；审计 database.update + 事件 database.updated + confirm
//     取证行（redeem/skip——票面锚经 peek 取，先于烧票）；
//   - ConfirmableVerbs（拦截器通用验票 + 本面内容绑定：RequestDigest 绑
//     合并后期望态——预览后世界漂移（行 settings/resources 变化）即
//     MISMATCH；成功路径烧票，失败保留可重试）。
//
// 票据时序（Deploy 的 appMu 先例）：验票内容绑定在 engine 受理钩子
// （utilityMu 锁内、读行之后、执行之前）完成，烧票在执行成功后仍在锁内
// 调用——同库并发同票的第二请求排队后 peek 即见 tombstone，单用途票不
// 被双兑；E_CONFIRMATION_STALE 与 E_CONFIRMATION_MISMATCH 按票据腿分
// 流（teardown 先例：过期/漂移 → STALE，伪造/错面/已烧 → MISMATCH）。
//
// 执行真源在 engine → dataservices 卫星 update.go（utilityMu 单写边界 +
// dynamic 先行/static 落行 Kick；E_SETTINGS_APPLY_FAILED 哨兵映射）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/dataservices"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// previewVerbUpdate 是预览铸票的兑换动词锚（票面 Verb 与 ConfirmableVerbs
// 键同串——拦截器通用验票的③比对面）。
const previewVerbUpdate = "/fleetly.structure.v1.DatabasesService/UpdateDatabase"

// UpdateDatabase 受理（B2d，ADR-0095 决策 5）：校验六件 → engine 双车道
// 执行（受理钩子在 utilityMu 锁内完成 status 门与票面内容绑定；dynamic
// 同步热应用先行，失败 = E_SETTINGS_APPLY_FAILED 行不动；成功烧票）→
// 审计/事件一事务（先变更后留痕，DeleteDatabase 同序）→ 回显行与两车道
// 注记。
func (svc *DatabasesService) UpdateDatabase(ctx context.Context, req *structurev1.UpdateDatabaseRequest) (*structurev1.UpdateDatabaseResponse, error) {
	row, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitUpdate(ctx, req, row)
	if err != nil {
		return nil, err
	}
	// 受理临界区钩子（engine 在 utilityMu 锁内调用，Deploy 的 appMu 先例）：
	// status 门在锁内行上复核 + 确认票内容绑定（Peek——不烧；烧票 redeem
	// 由 engine 在执行成功后仍持锁时调用）。skip 通道不验票不烧票
	//（--yes 逃生门，governance.confirmguard 同判）。
	ticket := req.GetConfirmation().GetTicket()
	admit := func(locked *database.Database) (func(), error) {
		if locked.Status != database.StatusRunning {
			return nil, apperr.New("E_CONFLICT",
				"database %s is %s; update requires a running database (a roll is in progress or the database is not ready)", locked.Name, locked.Status).
				WithSuggestion("Retry once the database reports running (`fleetly databases get` shows status); updates during a roll are refused to keep the data-plane apply atomic.")
		}
		if plan.digestOf == nil {
			return nil, nil // skip 通道：无票面绑定物。
		}
		digest, derr := plan.digestOf(locked)
		if derr != nil {
			return nil, derr
		}
		redeem, aerr := svc.bindUpdateTicket(ctx, ticket, locked, digest)
		if aerr != nil {
			return nil, aerr
		}
		return redeem, nil
	}
	// 双车道执行（engine → dataservices 卫星：utilityMu per-entity 互斥全程
	// 持有；dynamic 热应用先行、失败行不动；static 落行 + Kick 滚动）。
	result, err := svc.s.Data.UpdateDatabase(ctx, row.ID, dataservices.UpdateRequest{
		Settings:  plan.settingsRequest,
		Resources: plan.resourcesRequest,
		Admit:     admit,
	})
	if err != nil {
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "database %s not found", req.GetDatabaseId())
		case errors.Is(err, api.ErrSettingsApplyRejected):
			return nil, apperr.New("E_SETTINGS_APPLY_FAILED", "%s", err.Error())
		}
		// 受理钩子（Admit）拒绝已带精确码（E_CONFLICT/E_CONFIRMATION_*）
		//——apperr 原样上抛（拦截器映射 HTTP/gRPC 状态）。
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, apperr.New("E_INTERNAL", "database update failed").WithCause(err)
	}
	// 先变更后留痕（DeleteDatabase 同序）：confirm 取证行（有票面时）+
	// 事件 + 审计一事务；Detail 携两车道注记（键集不携值——事件面最小
	// 披露，行回显面可见全集）。
	confirmFact := svc.updateConfirmFact(ctx, req, row)
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventDatabaseUpdated, Aggregate: "database", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "database.update", Resource: "database/" + row.ID,
			Detail: string(mustJSON(map[string]any{
				"dynamic_applied": result.DynamicApplied,
				"static_applied":  result.StaticRolled,
			})),
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
	if err := svc.s.commit(ctx, writeFact{
		Facts: facts,
	}); err != nil {
		return nil, mapStateError(err, "database")
	}
	updated, err := svc.s.Databases.Get(ctx, row.ID)
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	return &structurev1.UpdateDatabaseResponse{
		Database:       databaseMsg(updated),
		DynamicApplied: result.DynamicApplied,
		StaticApplied:  result.StaticRolled,
	}, nil
}

// bindUpdateTicket 是 Update 的票面内容绑定（utilityMu 锁内调用）：Peek
// （不烧）核对 Verb/目标/RequestDigest（RequestDigest 绑锁内行的合并
// 后期望态——预览后世界漂移即 MISMATCH），返回烧票闭包（执行成功后仍
// 持锁时调用；失败路径不调用——票保留可重试）。过期/漂移腿 STALE，
// 伪造/错面/已烧腿 MISMATCH（teardown 先例）。
func (svc *DatabasesService) bindUpdateTicket(ctx context.Context, ticket string, row *database.Database, digest string) (func(), error) {
	payload, status := svc.s.ConfirmTickets().PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return nil, apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket for updating database %s has expired", row.Name).
			WithSuggestion("Run the update preview again and confirm the fresh ticket.")
	default:
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the update preview; tickets are single-use.")
	}
	if payload.Verb != previewVerbUpdate || payload.TargetApp != row.ID {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different update target").
			WithSuggestion("Mint a fresh preview ticket for this database.")
	}
	if payload.RequestDigest != digest {
		return nil, apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current desired state of database %s", row.Name).
			WithSuggestion("The database row moved since the preview (settings or resources). Run the preview again and confirm the fresh ticket.")
	}
	return func() { svc.s.ConfirmTickets().DiscardConfirm(ticket) }, nil
}

// updateConfirmFact 铸 Update 确认的 LedgerOnly 取证行（teardownConfirmFact
// 同款：票面锚经 peek 取不删，调用时点在 engine 烧票之前；skip 通道落
// proof=skipped）。
func (svc *DatabasesService) updateConfirmFact(ctx context.Context, req *structurev1.UpdateDatabaseRequest, row *database.Database) *writes.FactRow {
	conf := req.GetConfirmation()
	if conf == nil {
		return nil
	}
	fr := writes.FactRow{
		Aggregate: "database", ID: row.ID,
		Action: "confirm.redeem", Resource: "database/" + row.ID,
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（database.updated 的流面事实由主 fact 承载）。
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

// updateAdmission 是 Update 受理的合并视图（预览与执行同源计算——票面
// digest 与 diff 的单一真源，双源漂移即静默失配）。digestOf 在受理段做
// 快速失败预检，执行时在 engine 锁内行上复算（预检时点不烧票、锁内复算
// 为准）。
type updateAdmission struct {
	tpl              databasetemplate.Template
	nextSettings     map[string]string // 合并后期望态（全量）
	settingsRequest  map[string]string // 卫星请求形态（nil = 旋钮面不变）
	resourcesRequest *dataservices.ResourceUpdate
	digestOf         func(row *database.Database) (string, error) // 票面 digest 复算（nil = skip 通道）
}

// admitUpdate 是预览/执行共用的受理段：不可变轴 + 合并期望态 +
// ValidateSettings 全量校验（含未变键——声明式幂等）+ 资源范围 + 票面
// digest 铸造。status 门不在本段（锁内受理钩子单源——预检时点与执行
// 时点之间 status 可漂移，双检即漂移窗口）。
func (svc *DatabasesService) admitUpdate(ctx context.Context, req *structurev1.UpdateDatabaseRequest, row *database.Database) (*updateAdmission, error) {
	// engine/version 不可变（ADR-0095 决策 8）：携带即拒并指引升级路径。
	if req.GetEngine() != "" || req.GetVersion() != "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine and version are immutable after create (request carried engine=%q version=%q); to move to a new engine version, create a new database on the target version restoring from a backup (see the upgrade runbook)",
			req.GetEngine(), req.GetVersion())
	}
	tpl, err := databasetemplate.For(row.Engine, row.Version)
	if err != nil {
		// 防御：受理位已校验值域；行版本离册的存量行（移除序）诚实拒绝。
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	// 合并期望态：settings/resources 的缺席 = 不变面（全量替换语义——
	// values 空 = 清空全部旋钮；DatabaseSettingsUpdate 包装消息承载在场
	// 语义：proto3 map 的缺席与空在线上不可区分）。
	next := row.Settings
	var settingsReq map[string]string
	if req.GetSettings() != nil {
		next = req.GetSettings().GetValues()
		settingsReq = next
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
	plan := &updateAdmission{
		tpl:              tpl,
		nextSettings:     next,
		settingsRequest:  settingsReq,
		resourcesRequest: resourcesReq,
		digestOf: func(r *database.Database) (string, error) {
			return updateDesiredStateDigest(r.ID, next, nextCPU, nextMemory), nil
		},
	}
	if req.GetConfirmation().GetSkip() {
		plan.digestOf = nil // skip 通道：无票面绑定物（拦截器放行在先）。
	}
	// 受理段快速失败预检（锁内为准）：无票非 skip 已被拦截器拦；此处只
	// 在携带票时预 peek 一次伪造/错面/过期腿，把明显坏票挡在 engine 之前
	//（锁内 bindUpdateTicket 仍全量复验——本预检纯诊断分流，不做真相源）。
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		digest, derr := plan.digestOf(row)
		if derr != nil {
			return nil, derr
		}
		if _, err := svc.bindUpdateTicket(ctx, ticket, row, digest); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

// PreviewUpdateDatabase 是 Update 的只读预演（B2d，ADR-0089/0095 决策 9）：
// 与 Update 同一受理段（admitUpdate 单源——预演动词不得低于被预演动词的
// 门槛）+ settings/resources diff + 车道判定 + 滚动后果 + 铸票。零行写、
// 零工具容器执行。预演的 status 门：非 running 行仍可预览（变更计划面
// 可见性；执行门照拦——预演动词零副作用，低于执行门不产生竞态面）。
func (svc *DatabasesService) PreviewUpdateDatabase(ctx context.Context, req *structurev1.PreviewUpdateDatabaseRequest) (*structurev1.PreviewUpdateDatabaseResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	row, err := svc.s.Databases.Get(ctx, update.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	plan, err := svc.admitUpdate(ctx, update, row)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdateDatabaseResponse{}
	// 旋钮级 diff（变更键集；未变键不进场——声明式重放零扰动的预演面）。
	var dynamicKeys, staticKeys []string
	for key, class := range knobClasses(plan.tpl) {
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
	// 资源面 diff + 滚动后果（ADR-0095 决策 9：dynamic 全集 = zero_touch
	// ——容器不重启；含 static 或资源变更 = replace——受监督滚动重启发）。
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
	// 铸票：RequestDigest 绑合并后期望态（执行面锁内同参数复算——双源
	// 漂移即 MISMATCH）；PreviewDigest 绑预演载荷本体；Operator 烙印操作者。
	diffLines := make([]string, 0, len(out.SettingsChanges))
	for _, ch := range out.SettingsChanges {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s:%s", ch.GetKey(), ch.GetFrom(), ch.GetTo(), ch.GetClass()))
	}
	digest, derr := plan.digestOf(row)
	if derr != nil {
		return nil, derr
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbUpdate,
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

// knobClasses 报告模板旋钮白名单的键 → class 映射（diff 侧的车道判定面）。
func knobClasses(tpl databasetemplate.Template) map[string]dataservicetemplate.KnobClass {
	return knobClassesOf(tpl.Knobs())
}

// knobClassesOf 是 knobClasses 的类型中立半边（B4c 起三种数据服务类型共用——旋钮
// schema 住 dataservicetemplate 内核，模板包只影响取值面）。
func knobClassesOf(knobs []dataservicetemplate.Knob) map[string]dataservicetemplate.KnobClass {
	out := map[string]dataservicetemplate.KnobClass{}
	for _, k := range knobs {
		out[k.Key] = k.Class
	}
	return out
}

// updateDesiredStateDigest 是 Update 票面 RequestDigest 的单源公式：目标
// 行 + 合并后期望态（settings 全集 + 资源面；nil 轴以 "-" 承载缺席）。
// 预览铸票与执行面锁内复算同参数调用，漂移即 MISMATCH。
func updateDesiredStateDigest(databaseID string, settings map[string]string, cpuMillis, memoryMB *int32) string {
	return desiredStateDigest("database", databaseID, settings, cpuMillis, memoryMB)
}

// desiredStateDigest 是两类型 Update digest 的共享内核（B4c：database/pubsub
// 前缀词分立——同参数不同前缀即不同票面域，跨类型票不可互兑）。
func desiredStateDigest(kind, entityID string, settings map[string]string, cpuMillis, memoryMB *int32) string {
	lines := []string{kind + "=" + entityID}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, "settings:"+k+"="+settings[k])
	}
	lines = append(lines, "cpu="+derefAxis(cpuMillis), "memory="+derefAxis(memoryMB))
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// derefAxis 折资源轴（nil = "-" 缺席锚；0 与 nil 语义分立——proto 0 值
// 折 nil 后由 digest 承载「无限制」终态）。
func derefAxis(p *int32) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *p)
}

// resourcesMsg 折资源面回显（双 nil = 消息缺席 = 无限制）。
func resourcesMsg(cpuMillis, memoryMB *int32) *structurev1.DatabaseResources {
	if cpuMillis == nil && memoryMB == nil {
		return nil
	}
	out := &structurev1.DatabaseResources{}
	if cpuMillis != nil {
		out.CpuMillis = *cpuMillis
	}
	if memoryMB != nil {
		out.MemoryMb = *memoryMB
	}
	return out
}

// resourcesFaceEqual 比对资源面两态（nil 轴缺席语义参与等值）。
func resourcesFaceEqual(aCPU, aMem, bCPU, bMem *int32) bool {
	return derefAxis(aCPU) == derefAxis(bCPU) && derefAxis(aMem) == derefAxis(bMem)
}
