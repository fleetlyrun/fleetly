package fleetlygrpc

// B1 Change Preview 的 api 装配面（W2a，设计稿 2026-10-30 §1.3/§1.4）：
//
//   - PreviewDeploy = 段一（deployIntake，与 Deploy 同一入口执法）+ 引擎
//     段二/段三只读孪生（PreviewSpec——合成 Spec 不冻结、内存 spec 投影）
//     + spec_diff/workload_changes/warnings 装配 + 铸 Confirm Ticket。
//   - PreviewTeardown = 新装配非单源（行级联在 API 层 tombstone 事务，
//     预演是其只读镜像）+ 三锚铸票（app 行版本 + hook 行 digest + route
//     行 ID 集摘要）。
//
// 脱敏红线（ADR-0071 决策 5 同款，设计稿 §1.3 A1）：预览载荷零 env 值集
// （spec_diff 的 env 叶红acted、WorkloadChange 无 env 字段——至多 name
// 集）、零 FLEETLY_DB_*（材料注入在 materialize 尾段 databaseenv.go——
// 预览投影停在 projectRevision，绝不进 materialize 段）、零材料值、零代戳。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// jsonUnmarshalTree / jsonMarshalTree 是 diff 输入侧脱敏的 JSON 装卸
// （encoding/json——spec.Diff 同参数形态）。
func jsonUnmarshalTree(body []byte) (any, error) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func jsonMarshalTree(v any) ([]byte, error) {
	return json.Marshal(v)
}

// previewVerbDeploy / previewVerbDeleteApp 是预览铸票的兑换动词锚（票面
// Verb 与 ConfirmableVerbs 键同串——拦截器通用验票的③比对面）。
const (
	previewVerbDeploy = "/fleetly.delivery.v1.DeploymentsService/Deploy"
	previewVerbDelete = "/fleetly.structure.v1.AppsService/DeleteApp"
)

// redactedEnvValue 是 spec_diff 中 env 叶的值面（脱敏红线：至多 name 集——
// 键名经 diff 路径可见，值不可见）。
const redactedEnvValue = "(redacted)"

