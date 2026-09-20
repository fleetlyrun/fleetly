// Package placement 是放置解析与绑定生命周期（stateful-placement 专项的
// v0.1 单机切面，T2.14）：
//
//   - 概念模型三层（§2.1）：意图 = 服务 label fleetly.placement.node；
//     绑定 = placements 记录（平台节点 ID 为锚）；执行 = 适配器编译为
//     节点 label 约束（本包 ConstraintFor）。
//   - 不变量（§2.1）：绑定优先于 label 的缺失；有卷应用不存在「无绑定」
//     的合法运行态；绑定节点不可用时不迁移、不换点。
//   - 单机切面（§2.9）：同一字段、同一代码路径，候选集只有一项（本机）；
//     多节点操作一律 E_CAPABILITY_REQUIRES_MULTI_NODE，不静默成功。
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

// Resolver 是放置解析器（单机同路径）。
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

// selfNode 是单机候选（本机）的直读快照。
type selfNode struct {
	platformID   string
	swarmNodeID  string
	hostname     string
	ready        bool
	state        string
	availability string
}

// Resolve 解析放置（不落库）：单机同路径，候选集 = 本机（§2.9）。
//
//	已有绑定 → 保持（绑定优先）
//	无绑定且无卷 → 不钉（自由调度）；显式 pin 出 W_PLACEMENT_STATELESS_PIN
//	无绑定且有卷 → 自动绑定本机（label 引导时来源记 label）
//	label 解析失败 → E_PLACEMENT_NODE_NOT_FOUND（422 + 候选清单）/ INVALID
//	拓扑非单节点 → E_CAPABILITY_REQUIRES_MULTI_NODE（v0.1 守卫，不静默）
func (r *Resolver) Resolve(ctx context.Context, in Input) (Decision, error) {
	self, err := r.self(ctx)
	if err != nil {
		return Decision{}, err
	}

	// label 意图先解析（只在显式声明时参与——绑定优先于 label 的缺失）。
	if err := validateLabelRef(in.LabelRef); err != nil {
		return Decision{}, err
	}
	if in.LabelRef != "" && !labelRefersTo(self, in.LabelRef) {
		// 名→ID 解析失败：422 + 候选清单（stateful-placement §2.2）。
		return Decision{}, apperr.New("E_PLACEMENT_NODE_NOT_FOUND",
			"放置 label %q 不匹配任何节点（v0.1 单机候选：%s）", in.LabelRef, self.describe()).
			WithContext("label_ref", in.LabelRef).
			WithContext("candidates", self.describe())
	}

	// 已有绑定 → 保持（label 只作展示，不触发迁移——跨点确认属 v0.2）。
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

	// 无绑定且无卷 → 不钉（自由调度）；显式 pin 出计划警告（§2.3）。
	if len(in.Volumes) == 0 {
		d := Decision{AppID: in.AppID, LabelRef: in.LabelRef}
		if in.LabelRef != "" {
			d.Warnings = append(d.Warnings, compose.Warning{
				Code:    "W_PLACEMENT_STATELESS_PIN",
				Service: in.AppName,
				Message: "服务 " + in.AppName + " 显式钉住节点 " + in.LabelRef +
					"（应用无命名卷，将失去自动重调度；节点故障时平台不迁移）",
			})
		}
		return d, nil
	}

	// 无绑定且有卷 → 自动绑定本机（强制钉住，平台自动无需声明；§2.3）。
	// 候选 = 节点 ready（§2.5）：本机非 ready 无候选。
	if !self.ready {
		return Decision{}, apperr.New("E_PLACEMENT_NO_ELIGIBLE_NODE",
			"自动选点无候选（v0.1 单机，本机 %s 非 ready：state=%s availability=%s）",
			self.hostname, self.state, self.availability).
			WithContext("node", self.describe())
	}
	source := state.PlacementSourcePlatform
	if in.LabelRef != "" {
		source = state.PlacementSourceLabel
	}
	return Decision{
		AppID:          in.AppID,
		Bind:           true,
		PlatformNodeID: self.platformID,
		Source:         source,
		LabelRef:       in.LabelRef,
		Constraint:     ConstraintFor(self.platformID),
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

	// 卷注册表登记（docker_name = 命名约定值，state-model §2.4）。
	if err := r.registerVolumes(ctx, in); err != nil {
		return Decision{}, err
	}
	return d, nil
}

// registerVolumes 把应用声明的命名卷登记进卷注册表；新卷发 volume.created
// 事件（数据诞生点）。platform 节点 = 本机（单机切面）。
func (r *Resolver) registerVolumes(ctx context.Context, in Input) error {
	if len(in.Volumes) == 0 {
		return nil
	}
	self, err := r.self(ctx)
	if err != nil {
		return err
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
			PlatformNodeID: self.platformID,
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
					Payload: `{"app":"` + in.AppID + `","key":"` + m.Key + `","node":"` + self.platformID + `"}`,
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

// self 直读本机候选（决策路径禁读观测缓存，state-model §2.2）：Swarm 自省
// → 平台 ID（meta）→ 全量节点快照取 hostname/ready。拓扑非单节点即守卫。
func (r *Resolver) self(ctx context.Context) (selfNode, error) {
	platformID, err := r.store.GetMeta(ctx, state.MetaKeyPlatformNodeID)
	if err != nil {
		return selfNode{}, fmt.Errorf("placement: read platform node id: %w", err)
	}
	if platformID == "" {
		return selfNode{}, errors.New("placement: platform node id not ensured (fleetlyd identity missing)")
	}
	swarmNodeID, err := r.docker.SelfNodeID(ctx)
	if err != nil {
		return selfNode{}, fmt.Errorf("placement: self node id: %w", err)
	}
	nodes, err := r.docker.ListNodeObservations(ctx)
	if err != nil {
		return selfNode{}, fmt.Errorf("placement: list nodes: %w", err)
	}
	if err := GuardMultiNode(len(nodes)); err != nil {
		return selfNode{}, err
	}
	if len(nodes) == 0 {
		return selfNode{}, apperr.New("E_PLACEMENT_NO_ELIGIBLE_NODE",
			"自动选点无候选（直读底座节点快照为空）")
	}
	n := nodes[0]
	if n.SwarmNodeID != swarmNodeID {
		return selfNode{}, fmt.Errorf("placement: self node %s not in direct snapshot", swarmNodeID)
	}
	return selfNode{
		platformID:   platformID,
		swarmNodeID:  swarmNodeID,
		hostname:     n.Hostname,
		ready:        n.State == "ready" && n.Availability == "active",
		state:        n.State,
		availability: n.Availability,
	}, nil
}

// describe 是候选项的人读形态（候选清单：名 + 平台 ID）。
func (s selfNode) describe() string {
	return s.hostname + " (" + s.platformID + ")"
}

// labelRefersTo 报告 label 原值是否指向候选节点（平台 ID / 显示名 / 底座
// 节点 ID 均可——§2.2 显示名仅供读写，解析在适配器层收敛到平台 ID）。
func labelRefersTo(s selfNode, ref string) bool {
	return ref == s.platformID || ref == s.hostname || ref == s.swarmNodeID
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
			"放置 label %q 含首尾空白（可写节点显示名或平台节点 ID）", ref).
			WithContext("label_ref", ref)
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return apperr.New("E_PLACEMENT_NODE_INVALID",
				"放置 label %q 含非法字符 %q（可写节点显示名或平台节点 ID）", ref, string(r)).
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

// GuardMultiNode 是多节点操作守卫：节点数 > 1 即 v0.1 不支持（不静默成功，
// §2.9 多节点操作返回 E_CAPABILITY_REQUIRES_MULTI_NODE）。
func GuardMultiNode(nodeCount int) error {
	if nodeCount > 1 {
		return apperr.New("E_CAPABILITY_REQUIRES_MULTI_NODE",
			"检测到 %d 个节点：该操作在 v0.1 单节点拓扑上不可用（多节点能力随 v0.2）", nodeCount)
	}
	return nil
}

// MultiNodeUnsupported 是「结构性多节点操作」的静态守卫（换点 API、选点
// 第二候选等——单机实现直接拒绝）。
func MultiNodeUnsupported(op string) error {
	return apperr.New("E_CAPABILITY_REQUIRES_MULTI_NODE",
		"%s 需要多节点拓扑：v0.1 为单节点，多节点能力随 v0.2 提供", op)
}

// MoveBinding 是显式换点（破坏性确认路径，§2.1 绑定变更四类操作之一）。
// v0.1 单机无第二候选：守卫拒绝，不静默、不改状态（rebind CLI 属 v0.2）。
func (r *Resolver) MoveBinding(_ context.Context, _ string, _ string, _ string, _ bool) error {
	return MultiNodeUnsupported("placement move/rebind")
}
