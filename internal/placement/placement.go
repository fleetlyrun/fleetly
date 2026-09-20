// Package placement 是放置解析与绑定生命周期（stateful-placement 专项的
// 概念模型三层；v0.2 E1-7 起多节点化——multi-node §2.6/D-MN-7）：
//
//   - 概念模型三层（§2.1）：意图 = 服务 label fleetly.placement.node；
//     绑定 = placements 记录（平台节点 ID 为锚）；执行 = 适配器编译为
//     节点 label 约束（本包 ConstraintFor）。
//   - 不变量（§2.1）：绑定优先于 label 的缺失；有卷应用不存在「无绑定」
//     的合法运行态；绑定节点不可用时不迁移、不换点。
//   - 多节点切面（multi-node §2.6）：候选集 = 底座直读全量节点快照
//     （D-MN-7：决策路径禁读观测缓存）；label 值域 = 唯一显示名或平台 ID
//     （歧义 → INVALID + 提示平台 ID）；自动选点三因子 = 数据引力 >
//     已钉数少 > 平台 ID 字典序。v0.1 的单机守卫（GuardMultiNode/
//     MultiNodeUnsupported）退役：候选集唯一（本机）时自动绑定行为与
//     v0.1 等价——E_CAPABILITY_REQUIRES_MULTI_NODE 码保留注册表、永不复用。
//
// 本包零框架依赖；底座访问经 state.DockerClient 端口（写前直读纪律：
// 决策路径直读底座，禁读观测缓存，state-model §2.2）。
package placement

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// Resolver 是放置解析器。
type Resolver struct {
	store  *state.Store
	docker state.DockerClient
}

// NewResolver 构造解析器。
func NewResolver(store *state.Store, docker state.DockerClient) *Resolver {
	return &Resolver{store: store, docker: docker}
}

// VolumeMount 是一次命名卷挂载声明（归一化 compose 的 volumes 子集）。
type VolumeMount struct {
	Key      string // 顶层卷 key（compose 侧标识）
	Target   string // 容器内挂载点（volumes.mount_path）
	ReadOnly bool
}

// Input 是一次放置解析输入。
type Input struct {
	// AppID/AppName 是应用平台 ID（appid8/绑定锚）与 compose 名（卷命名）。
	AppID   string
	AppName string
	// Volumes 是应用整体声明的命名卷挂载（空 = 无卷应用 → 默认不钉）。
	Volumes []VolumeMount
	// LabelRef 是 fleetly.placement.node 字面值（空 = 未声明）。
	LabelRef string
}

// Decision 是解析结果（意图 → 绑定 → 执行的裁决书）。
type Decision struct {
	AppID string
	// Bind 报告是否应当存在绑定（有卷应用恒真；无卷应用显式 pin 也不钉）。
	Bind bool
	// PlatformNodeID 是绑定锚（平台节点 ID n_<ULID>；Bind=false 时空）。
	PlatformNodeID string
	// Source 是绑定来源（platform=自动选点 / label=显式钉住解析成功）。
	Source state.PlacementSource
	// LabelRef 保留用户书写原值（可解释性；空 = 未声明）。
	LabelRef string
	// KeptExisting 报告既有绑定被保持（绑定优先于 label 的缺失）。
	KeptExisting bool
	// Constraint 是有卷服务的约束编译结果
	//（`node.labels.fleetly.node-id == <平台ID>`；未钉时空）。
	Constraint string
	// Warnings 是计划级非阻断标注（W_PLACEMENT_STATELESS_PIN）。
	Warnings []compose.Warning
}

// candidate 是一个候选节点（底座直读快照项，multi-node §2.6）。
type candidate struct {
	platformID   string // fleetly.node-id label 值；未锚定为空
	swarmNodeID  string
	hostname     string
	state        string
	availability string
}

// ready 是底座 ready 语义：state=ready 且 availability=active（drain/pause
// 均不可部署——放置专项 §2.6 drain 同 DOWN）。
func (c candidate) ready() bool {
	return c.state == "ready" && c.availability == "active"
}

// describe 是候选项的人读形态（候选清单：名 + 平台 ID）。
func (c candidate) describe() string {
	return c.hostname + " (" + c.platformID + ")"
}