// PreviewDeploy 是 Deploy 受理的只读孪生（B1，设计稿 §1.3）：三段单源
// （段一 deployIntake / 段二合成 / 段三内存投影——与 Deploy 逐段同函数），
// 产出 spec_diff（vs LatestSucceeded 冻结体；首次部署空基线 = 全量 added，
// 不照抄渲染面的 ErrNotFound）、workload_changes（提案投影 strict vs 当前
// 生效渲染 isolate——口径差进 fixture）与结构化 warnings，并铸 Confirm
// Ticket（票面绑定物与验票序见设计稿 §1.4）。只读 + 铸票零其他副作用。
func (svc *DeploymentsService) PreviewDeploy(ctx context.Context, req *deliveryv1.PreviewDeployRequest) (*deliveryv1.PreviewDeployResponse, error) {
	// 空载荷兜底（内嵌 DeployRequest 缺席——descriptor 的 at-least-one 规则
	// 住 DeployRequest，顶层缺席在此显式拒）。
	deployReq := req.GetDeploy()
	if deployReq == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "deploy: must not be empty (the preview carries the same deploy request)")
	}
	// 段一：与 Deploy 同一入口执法（授权/四源互斥〔CEL 经内嵌单源继承〕/
	// upload 归属/变量 lookup/归一化/hook 闸门与 merge 裁决）。
	intake, err := svc.deployIntake(ctx, deployReq)
	if err != nil {
		return nil, err
	}
	// 段二 + 段三 + 受理位只读预检（引擎半边；不冻结不受理）。
	res, err := svc.s.Engine.PreviewSpec(ctx, engine.SubmitSpecRequest{
		AppID: intake.appRow.ID, Spec: intake.spec, SourceText: deployReq.GetComposeYaml(),
		HookOwned: intake.hookOwned, Merge: intake.merge,
	})
	if err != nil {
		return nil, mapStateError(err, "revision")
	}

	// spec_diff 基线：LatestSucceeded 的 to_revision 冻结体（空基线 = 首次
	// 部署全量 added 形态）。合成 Spec 的序列化 = PreviewSpec 的规范形
	// （marshalSpec 同参——diff 与 digest 同字节源，设计稿 §1.3 铁律）。
	baseBody, err := latestSucceededSpecBody(ctx, svc.s, intake.appRow.ID)
	if err != nil {
		return nil, err
	}
	out := &deliveryv1.PreviewDeployResponse{}
	out.SpecDiff, err = previewSpecDiff(baseBody, res.SpecBody)
	if err != nil {
		return nil, mapStateError(err, "revision diff")
	}

	// workload_changes：提案投影（strict；卷钉住只读并入——渲染同口径）
	// vs 当前生效渲染（RenderWorkloads 恒 isolate）。
	proposal := append([]capability.Workload(nil), res.Workloads...)
	if perr := svc.s.Engine.ApplyVolumePinning(ctx, proposal, intake.appRow.ProjectID); perr != nil {
		return nil, mapStateError(perr, "workloads")
	}
	baselineSpec, baselineWS, berr := previewBaseline(ctx, svc.s, intake.appRow.ID)
	if berr != nil {
		return nil, berr
	}
	changes := workloadChanges(proposal, baselineWS, res.Spec)
	out.WorkloadChanges = changes

	// warnings（结构化只增，序固定：kind 声明序、逐 workload 预警按
	// WorkloadChange 序——golden 钉死）。
	warnings, werr := svc.previewWarnings(ctx, deployReq, intake, res, proposal, baselineWS, baselineSpec, changes)
	if werr != nil {
		return nil, werr
	}
	out.Warnings = warnings

	// 铸票（设计稿 §1.4）：RequestDigest = ConfirmRequestDigest(合成 Spec
	// 规范形, 语义位)；Base 四锚（LatestFrozen/LatestSucceeded/VarEnv/
	// HookRowVersion——engine.ConfirmBaseSnapshot 与 admission 消费面同源
	// 采集）；PreviewDigest = 载荷核心摘要（规范形 sha256）；Operator 从
	// authn。
	sem := governance.ConfirmSemantics{Supersede: deployReq.GetSupersede(), OverrideHook: deployReq.GetOverrideHook()}
	base, err := svc.s.Engine.ConfirmBaseSnapshot(ctx, intake.appRow.ID)
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbDeploy,
		TargetApp:     intake.appRow.ID,
		RequestDigest: governance.ConfirmRequestDigest(res.SpecBody, sem),
		Semantics:     sem,
		Base:          base,
		PreviewDigest: sha256Hex(res.SpecBody),
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

// confirmOperator 从 authn 解析票面操作者烙印（票不得跨操作者——设计稿
// §1.4 ③；无身份 = fail-closed 空烙印，确认面 matchOperator 拒绝）。
func confirmOperator(ctx context.Context) governance.ConfirmOperator {
	if id, ok := authn.FromContext(ctx); ok {
		return governance.ConfirmOperator{TokenID: id.TokenID, UserID: id.UserID, TeamID: id.TeamID}
	}
	return governance.ConfirmOperator{}
}

// latestSucceededSpecBody 回读 LatestSucceeded 的冻结体字节（spec_diff 的
// base 侧）；无成功部署 = nil（空基线——首次部署全量 added）。
func latestSucceededSpecBody(ctx context.Context, s *Services, appID string) ([]byte, error) {
	d, err := s.Deployments.LatestSucceeded(ctx, appID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil
		}
		return nil, mapStateError(err, "deployment")
	}
	rev, err := s.Revisions.Get(ctx, d.ToRevision)
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	return rev.Spec, nil
}

