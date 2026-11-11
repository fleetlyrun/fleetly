package engine

// B1 Change Preview 的引擎半边（设计稿 2026-10-30 §1.3 三段单源）：
// PreviewSpec = 段二（composeSpec——与 SubmitSpec 同一合成函数，不冻结）
// + 段三（projectSpecMem——projectRevision 的内存 spec 投影变体）+
// 受理位只读预检（CheckPeerRefsSpec/CheckPreDeployNetworksSpec 与 Submit
// 单源）。写半边全不触碰：不冻结、不 admission、不 stamp 代戳、不
// pinVolumes、不进 materialize 段（FLEETLY_DB_* 注入发生在 materialize
// 尾段 databaseenv.go:77——预览投影停在 projectRevision 是脱敏红线，
// ADR-0071 决策 5 同款）。
//
// 铸票/装配/warning 组装在 api 层（fleetlygrpc preview.go）；本文件只提供
// 单源原语：合成 Spec、规范形（marshalSpec 同参——diff 与 digest 同序列
// 化，设计稿 §1.3 铁律）、提案投影、预检错误、base 多锚快照与 teardown
// 三锚 + 级联清单（铸/验共享单源，修复批 F2）。

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// PreviewSpec 是 Deploy 受理的只读孪生的引擎半边：段二合成（不冻结）→
// 受理位只读预检 → 段三投影（strict）。req.Submit 与 req.Confirmation
// 必须为 nil（预览永不受理、永不消费票——编程错误防线）。
func (e *Engine) PreviewSpec(ctx context.Context, req SubmitSpecRequest) (*PreviewSpecResult, error) {
	if req.AppID == "" || req.Spec == nil {
		return nil, fmt.Errorf("engine: preview spec: app_id and spec are required")
	}
	if req.Merge != MergeNone && !req.HookOwned {
		return nil, fmt.Errorf("engine: preview spec: merge without hook ownership is a programming error (the api gate must set HookOwned for MergeMaterial/MergeOverlay)")
	}
	if req.Submit != nil || req.Confirmation != nil {
		return nil, fmt.Errorf("engine: preview spec: preview never admits or consumes tickets (Submit/Confirmation must be nil)")
	}
	// 段二：合成（与 SubmitSpec 同一函数——预览不复合成即违反「确认的
	// 正是展示的」）。
	composed, _, err := e.composeSpec(ctx, req)
	if err != nil {
		return nil, err
	}
	// 规范形（diff 与 digest 同源字节；与冻结咽喉同参）。
	body, err := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(composed)
	if err != nil {
		return nil, err
	}
	out := &PreviewSpecResult{Spec: composed, SpecBody: body, QueueCapacity: e.opts.Deploy.QueueCapacity}
	// 受理位只读预检（spec 参数变体，与 Submit 单源）：peer 引用与
	// preDeploy 网络是入队即拒项——预览不失败，错误透出给 warning 面。
	out.PeerErr = e.CheckPeerRefsSpec(ctx, e.repos, composed.GetApp().GetProject(), composed)
	out.PreDeployNetworksErr = e.CheckPreDeployNetworksSpec(ctx, e.repos, composed.GetApp().GetProject(), composed)
	// 段三：提案投影（恒 strict——部署链口径）。strict 因未批准 peer 失败
	// 时退 isolate 重投影（warning 已透出拒因；workload 面仍可展示——
	// 与对照半边 RenderWorkloads 的 isolate 口径一致）。
	ws, pending, perr := e.projectSpecMem(ctx, req.AppID, composed, false)
	if perr != nil {
		if !errors.Is(perr, ErrCrossProjectRefNotApproved) {
			return nil, perr
		}
		out.StrictDegraded = true
		ws, pending, perr = e.projectSpecMem(ctx, req.AppID, composed, true)
		if perr != nil {
			return nil, perr
		}
	}
	out.Workloads = ws
	out.PendingFromBuild = pending
	return out, nil
}

// ApplyVolumePinning 是 applyVolumePinning 的只读导出（B1 预览管线消费：
// 提案投影与对照半边 RenderWorkloads 的 placement 同口径——卷钉住只读
// 并入，不 pinVolumes、零写库；materials.go 的渲染纪律同款）。
func (e *Engine) ApplyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error {
	if e.intake == nil {
		return errIntakeUnconfigured
	}
	return e.intake.ApplyVolumePinning(ctx, ws, projectID)
}