// Resolve 解析放置（不落库）：候选集 = 底座直读全量节点快照（D-MN-7）。
//
//	label pin → 在候选集中解析（唯一显示名或平台 ID）；失败 →
//	  E_PLACEMENT_NODE_NOT_FOUND（422 + 全量候选清单）
//	已有绑定 → 保持（绑定优先于 label 的缺失）
//	无绑定且无卷 → 不钉（自由调度）；显式 pin → W_PLACEMENT_STATELESS_PIN
//	无绑定且有卷 → 自动选点：候选 = 已锚定 + ready + active；
//	  评分 = 数据引力（卷注册表所在节点）> 已钉应用数少（placements 权威
//	  计数）> 平台 ID 字典序（multi-node §2.6/D-MN-7 三因子）
//	无候选 → E_PLACEMENT_NO_ELIGIBLE_NODE
//
// 候选集唯一 = 本机时行为与 v0.1 单机切面等价（同码路径、同结果）。
func (r *Resolver) Resolve(ctx context.Context, in Input) (Decision, error) {
	cands, err := r.candidates(ctx)
	if err != nil {
		return Decision{}, err
	}

	// label 意图先解析（只在显式声明时参与——绑定优先于 label 的缺失）。
	if err := validateLabelRef(in.LabelRef); err != nil {
		return Decision{}, err
	}
	pinned, err := r.resolveRef(ctx, cands, in.LabelRef)
	if err != nil {
		return Decision{}, err
	}

	// 已有绑定 → 保持（label 只作展示，不触发迁移——跨点唯一路径 = 显式
	// 换点 Rebind，multi-node §2.6）。
	existing, err := r.store.GetPlacement(ctx, in.AppID)
	switch {
	case err == nil:
		d := Decision{
			AppID:          in.AppID,
			Bind:           true,
			PlatformNodeID: existing.PlatformNodeID,
			Source:         existing.Source,
			LabelRef:       in.LabelRef,
			KeptExisting:   true,
		}
		if len(in.Volumes) > 0 {
			d.Constraint = ConstraintFor(existing.PlatformNodeID)
		}
		return d, nil
	case errors.Is(err, state.ErrPlacementNotFound):
		// 首次解析，继续。
	default:
		return Decision{}, fmt.Errorf("placement: read placement: %w", err)
	}

	// 无绑定且无卷 → 不钉（自由调度）；显式 pin 出计划警告（放置专项 §2.3）。
	if len(in.Volumes) == 0 {
		d := Decision{AppID: in.AppID, LabelRef: in.LabelRef}
		if in.LabelRef != "" {
			d.Warnings = append(d.Warnings, compose.Warning{
				Code:    "W_PLACEMENT_STATELESS_PIN",
				Service: in.AppName,
				Message: "service " + in.AppName + " explicitly pins node " + in.LabelRef +
					" (app has no named volumes and will lose automatic rescheduling; the platform does not migrate on node failure)",
			})
		}
		return d, nil
	}

	// 无绑定且有卷 → 自动选点（三因子，multi-node §2.6）。
	return r.autoPick(ctx, cands, pinned, in)
}

// autoPick 执行绑定选点：候选池 = 已锚定 + ready + active。显式 pin 已
// 解析成功时以 pin 目标为准（目标不在池内 = 节点未就绪/未锚定 →
// E_PLACEMENT_NO_ELIGIBLE_NODE，v0.1 同码同语义）；自动选点走三因子：
// 数据引力（卷注册表所在节点）> 已钉应用数少（placements 权威计数）>
// 平台 ID 字典序（multi-node §2.6/D-MN-7）。
func (r *Resolver) autoPick(ctx context.Context, cands []candidate, pinned *candidate, in Input) (Decision, error) {
	pool := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if c.platformID != "" && c.ready() {
			pool = append(pool, c)
		}
	}
	if pinned != nil {
		// 显式 pin：目标必须在合格池内（v0.1 语义平移——非 ready 不绑定）。
		for _, c := range pool {
			if c.platformID == pinned.platformID {
				return Decision{
					AppID:          in.AppID,
					Bind:           true,
					PlatformNodeID: c.platformID,
					Source:         state.PlacementSourceLabel,
					LabelRef:       in.LabelRef,
					Constraint:     ConstraintFor(c.platformID),
				}, nil
			}
		}
		return Decision{}, apperr.New("E_PLACEMENT_NO_ELIGIBLE_NODE",
			"pinned node %s is not eligible for placement (anchored+ready+active required; state=%s availability=%s)",
			pinned.describe(), pinned.state, pinned.availability).
			WithContext("candidates", describeCandidates(cands))
	}
	if len(pool) == 0 {
		return Decision{}, apperr.New("E_PLACEMENT_NO_ELIGIBLE_NODE",
			"no candidate for automatic placement (anchored+ready+active set is empty; direct snapshot has %d node(s))", len(cands)).
			WithContext("candidates", describeCandidates(cands))
	}

	// 数据引力：应用卷注册表（权威 SQLite）所在节点优先（§2.5 数据引力）。
	gravity := map[string]bool{}
	if vols, err := r.store.ListAppVolumes(ctx, in.AppID); err != nil {
		return Decision{}, fmt.Errorf("placement: read volume registry: %w", err)
	} else {
		for _, v := range vols {
			if v.Status == state.VolumeActive && v.PlatformNodeID != "" {
				gravity[v.PlatformNodeID] = true
			}
		}
	}
	// 已钉数：placements 权威表计数（非观测缓存）。
	pinnedCount, err := r.store.PlacementCountByNode(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("placement: read pinned counts: %w", err)
	}
	// 三因子排序（稳定：同分节点由平台 ID 字典序定序，可解释可测试）。
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
	return Decision{
		AppID:          in.AppID,
		Bind:           true,
		PlatformNodeID: chosen.platformID,
		Source:         state.PlacementSourcePlatform,
		LabelRef:       in.LabelRef,
		Constraint:     ConstraintFor(chosen.platformID),
	}, nil
}