// previewSpecDiff 对基线冻结体与合成 Spec 规范形做字段级 diff（两侧同
// marshalSpec 参数——基线体本就是同参冻结产物；diff 与 digest 同序列化，
// 设计稿 §1.3 铁律）。**脱敏红线在 diff 输入侧先行**：两侧 JSON 树的 env
// map 值面先替换为脱敏标记再 diff——键名（name 集）保留可见，值集零暴露
// （含空基线塌缩条目与子树 blob 形态——值不因路径形态而漏）；env **值**
// 变化因此不出条目（键增减照常 diff），这是「至多 name 集」的语义代价。
// 空基线（首次部署）对空 JSON 对象 diff——全量 added 以顶层子树条目展开。
func previewSpecDiff(baseBody, newBody []byte) ([]*deliveryv1.DiffEntry, error) {
	oldTree, newTree, err := redactedTrees(baseBody, newBody)
	if err != nil {
		return nil, err
	}
	entries, err := spec.Diff(oldTree, newTree)
	if err != nil {
		return nil, err
	}
	out := make([]*deliveryv1.DiffEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &deliveryv1.DiffEntry{
			Path: e.Path, OldValue: scalarJSON(e.Old), NewValue: scalarJSON(e.New),
		})
	}
	return out, nil
}

// redactedTrees 解析两侧规范形并做 env 值面脱敏，返回可 diff 的 JSON 字节
// （spec.Diff 接受 protojson 规范形；重序列化保持同构）。空基线归一为空
// JSON 对象（全量 added 叶面展开）。
func redactedTrees(baseBody, newBody []byte) ([]byte, []byte, error) {
	oldV, newV := any(nil), any(nil)
	var err error
	if len(baseBody) > 0 {
		if oldV, err = jsonUnmarshalTree(baseBody); err != nil {
			return nil, nil, fmt.Errorf("diff: parse old spec: %w", err)
		}
	} else {
		oldV = map[string]any{}
	}
	if newV, err = jsonUnmarshalTree(newBody); err != nil {
		return nil, nil, fmt.Errorf("diff: parse new spec: %w", err)
	}
	oldRed, err := jsonMarshalTree(redactEnvValues(oldV))
	if err != nil {
		return nil, nil, err
	}
	newRed, err := jsonMarshalTree(redactEnvValues(newV))
	if err != nil {
		return nil, nil, err
	}
	return oldRed, newRed, nil
}

// redactEnvValues 递归替换 JSON 树内 env map 的值面（键名保留）。
func redactEnvValues(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "env" {
				if env, ok := val.(map[string]any); ok {
					red := make(map[string]any, len(env))
					for name := range env {
						red[name] = redactedEnvValue
					}
					out[k] = red
					continue
				}
			}
			out[k] = redactEnvValues(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = redactEnvValues(item)
		}
		return out
	default:
		return v
	}
}

// previewBaseline 解析对照半边：当前生效渲染（RenderWorkloads 恒 isolate，
// 卷钉住只读并入）+ 冻结 Spec（VOLUME_PIN_MOVE 的声明侧 placement 对照）。
// 无成功部署 = 空基线（首次部署——不照抄渲染面的 ErrNotFound）。
func previewBaseline(ctx context.Context, s *Services, appID string) (*specv1.AppSpec, []capability.Workload, error) {
	rev, ws, err := s.Engine.RenderWorkloads(ctx, appID, 0)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil, nil
		}
		return nil, nil, mapStateError(err, "workloads")
	}
	frozen := &specv1.AppSpec{}
	if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(rev.Spec, frozen); uerr != nil {
		return nil, nil, mapStateError(uerr, "revision spec")
	}
	return frozen, ws, nil
}

