// Package eventcode 是 fleetly 领域事件名注册表（唯一真源，只增）：Event
// 是状态迁移的既成事实（CONTEXT.md Event 词条），名形如 "deployment.
// succeeded"（<聚合>.<事实>）。三链咬合与 errcode 同构：构造期 fail-fast、
// golden 快照、usage 反扫（Outbox 落库随部署链批次接入）。
package eventcode

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// namePattern 钉死事件名格式：小写聚合 + 点 + 小写事实（node.joined、
// deployment.succeeded）。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// Event 是一条领域事件名条目。
type Event struct {
	// Name 是稳定事件名字符串（Outbox 事件与订阅面的契约锚）。
	Name string
	// Summary 一句英文摘要（对外文档面）。
	Summary string
	// Source 来源锚点：docs §anchor 或 "added during implementation"。
	Source string
}

// Registry 是注册表；构造后不可变。
type Registry struct {
	m map[string]Event
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry { return &Registry{m: map[string]Event{}} }

// MustRegister 登记一条事件；格式非法、重复、Source 为空直接 panic。
func (r *Registry) MustRegister(e Event) {
	if !namePattern.MatchString(e.Name) {
		panic(fmt.Sprintf("eventcode: invalid name %q (want %s)", e.Name, namePattern.String()))
	}
	if _, dup := r.m[e.Name]; dup {
		panic(fmt.Sprintf("eventcode: duplicate name %s", e.Name))
	}
	if strings.TrimSpace(e.Source) == "" {
		panic(fmt.Sprintf("eventcode: %s missing Source", e.Name))
	}
	r.m[e.Name] = e
}

// Get 按名查事件。
func (r *Registry) Get(name string) (Event, bool) {
	e, ok := r.m[name]
	return e, ok
}

// Len 返回条目数。
func (r *Registry) Len() int { return len(r.m) }

// Names 返回全部事件名，字典序。
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.m))
	for n := range r.m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All 返回全部条目，按名字典序。
func (r *Registry) All() []Event {
	out := make([]Event, 0, len(r.m))
	for _, n := range r.Names() {
		out = append(out, r.m[n])
	}
	return out
}

// Snapshot 输出规范化 TSV（golden 契约）：name \t summary \t source。
func (r *Registry) Snapshot() string {
	var b strings.Builder
	for _, e := range r.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", e.Name, e.Summary, e.Source)
	}
	return b.String()
}

// builtins 是首发事件集（events.go）；只增。N0 骨架尚无状态迁移，首条
// 事件随部署链批次（deployment.* / build.*）入册。

// defaultRegistry 在包初始化期逐条 MustRegister。
var defaultRegistry = func() *Registry {
	r := NewRegistry()
	for _, e := range builtins {
		r.MustRegister(e)
	}
	return r
}()

// Default 返回默认注册表。
func Default() *Registry { return defaultRegistry }

// 包级便捷面（Default 的直通）。

func Get(name string) (Event, bool) { return defaultRegistry.Get(name) }
func Len() int                      { return defaultRegistry.Len() }
func Names() []string               { return defaultRegistry.Names() }
func All() []Event                  { return defaultRegistry.All() }
func Snapshot() string              { return defaultRegistry.Snapshot() }