// ConfirmBaseSnapshot 采集铸票时刻的 base 多锚（B1 铸票面消费原语）：
// 与 admission 消费面（checkConfirmBase → confirmBaseNow）同一采集函数
// ——铸票与验票的锚定义单源。锚缺失态以空串入摘要（首次部署空基线同参
// 闭合，confirmBaseNow 注释）。
func (e *Engine) ConfirmBaseSnapshot(ctx context.Context, appID string) (governance.ConfirmBase, error) {
	a, err := e.repos.Apps.Get(ctx, appID)
	if err != nil {
		return governance.ConfirmBase{}, err
	}
	return confirmBaseNow(ctx, e.repos, appID, a.ProjectID)
}

// TeardownAnchors 采集拆除票三锚（B1 PreviewTeardown 铸票面与
// peekTeardownConfirm 消费面共用，设计稿 §1.4 teardown 票绑定）：app 行
// 版本（UpdatedAt 即行内容换代戳——hook 行版本锚同款形态）+ hook 行
// digest + route 行 ID 集摘要（governance.ConfirmRouteSetDigest 单源）。
// 锚缺失态（无 hook 行/无 route 行）以空串入票面，消费面无条件全等比对
// （空==空闭合，空↔非空即 STALE——checkConfirmBase 空串全等同形态）。
func (e *Engine) TeardownAnchors(ctx context.Context, appID string) (governance.ConfirmBase, error) {
	var base governance.ConfirmBase
	a, err := e.repos.Apps.Get(ctx, appID)
	if err != nil {
		return base, err
	}
	base.AppRowVersion = a.UpdatedAt
	if h, herr := e.repos.Hooks.Get(ctx, appID); herr == nil {
		base.HookRowVersion = h.UpdatedAt
	} else if !errors.Is(herr, state.ErrNotFound) {
		return base, herr
	}
	routes, rerr := e.repos.Routes.List(ctx)
	if rerr != nil {
		return base, rerr
	}
	ids := make([]string, 0, len(routes))
	for _, rt := range routes {
		// tombstone 行不进锚集（DeleteApp 的级联删除只面对活跃行）。
		if rt.AppID == appID && !rt.Deleted() {
			ids = append(ids, rt.ID)
		}
	}
	base.RouteSetDigest = governance.ConfirmRouteSetDigest(ids)
	return base, nil
}

// TeardownCascade 采集拆除级联清单（修复批 F2 铸/验共享单源）：将拆载体
// （当前生效渲染投影——RenderWorkloads 恒 isolate，与 PreviewTeardown 展示
// 面同源；无成功部署 = 无载体腿，不照抄渲染面 ErrNotFound）+ 将删 hook 行
// （repo 名）+ 将删 route 行（host+path；tombstone 行不进场——DeleteApp 的
// 级联删除只面对活跃行）。条目序确定：载体（投影序）→ hook → route
// （行序），清单 digest 同序敏感。
func (e *Engine) TeardownCascade(ctx context.Context, appID string) ([]TeardownCascadeEntry, error) {
	var out []TeardownCascadeEntry
	_, ws, err := e.RenderWorkloads(ctx, appID, 0)
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	for _, w := range ws {
		out = append(out, TeardownCascadeEntry{
			Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_WORKLOAD, Name: w.ID,
		})
	}
	if h, herr := e.repos.Hooks.Get(ctx, appID); herr == nil {
		out = append(out, TeardownCascadeEntry{
			Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_HOOK_ROW, Name: h.Repo,
		})
	} else if !errors.Is(herr, state.ErrNotFound) {
		return nil, herr
	}
	routes, rerr := e.repos.Routes.List(ctx)
	if rerr != nil {
		return nil, rerr
	}
	for _, rt := range routes {
		if rt.AppID != appID || rt.Deleted() {
			continue
		}
		name := rt.Host
		if rt.Path != "" {
			name += rt.Path
		}
		out = append(out, TeardownCascadeEntry{
			Kind: deliveryv1.TeardownItemKind_TEARDOWN_ITEM_KIND_ROUTE_ROW, Name: name,
		})
	}
	return out, nil
}