// workloadChanges 装配逐 Workload 变更摘要 + 滚动后果判定（内容锚定等价
// 纯 spec 侧，设计稿 §1.3：未变载体 = ZERO_TOUCH、仅副本异 = SCALE_ONLY、
// 内容异 = REPLACE；ADDED = SCALE_ONLY〔新增载体零替换〕、REMOVED =
// REPLACE〔该进程载体将被拆除〕）。proposal 恒 strict、baseline 恒 isolate
// ——口径差是 fixture 断言面（本函数不抹平）。输出序：提案投影序，缺席
// 于提案的基线进程（REMOVED）按渲染序补尾——确定性（golden 钉死）。
func workloadChanges(proposal, baseline []capability.Workload, proposalSpec *specv1.AppSpec) []*deliveryv1.WorkloadChange {
	baseByID := make(map[string]capability.Workload, len(baseline))
	for _, w := range baseline {
		baseByID[w.ID] = w
	}
	propByID := make(map[string]capability.Workload, len(proposal))
	for _, w := range proposal {
		propByID[w.ID] = w
	}
	var out []*deliveryv1.WorkloadChange
	emit := func(w capability.Workload, action deliveryv1.WorkloadChangeAction, base *capability.Workload) {
		out = append(out, workloadChange(w, action, base, rolloutOf(action, w, base)))
	}
	for _, w := range proposal {
		base, ok := baseByID[w.ID]
		if !ok {
			emit(w, deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_ADDED, nil)
			continue
		}
		emit(w, deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_CHANGED, &base)
	}
	for _, w := range baseline {
		if _, ok := propByID[w.ID]; !ok {
			emit(w, deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_REMOVED, &w)
		}
	}
	return out
}

// rolloutOf 是滚动后果判定（纯 spec 侧，设计稿 §1.3——内容锚定等价红利：
// 操作者第一问「会不会动我在跑的容器」由它回答）：全内容等价（含副本）
// = ZERO_TOUCH；仅副本异 = SCALE_ONLY；内容异 = REPLACE（策略面经
// BLUE_GREEN_WINDOW warning 透出——WorkloadChange 无策略字段，「replace
// (策略)」的承载位）。ADDED = SCALE_ONLY（新增载体非替换——既有载体零
// 触碰）；REMOVED = REPLACE（该进程载体将被拆除，rollout 轴的破坏端）。
func rolloutOf(action deliveryv1.WorkloadChangeAction, prop capability.Workload, base *capability.Workload) deliveryv1.WorkloadRollout {
	switch action {
	case deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_ADDED:
		return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_SCALE_ONLY
	case deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_REMOVED:
		return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE
	default: // CHANGED
		if base == nil {
			return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE
		}
		contentEq := workloadContentEqual(prop, *base)
		switch {
		case contentEq && prop.Replicas == base.Replicas:
			return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_ZERO_TOUCH
		case contentEq:
			return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_SCALE_ONLY
		default:
			return deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE
		}
	}
}

// workloadContentEqual 比对两 Workload 的载体内容（副本数与部署代戳除外
// ——代戳是 materialize 段产物，预览恒零；副本异归 SCALE_ONLY 判定面）。
// 指针字段（Healthcheck/Resources/Materials）显式解引用判等：fmt "%+v"
// 对指针打印地址，带探针/资源上限的载体恒不等（恒 REPLACE——修复批 F3
// 审查探针实证）。nil↔非 nil 不等、双 nil 等、指向值深度比较（值内仅
// 标量/切片/映射，"%+v" 按内容打印——Go 1.12 起映射键序稳定）。
// capability.Workload 是内部 capability 结构非 proto 消息，无 proto.Equal
// 可用（核对：internal/domain/shared/capability/runtime.go）。
func workloadContentEqual(a, b capability.Workload) bool {
	ma, mb := a, b
	ma.Replicas, mb.Replicas = 0, 0
	ma.Generation, mb.Generation = 0, 0
	if !derefEqual(ma.Healthcheck, mb.Healthcheck) {
		return false
	}
	ma.Healthcheck, mb.Healthcheck = nil, nil
	if !derefEqual(ma.Resources, mb.Resources) {
		return false
	}
	ma.Resources, mb.Resources = nil, nil
	if !derefEqual(ma.Materials, mb.Materials) {
		return false
	}
	ma.Materials, mb.Materials = nil, nil
	return fmt.Sprintf("%+v", ma) == fmt.Sprintf("%+v", mb)
}