// LabelInput 是 Resolve 输入的别名（测试/文档可读性）。
type LabelInput = Input

// Apply = Resolve + 落库：绑定 upsert（首次钉住）+ 卷注册表登记 + 审计与
// 事件同事务（placement.bound / volume.created；系统自动动作必入审计，
// state-model §2.9）。既有绑定保持时不重写（幂等）；卷登记始终执行
// （新卷 = 数据诞生点钉住，§2.6 漂移矩阵）。
func (r *Resolver) Apply(ctx context.Context, in Input) (Decision, error) {
	d, err := r.Resolve(ctx, in)
	if err != nil {
		return Decision{}, err
	}

	if d.Bind && !d.KeptExisting {
		// 绑定写 + placement.bound 事件 + 审计同一事务（M1-12 修复：事件
		// 此前只在注释与注册表里承诺、从未发出——stateful-placement §2.8
		// 的「绑定 = 系统自动动作必入审计与事件」两侧齐备）。
		var bound state.Placement
		err := r.store.InTx(ctx, func(tx *state.Tx) error {
			p, err := tx.BindPlacement(ctx, state.PlacementWrite{
				AppID:          d.AppID,
				PlatformNodeID: d.PlatformNodeID,
				Source:         d.Source,
				LabelRef:       d.LabelRef,
				State:          state.PlacementBound,
				Pinned:         true,
			})
			if err != nil {
				return err
			}
			bound = p
			if _, err := tx.AppendEvent(ctx, state.Event{
				Name:    "placement.bound",
				Subject: "app:" + d.AppID,
				Payload: `{"app_id":"` + d.AppID + `","node":"` + p.PlatformNodeID +
					`","source":"` + string(p.Source) + `"}`,
			}); err != nil {
				return err
			}
			return tx.WriteAudit(ctx, state.AuditEntry{
				Actor:       "system",
				Action:      "placement.bound",
				Target:      "app:" + d.AppID,
				Result:      "ok",
				DiffSummary: state.DiffSummary("node", p.PlatformNodeID, "source", string(p.Source)), // MG-6：构造器替换手拼 JSON
			})
		})
		if err != nil {
			return Decision{}, fmt.Errorf("placement: persist binding: %w", err)
		}
		_ = bound // 节点/来源仅供同事务载荷；裁决书以 Decision 返回
	}

	// 卷注册表登记（docker_name = 命名约定值，state-model §2.4）；数据
	// 诞生点 = 绑定节点（多节点下可能是任一候选，multi-node §2.6）。
	if err := r.registerVolumes(ctx, in, d.PlatformNodeID); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// registerVolumes 把应用声明的命名卷登记进卷注册表；新卷发 volume.created
// 事件（数据诞生点）。platform 节点 = 当前绑定锚（多节点语义）。
func (r *Resolver) registerVolumes(ctx context.Context, in Input, platformNodeID string) error {
	if len(in.Volumes) == 0 {
		return nil
	}
	for _, m := range sortedMounts(in.Volumes) {
		dockerName, err := naming.VolumeName(in.AppName, m.Key, in.AppID)
		if err != nil {
			return fmt.Errorf("placement: volume name: %w", err)
		}
		_, isNew, err := r.store.RegisterAppVolume(ctx, state.VolumeWrite{
			AppID:          in.AppID,
			Key:            m.Key,
			Name:           dockerName,
			Kind:           state.VolumeKindNamed,
			PlatformNodeID: platformNodeID,
			MountPath:      m.Target,
		})
		if err != nil {
			return fmt.Errorf("placement: register volume %s: %w", m.Key, err)
		}
		if isNew {
			if err := r.store.InTx(ctx, func(tx *state.Tx) error {
				_, err := tx.AppendEvent(ctx, state.Event{
					Name:    "volume.created",
					Subject: "volume:" + dockerName,
					Payload: `{"app":"` + in.AppID + `","key":"` + m.Key + `","node":"` + platformNodeID + `"}`,
				})
				return err
			}); err != nil {
				return fmt.Errorf("placement: event volume.created: %w", err)
			}
		}
	}
	return nil
}

// sortedMounts 返回按 key 字典序的挂载（登记确定性）。
func sortedMounts(in []VolumeMount) []VolumeMount {
	out := make([]VolumeMount, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// candidates 直读全量候选节点（决策路径禁读观测缓存，state-model §2.2；
// multi-node §2.6/D-MN-7）：每项 = swarm node ID、平台 ID（fleetly.node-id
// label，未锚定为空）、hostname、ready 性。空快照返回空集非错误——无候选
// 的裁决由各调用点给出契约错误。
func (r *Resolver) candidates(ctx context.Context) ([]candidate, error) {
	nodes, err := r.docker.ListNodeObservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("placement: list nodes: %w", err)
	}
	out := make([]candidate, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, candidate{
			platformID:   n.Labels[state.LabelNodeID],
			swarmNodeID:  n.SwarmNodeID,
			hostname:     n.Hostname,
			state:        n.State,
			availability: n.Availability,
		})
	}
	return out, nil
}

// resolveRef 把 label 原值解析为候选（multi-node §2.6 label 值域）：
// 平台 ID（n_<ULID>）、唯一显示名（集群内必须唯一——同名即歧义 →
// E_PLACEMENT_NODE_INVALID 422 + 提示改用平台 ID）、底座节点 ID 形态
// 宽松接受（无害，v0.1 行为平移）。空值 = 未声明，返回 nil 非 error。
func (r *Resolver) resolveRef(_ context.Context, cands []candidate, ref string) (*candidate, error) {
	if ref == "" {
		return nil, nil
	}
	var hits []candidate
	for _, c := range cands {
		if ref == c.platformID || ref == c.hostname || ref == c.swarmNodeID {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return &hits[0], nil
	case 0:
		return nil, apperr.New("E_PLACEMENT_NODE_NOT_FOUND",
			"placement label %q does not match any node (candidates: %s)", ref, describeCandidates(cands)).
			WithContext("label_ref", ref).
			WithContext("candidates", describeCandidates(cands))
	default:
		return nil, apperr.New("E_PLACEMENT_NODE_INVALID",
			"placement label %q is ambiguous: %d nodes share this display name — use the platform node ID instead (candidates: %s)",
			ref, len(hits), describeCandidates(hits)).
			WithContext("label_ref", ref).
			WithContext("candidates", describeCandidates(hits))
	}
}

// describeCandidates 是候选清单的人读形态（错误信息的可行动面）。
func describeCandidates(cands []candidate) string {
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		parts = append(parts, c.describe())
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// validateLabelRef 拒绝形态非法的 label 值（空串视为未声明；含空白或越界
// 字符 → E_PLACEMENT_NODE_INVALID 422）。
func validateLabelRef(ref string) error {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return nil
	}
	if trimmed != ref {
		return apperr.New("E_PLACEMENT_NODE_INVALID",
			"placement label %q has leading or trailing whitespace (writable node display name or platform node ID)", ref).
			WithContext("label_ref", ref)
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return apperr.New("E_PLACEMENT_NODE_INVALID",
				"placement label %q contains invalid character %q (writable node display name or platform node ID)", ref, string(r)).
				WithContext("label_ref", ref)
		}
	}
	return nil
}

// ConstraintFor 编译有卷服务的节点约束（执行层 = 适配器把绑定翻译为节点
// label 约束，stateful-placement §2.1）：约束引用节点身份 label
// fleetly.node-id = 平台节点 ID。
func ConstraintFor(platformNodeID string) string {
	return "node.labels." + state.LabelNodeID + " == " + platformNodeID
}
