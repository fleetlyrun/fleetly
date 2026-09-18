package compose

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Plan 是 plan/diff 的产物：两份归一化 Spec 的变更集（架构 §2.4 plan/apply
// 语义——面向 AI Agent 的一等接口）。JSON 形态即 plan artifact：
//   - spec_hash 即 etag（apply 前校验防 stale 并发，落盘 artifact 的机制位）；
//   - requires_confirm_destructive 置位时，执行侧（T2.10 引擎层）必须持有
//     --confirm-destructive 才允许继续；
//   - 脱敏结构性成立：env 只以 key+sha256+source 表示，明文值无从泄露。
type Plan struct {
	// Name 是目标 Spec 的应用名（基线与目标名不一致时取目标侧）。
	Name string `json:"name,omitempty"`
	// SpecHash 是目标归一化形态哈希（= etag）。
	SpecHash string `json:"spec_hash"`
	// BaseSpecHash 是基线归一化形态哈希（空基线为空串）。
	BaseSpecHash string `json:"base_spec_hash,omitempty"`
	HasChanges   bool   `json:"has_changes"`
	// Destructive 表示变更集含破坏性操作（服务删除/卷解绑——省略=删除；
	// 卷数据不随声明删除的例外语义由执行层 T2.10 兑现，本期只解析标记）。
	Destructive bool `json:"destructive"`
	// RequiresConfirmDestructive 是执行侧 --confirm-destructive 的门控位
	// （v0.1 恒等于 Destructive；独立成字段以便引擎层引入非破坏性降级后
	// 二者分叉）。
	RequiresConfirmDestructive bool         `json:"requires_confirm_destructive"`
	Services                   ServicesPlan `json:"services"`
	Volumes                    VolumesPlan  `json:"volumes,omitempty"`
	Warnings                   []Warning    `json:"warnings,omitempty"`
}

// ServicesPlan 是服务维度的变更集。
type ServicesPlan struct {
	Added   []string        `json:"added,omitempty"`
	Removed []string        `json:"removed,omitempty"`
	Updated []ServiceUpdate `json:"updated,omitempty"`
}

// ServiceUpdate 是单个服务的字段级变更报告。
type ServiceUpdate struct {
	Name   string        `json:"name"`
	Fields []FieldChange `json:"fields"`
}

// FieldChange 是一条字段级变更：Path 为归一化形态内的稳定路径
// （如 environment.FOO、deploy.replicas、healthcheck.test）；From/To 为
// 该路径上的 JSON 值（env 路径只含 hash/source，无明文）。
type FieldChange struct {
	Path string `json:"path"`
	From any    `json:"from,omitempty"`
	To   any    `json:"to,omitempty"`
}