// derefEqual 深度比较两个同型指针指向的内容：nil↔非 nil 不等、双 nil 等，
// 双非 nil 按指向值比较（"%+v" 值打印——标量/切片/映射按内容，映射键序
// 稳定）。
func derefEqual[T any](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return fmt.Sprintf("%+v", *a) == fmt.Sprintf("%+v", *b)
}

// workloadChange 铸单条 Workload 摘要（变更后终态；REMOVED 回显被拆载体
// 的终态摘要——副本 to = 0）。
func workloadChange(w capability.Workload, action deliveryv1.WorkloadChangeAction, base *capability.Workload, rollout deliveryv1.WorkloadRollout) *deliveryv1.WorkloadChange {
	from, to := int64(0), w.Replicas
	if base != nil {
		from = base.Replicas
	}
	if action == deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_REMOVED {
		to = 0
	}
	out := &deliveryv1.WorkloadChange{
		WorkloadId: w.ID, Action: action, Image: w.Image, Command: w.Command,
		ReplicasFrom: from, ReplicasTo: to,
		Healthcheck: healthcheckSummary(w.Healthcheck),
		Rollout:     rollout,
	}
	for _, v := range w.Volumes {
		out.Volumes = append(out.Volumes, v.VolumeID)
	}
	out.Networks = append(out.Networks, w.Networks...)
	return out
}

// healthcheckSummary 渲染探针摘要（简述形态；全解析探针走
// GetAppWorkloads 深链）。
func healthcheckSummary(hc *capability.Healthcheck) string {
	if hc == nil {
		return ""
	}
	switch {
	case hc.HTTPPath != "":
		return fmt.Sprintf("http %s:%d", hc.HTTPPath, hc.HTTPPort)
	case hc.TCPPort != 0:
		return fmt.Sprintf("tcp %d", hc.TCPPort)
	case len(hc.Exec) > 0:
		return "exec " + strings.Join(hc.Exec, " ")
	default:
		return "declared"
	}
}

