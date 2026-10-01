// Package schema 是能力自描述面（架构 §7：fleetly explain / fleetly
// schema，porter 模式——Agent 的零文档发现面）。Spec 与事件 payload 契约
// 由 Go 类型反射生成 JSON Schema（draft-07 子集）；扩展面各自注入合并：
// 本包登记 builtin Spec 条目，事件 payload 形状的拥有方（engine、api 层）
// 在自身 init 期 Register——与 capability.RegisterFactory 同款自注册纪律。
//
// 叶子纯度：只 import genproto 与标准库（守卫见 internal/guards）。
//
// 形状钉扎（ADR-0026）：payload 字段只增原则的可执法形态——schema golden
// 漂移门在 internal/assembly，字段改名/删除即 CI 红。
package schema

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// schemaDialect 是输出文档自报的 JSON Schema 方言（描述用；本面只产出
// 子集：type/properties/required/additionalProperties/items/enum/oneOf/
// format）。
const schemaDialect = "http://json-schema.org/draft-07/schema#"

// Kind 是条目家族（spec = 期望状态 IR；event = Outbox 事件 payload）。
type Kind string

const (
	KindSpec  Kind = "spec"
	KindEvent Kind = "event"
)

// Entry 是一条可解释资源的自描述；Name 是 explain 的寻址名（spec 用领域
// 名词如 "app"，event 用事件名如 "deployment.succeeded"），Summary 单源
// 自 eventcode 注册表（事件）或本包（spec）。
type Entry struct {
	Name    string         `json:"name"`
	Kind    Kind           `json:"kind"`
	Summary string         `json:"summary"`
	Schema  map[string]any `json:"schema"`
}

// SchemaJSON 返回 schema 树的 canonical 紧凑 JSON（map 键字典序——
// encoding/json 内建排序，字节确定；proto 面 schema_json 字段与 CLI
// golden 共用同一串）。
func (e Entry) SchemaJSON() string {
	data, err := json.Marshal(e.Schema)
	if err != nil {
		// 注册期已校验可序列化；此处不可达。
		panic(fmt.Sprintf("schema: entry %s schema is not marshalable: %v", e.Name, err))
	}
	return string(data)
}

// registry 是注入合并注册表；Register 只在 init 期调用，Build 读侧无锁
// （构造后不可变）。
var registry struct {
	mu      sync.Mutex
	entries map[string]Entry
}

// Register 登记一条自描述条目（扩展面 init 期调用；重名/空字段/不可序列
// 化 schema 直接 panic——装配错误在启动期暴露）。
func Register(kind Kind, name, summary string, schemaTree map[string]any) {
	if name == "" || summary == "" {
		panic(fmt.Sprintf("schema: registration must carry name and summary (kind=%s name=%q)", kind, name))
	}
	if len(schemaTree) == 0 {
		panic(fmt.Sprintf("schema: registration %s carries an empty schema", name))
	}
	if kind != KindSpec && kind != KindEvent {
		panic(fmt.Sprintf("schema: registration %s carries unknown kind %q", name, kind))
	}
	tree := withDialect(schemaTree)
	if _, err := json.Marshal(tree); err != nil {
		panic(fmt.Sprintf("schema: registration %s schema is not marshalable: %v", name, err))
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.entries == nil {
		registry.entries = map[string]Entry{}
	}
	if _, dup := registry.entries[name]; dup {
		panic(fmt.Sprintf("schema: duplicate registration for %s", name))
	}
	registry.entries[name] = Entry{Name: name, Kind: kind, Summary: summary, Schema: tree}
}

// withDialect 把方言自报键盖进树（顶层唯一写点）。
func withDialect(tree map[string]any) map[string]any {
	out := make(map[string]any, len(tree)+1)
	for k, v := range tree {
		out[k] = v
	}
	out["$schema"] = schemaDialect
	return out
}

// Null 是恒 null payload 的 schema（如 token.revoked 落库 "null" 字节）。
func Null() map[string]any { return map[string]any{"type": "null"} }

// Document 是合并后的全量自描述文档（条目按名字典序——输出确定性是
// golden 漂移门的前提）。
type Document struct {
	Entries []Entry `json:"entries"`
}

// CanonicalJSON 渲染全量文档的确定性 JSON（两空格缩进 + 尾换行；条目
// 字典序、map 键字典序——golden 漂移门与评审 diff 的同一形态）。
func (d Document) CanonicalJSON() string {
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("schema: document is not marshalable: %v", err))
	}
	return string(data) + "\n"
}

// Build 合并全部注册条目（builtin Spec + 各扩展面注入的事件 payload）。
func Build() Document {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	names := make([]string, 0, len(registry.entries))
	for n := range registry.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	doc := Document{Entries: make([]Entry, 0, len(names))}
	for _, n := range names {
		doc.Entries = append(doc.Entries, registry.entries[n])
	}
	return doc
}

// Lookup 按寻址名取单条（explain 的服务端裁决面）。
func Lookup(name string) (Entry, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	e, ok := registry.entries[name]
	return e, ok
}