// VolumesPlan 是顶层卷维度的变更集（新增=声明卷；移除=解绑标记，数据
// 不随声明删除）。
type VolumesPlan struct {
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Diff 计算两份归一化 Spec 的变更集：base（如上一 revision 快照/基线
// compose）→ target（本次目标）。nil 参数视为空 Spec（首部署语义）。
// 数据来源接口化：真实 DB 基线（revisions.compose_normalized）随引擎票接入，
// 本期 CLI 用 --baseline 另一份 compose 文件或空基线。
func Diff(base, target *Spec) (*Plan, error) {
	b, t := base, target
	if b == nil {
		b = &Spec{}
	}
	if t == nil {
		t = &Spec{}
	}

	plan := &Plan{
		Name:         t.Name,
		SpecHash:     t.SpecHash,
		BaseSpecHash: b.SpecHash,
	}

	baseServices := serviceIndex(b)
	targetServices := serviceIndex(t)

	for _, name := range sortedStringKeys(targetServices) {
		if _, ok := baseServices[name]; !ok {
			plan.Services.Added = append(plan.Services.Added, name)
			continue
		}
		fields, err := diffService(name, baseServices[name], targetServices[name])
		if err != nil {
			return nil, err
		}
		if len(fields) > 0 {
			plan.Services.Updated = append(plan.Services.Updated, ServiceUpdate{Name: name, Fields: fields})
		}
	}
	for _, name := range sortedStringKeys(baseServices) {
		if _, ok := targetServices[name]; !ok {
			plan.Services.Removed = append(plan.Services.Removed, name)
		}
	}

	plan.Volumes.Added = sortedDelta(volumeIndex(t), volumeIndex(b))
	plan.Volumes.Removed = sortedDelta(volumeIndex(b), volumeIndex(t))

	plan.HasChanges = len(plan.Services.Added) > 0 || len(plan.Services.Removed) > 0 ||
		len(plan.Services.Updated) > 0 || len(plan.Volumes.Added) > 0 || len(plan.Volumes.Removed) > 0
	plan.Destructive = len(plan.Services.Removed) > 0 || len(plan.Volumes.Removed) > 0
	plan.RequiresConfirmDestructive = plan.Destructive
	// 计划期警告（stateful-placement §2.3）：无卷应用显式 pin 节点 →
	// 失去自动重调度（W_PLACEMENT_STATELESS_PIN，注册码）。
	plan.Warnings = append(plan.Warnings, placementPinWarnings(t)...)
	return plan, nil
}

// placementPinWarnings 对目标 Spec 产出计划期放置警告：应用整体无命名卷而
// 服务显式 pin 节点时提示（有卷应用强制钉住是平台自动行为，无需提示）。
func placementPinWarnings(target *Spec) []Warning {
	if target == nil {
		return nil
	}
	volumed := false
	for i := range target.Services {
		if len(target.Services[i].Volumes) > 0 {
			volumed = true
			break
		}
	}
	if volumed {
		return nil
	}
	var out []Warning
	for i := range target.Services {
		svc := target.Services[i]
		if svc.PlacementNode != "" {
			out = append(out, Warning{
				Code:    "W_PLACEMENT_STATELESS_PIN",
				Service: svc.Name,
				Message: "服务 " + svc.Name + " 显式钉住节点 " + svc.PlacementNode + "（应用无命名卷，将失去自动重调度；节点故障时平台不迁移）",
			})
		}
	}
	return out
}

// serviceIndex 建立服务名 → 服务的索引。
func serviceIndex(s *Spec) map[string]*Service {
	out := make(map[string]*Service, len(s.Services))
	for i := range s.Services {
		out[s.Services[i].Name] = &s.Services[i]
	}
	return out
}

// volumeIndex 建立卷名索引。
func volumeIndex(s *Spec) map[string]bool {
	out := make(map[string]bool, len(s.Volumes))
	for _, v := range s.Volumes {
		out[v.Key] = true
	}
	return out
}

// sortedDelta 返回 in − not 中不在的键（排序）。
func sortedDelta(in, not map[string]bool) []string {
	var out []string
	for k := range in {
		if !not[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// diffService 对比单服务的归一化形态：扁平化为 path → JSON 值后按键差分。
// env 以 key+hash+source 参与差分（路径 environment.<KEY>），明文值不存在
// 于归一化形态，脱敏由表示法保证。
func diffService(name string, base, target *Service) ([]FieldChange, error) {
	a, err := flatten(base)
	if err != nil {
		return nil, err
	}
	bb, err := flatten(target)
	if err != nil {
		return nil, err
	}
	var changes []FieldChange
	for _, p := range sortedStringKeys(bb) {
		if av, ok := a[p]; !ok {
			changes = append(changes, FieldChange{Path: p, To: bb[p]})
		} else if !jsonEqual(av, bb[p]) {
			changes = append(changes, FieldChange{Path: p, From: av, To: bb[p]})
		}
	}
	for _, p := range sortedStringKeys(a) {
		if _, ok := bb[p]; !ok {
			changes = append(changes, FieldChange{Path: p, From: a[p]})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// flatten 把服务归一化形态扁平化为 path → 原生值（map/slice 递归展开；
// 叶子为 string/number/bool）。服务名不参与路径（调用方已知）。
// environment 例外：按 key 展开为 environment.<KEY> 叶子值 {hash, source}
// （env 只报键名+hash 的字段级报告契约；数组下标路径对 env 重排不稳定，
// hash/source 不再下钻）。
func flatten(s *Service) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	out := map[string]any{}
	if env, ok := m["environment"].([]any); ok {
		delete(m, "environment")
		for _, e := range env {
			entry, ok := e.(map[string]any)
			if !ok {
				continue
			}
			key, _ := entry["key"].(string)
			out["environment."+key] = map[string]any{"hash": entry["hash"], "source": entry["source"]}
		}
	}
	flattenInto("", m, out)
	return out, nil
}

// flattenInto 递归展开嵌套 map/slice 到 out（path 以 . 连接）。
func flattenInto(prefix string, v any, out map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedStringKeys(t) {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			flattenInto(path, t[k], out)
		}
	case []any:
		for i, e := range t {
			flattenInto(fmt.Sprintf("%s.%d", prefix, i), e, out)
		}
	default:
		out[prefix] = v
	}
}

// jsonEqual 判定两个扁平化值是否等价（同为 JSON 原生值的深比较）。
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// RenderText 渲染人读 plan/diff 报告（非 --json 形态；顺序确定）。
func (p *Plan) RenderText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spec %s", shortHash(p.SpecHash))
	if p.BaseSpecHash != "" {
		fmt.Fprintf(&b, " (base %s)", shortHash(p.BaseSpecHash))
	}
	b.WriteString("\n")
	if !p.HasChanges {
		b.WriteString("no changes\n")
		return b.String()
	}
	for _, name := range p.Services.Added {
		fmt.Fprintf(&b, "+ service %s\n", name)
	}
	for _, name := range p.Services.Removed {
		fmt.Fprintf(&b, "- service %s (destructive)\n", name)
	}
	for _, u := range p.Services.Updated {
		fmt.Fprintf(&b, "~ service %s\n", u.Name)
		for _, f := range u.Fields {
			fmt.Fprintf(&b, "    %s: %s -> %s\n", f.Path, renderValue(f.From), renderValue(f.To))
		}
	}
	for _, name := range p.Volumes.Added {
		fmt.Fprintf(&b, "+ volume %s\n", name)
	}
	for _, name := range p.Volumes.Removed {
		fmt.Fprintf(&b, "- volume %s (unbind; data preserved)\n", name)
	}
	if p.RequiresConfirmDestructive {
		b.WriteString("destructive changes present: apply requires --confirm-destructive\n")
	}
	return b.String()
}

// renderValue 是 diff 值的人读形态（缺失渲染为 "-"）。
func renderValue(v any) string {
	if v == nil {
		return "-"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	return string(raw)
}

// shortHash 取哈希前 12 位（人读展示；artifact 内保留全量）。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