// previewWarnings 装配结构化预警（设计稿 §1.3 全集）。事实来源：
//   - REPLICAS_CHANGE / BLUE_GREEN_WINDOW / VOLUME_PIN_MOVE：提案投影 vs
//     当前生效渲染（workload_changes 同一对照面；声明侧 placement 对照
//     基线冻结 Spec）；
//   - HOOK_OWNERSHIP_MERGE：段一 merge 裁决；
//   - PRE_DEPLOY_JOBS_PRESENT：合成 Spec；
//   - DIGEST_PENDING：段三 digest-pending 进程集（from_build 源）；
//   - ADMISSION_PRECHECK：CheckPeerRefsSpec/CheckPreDeployNetworksSpec（与
//     Submit 单源谓词——入队即拒项预览期透出）+ 队列容量；
//   - PROJECTION_DEGRADED：引擎 StrictDegraded 旗标（strict 投影因未批准
//     peer 退化为 isolate 视图，实际部署将拒绝——修复批 F4 上 wire）；
//   - IN_FLIGHT_DEPLOYMENT：admission 队列态（ActiveByApp——在途/排队
//     计数与本次预期 outcome/位次）；
//   - LIVE_DRIFT：Engine.ObservationSnapshot 活观测 vs 提案声明副本（修复
//     批 F4 换源：InspectAppWorkloads 是 spec 对照面——swarm 读 service
//     spec 副本/k3s 读 deployment spec，Replicas 取期望，「真实在跑数<声明」
//     的漂移在彼面静默；活观测槽是逐 task 聚合的 last-write-wins 事实，
//     非全 running 即真漂移。观测面缺席静默跳过——诚实边界不变）。
func (svc *DeploymentsService) previewWarnings(ctx context.Context, deployReq *deliveryv1.DeployRequest, intake *deployIntakeResult, res *engine.PreviewSpecResult, proposal, baseline []capability.Workload, baselineSpec *specv1.AppSpec, changes []*deliveryv1.WorkloadChange) ([]*deliveryv1.PreviewWarning, error) {
	var out []*deliveryv1.PreviewWarning
	add := func(kind deliveryv1.PreviewWarningKind, format string, args ...any) {
		out = append(out, &deliveryv1.PreviewWarning{Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	baseByID := make(map[string]capability.Workload, len(baseline))
	for _, w := range baseline {
		baseByID[w.ID] = w
	}
	propSpecPlacement := placementByProcess(res.Spec)
	baseSpecPlacement := placementByProcess(baselineSpec)
	for _, ch := range changes {
		process := processOfID(ch.WorkloadId)
		if ch.Action == deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_CHANGED && ch.ReplicasFrom != ch.ReplicasTo {
			add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_REPLICAS_CHANGE,
				"process %q changes replicas %d -> %d", process, ch.ReplicasFrom, ch.ReplicasTo)
		}
		if ch.Rollout == deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE &&
			strategyOf(res.Spec, process) == specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN {
			add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_BLUE_GREEN_WINDOW,
				"process %q replaces carriers under the blue-green strategy: two generations coexist until the new generation passes its health gate", process)
		}
		// 卷钉住变更：post-pin placement 漂移而声明侧 placement 未动——
		// 钉住事实（卷行 PinnedNodeID）变了，placement 约束随钉住漂移。
		if ch.Action == deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_CHANGED {
			if base, ok := baseByID[ch.WorkloadId]; ok {
				if prop := findByID(proposal, ch.WorkloadId); prop != nil &&
					!nodeIDsEqual(prop.Placement.NodeIDs, base.Placement.NodeIDs) &&
					nodeIDsEqual(propSpecPlacement[prop.Process], baseSpecPlacement[base.Process]) {
					add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_VOLUME_PIN_MOVE,
						"process %q placement constraints moved with a volume pin change (declared placement unchanged)", prop.Process)
				}
			}
		}
	}
	// 合并/作业面。
	if intake.merge != engine.MergeNone {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_HOOK_OWNERSHIP_MERGE,
			"this deploy overlays the runtime half onto the hook-owned frozen material; material changes still go through the git hook")
	}
	if jobs := res.Spec.GetPreDeployJobs(); len(jobs) > 0 {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT,
			"%d pre-deploy job(s) will run before the release phase", len(jobs))
	}
	for _, name := range res.PendingFromBuild {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_DIGEST_PENDING,
			"process %q deploys from_build: the image reference settles after the build completes", name)
	}
	// 受理位只读预检（与 Submit 单源谓词）。
	if res.PeerErr != nil {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_ADMISSION_PRECHECK, "%s", res.PeerErr.Error())
	}
	if res.PreDeployNetworksErr != nil {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_ADMISSION_PRECHECK, "%s", res.PreDeployNetworksErr.Error())
	}
	// 严格投影退化上 wire（修复批 F4）：strict 投影因未批准 peer 引用失败、
	// workloads 已退 isolate 重投影（对照半边 RenderWorkloads 同口径——
	// workload 面仍可展示）；实际部署走 strict 口径将拒绝，直到 peer 获批。
	if res.StrictDegraded {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_PROJECTION_DEGRADED,
			"the strict projection degraded to an isolate view because a peer reference is not approved; the workload projection shown here omits it, and the actual deploy will be rejected until the peer is approved")
	}
	// 队列容量 + 在途/排队预警（admission 队列态）。
	active, err := svc.s.Deployments.ActiveByApp(ctx, intake.appRow.ID)
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	queued, inFlight := 0, 0
	for i := range active {
		if active[i].State == deployment.StateQueued {
			queued++
		} else {
			inFlight++
		}
	}
	if res.QueueCapacity > 0 && queued >= res.QueueCapacity {
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_ADMISSION_PRECHECK,
			"the deployment queue for this app is full (%d queued, capacity %d); the deploy will be rejected until a slot frees", queued, res.QueueCapacity)
	}
	if len(active) > 0 {
		outcome, position := "queued", inFlight+1
		if deployReq.GetSupersede() && inFlight > 0 {
			outcome, position = "superseded", 1
		} else if queued > 0 {
			outcome = "merged"
		}
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_IN_FLIGHT_DEPLOYMENT,
			"%d in-flight and %d queued deployment(s) exist; their completion will stale this ticket; this deploy would be %s at position %d",
			inFlight, queued, outcome, position)
	}
	// 现场漂移（活观测 vs 提案声明副本；观测面缺席静默跳过）。数据源 =
	// ObservationSnapshot 活观测槽（swarm 侧逐 task 聚合：子集全 running 才
	// running，任一非 running 按最重态上报，消息带 n/m 计数）——「真实在跑
	// 数<声明」在此面可见。pending 不计（滚动未就绪不是漂移，
	// IN_FLIGHT_DEPLOYMENT 已预警）；completed 是 one-shot 正常终态（非长
	// 运行漂移面）；声明 replicas=0 的 stopped 是声明态（诊断 Advice 档
	// 承载），不按漂移读。
	snap := svc.s.Engine.ObservationSnapshot(ctx, intake.appRow.ID)
	obsByID := make(map[string]capability.WorkloadEvent, len(snap))
	for _, o := range snap {
		obsByID[o.WorkloadID] = o
	}
	for _, w := range proposal {
		o, ok := obsByID[w.ID]
		if !ok || w.Replicas <= 0 || !liveDriftState(o.State) {
			continue
		}
		detail := o.Message
		if detail == "" {
			detail = "no runtime detail"
		}
		add(deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_LIVE_DRIFT,
			"live observation for process %q reports %s (%s): fewer than the declared %d replica(s) are running; this deploy will converge to the declared state — declare the replicas you want to keep (replica-only changes roll out as scale_only, zero replacement)",
			w.Process, o.State, detail, w.Replicas)
	}
	return out, nil
}

// liveDriftState 判定观测态是否构成现场漂移（非 running 的活事实即真实
// 在跑数低于声明的信号）：degraded（副本部分失联/重启循环）/stopped/
// failed/orphaned 计入；pending 是滚动未就绪（非漂移）、completed 是
// one-shot 正常终态（长运行 Workload 的漂移面不含它）。
func liveDriftState(s capability.WorkloadState) bool {
	switch s {
	case capability.WorkloadDegraded, capability.WorkloadStopped,
		capability.WorkloadFailed, capability.WorkloadOrphaned:
		return true
	}
	return false
}

// strategyOf 取进程的部署策略（spec 进程声明）。
func strategyOf(sp *specv1.AppSpec, process string) specv1.DeployStrategy {
	if sp == nil {
		return specv1.DeployStrategy_DEPLOY_STRATEGY_UNSPECIFIED
	}
	for _, p := range sp.GetProcesses() {
		if p.GetName() == process {
			return p.GetStrategy()
		}
	}
	return specv1.DeployStrategy_DEPLOY_STRATEGY_UNSPECIFIED
}

// placementByProcess 抽 Spec 的声明侧 placement（process → NodeIDs）。
func placementByProcess(sp *specv1.AppSpec) map[string][]string {
	out := map[string][]string{}
	if sp == nil {
		return out
	}
	for _, p := range sp.GetProcesses() {
		out[p.GetName()] = append([]string(nil), p.GetPlacement().GetNodeIds()...)
	}
	return out
}

// nodeIDsEqual 比对两组节点 ID（集合语义——钉住追加的序不作语义）。
func nodeIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// processOfID 从 WorkloadID 取进程名（WorkloadID = appID-process，投影
// 单源公式 engine.WorkloadID——取尾段；app ULID 无连字符，尾段即进程名）。
func processOfID(workloadID string) string {
	if i := strings.LastIndex(workloadID, "-"); i >= 0 {
		return workloadID[i+1:]
	}
	return workloadID
}

// findByID 在 Workload 集内按 ID 定位。
func findByID(ws []capability.Workload, id string) *capability.Workload {
	for i := range ws {
		if ws[i].ID == id {
			return &ws[i]
		}
	}
	return nil
}

// sha256Hex 是预览载荷摘要的确定性序列化（分节 → sha256 hex）。
func sha256Hex(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
