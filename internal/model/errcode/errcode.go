// Package errcode 是 fleetly 错误码注册表（唯一真源，只增不复用：条目
// 一经发布即冻结语义，废弃条目保留并注明）。三链咬合：
//
//  1. 构造期 fail-fast：坏码（格式/重复）使进程启动即 panic；
//  2. golden 快照：testdata/codes.golden 钉死码集（-update 联动再生成）；
//  3. usage 反扫：零引用的码即红（预留码进豁免清单，带理由且必须仍在册）。
//
// HTTP 状态经 gRPC code 由 gateway 机械映射派生（grpcapi DefaultCodeToHTTP），
// 注册表不单独维护 HTTP int。
package errcode

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
)

// idPattern 钉死码格式：E_ 前缀 + 大写蛇形（E_INVALID_ARGUMENT）。
var idPattern = regexp.MustCompile(`^E_[A-Z0-9]+(_[A-Z0-9]+)*$`)

// DocsURLPrefix 是错误码文档锚点前缀；Envelope.docs 由 ID 派生。
const DocsURLPrefix = "https://docs.fleetly.dev/errors/"

// Code 是一个对外稳定的错误码条目。
type Code struct {
	// ID 是稳定错误码字符串（如 "E_INTERNAL"）。
	ID string
	// Summary 一句英文摘要（对外文档面；用户可见文本英文约定）。
	Summary string
	// Suggestion 可执行修复建议（Agent 与人类共用的处置提示）。
	Suggestion string
	// Source 来源锚点：docs §anchor 或 "added during implementation"
	//（三链 usage 反扫豁免预留码时同加注释）。
	Source string
	// GRPC 是该码的 gRPC status code（错误族门禁；HTTP 由 gateway 派生）。
	GRPC codes.Code
}

// Registry 是注册表；构造后不可变。
type Registry struct {
	m map[string]Code
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry { return &Registry{m: map[string]Code{}} }

// MustRegister 登记一条码；格式非法、重复、Source 为空、GRPC 非错误族
// 均为编程错误，直接 panic（fail-fast：坏码不该等到首个请求才暴露）。
func (r *Registry) MustRegister(c Code) {
	if !idPattern.MatchString(c.ID) {
		panic(fmt.Sprintf("errcode: invalid id %q (want %s)", c.ID, idPattern.String()))
	}
	if _, dup := r.m[c.ID]; dup {
		panic(fmt.Sprintf("errcode: duplicate id %s", c.ID))
	}
	if strings.TrimSpace(c.Source) == "" {
		panic(fmt.Sprintf("errcode: %s missing Source (docs anchor or \"added during implementation\")", c.ID))
	}
	if c.GRPC == codes.OK {
		panic(fmt.Sprintf("errcode: %s maps to codes.OK", c.ID))
	}
	r.m[c.ID] = c
}

// Get 按 ID 查码。
func (r *Registry) Get(id string) (Code, bool) {
	c, ok := r.m[id]
	return c, ok
}

// Len 返回条目数。
func (r *Registry) Len() int { return len(r.m) }

// IDs 返回全部 ID，字典序。
func (r *Registry) IDs() []string {
	ids := make([]string, 0, len(r.m))
	for id := range r.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// All 返回全部条目，按 ID 字典序。
func (r *Registry) All() []Code {
	out := make([]Code, 0, len(r.m))
	for _, id := range r.IDs() {
		out = append(out, r.m[id])
	}
	return out
}

// Snapshot 输出规范化 TSV（golden 契约）：ID \t grpc \t summary \t
// suggestion \t source，按 ID 字典序。
func (r *Registry) Snapshot() string {
	var b strings.Builder
	for _, c := range r.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.GRPC.String(), c.Summary, c.Suggestion, c.Source)
	}
	return b.String()
}

// defaultRegistry 在包初始化期逐条 MustRegister——坏码使任何 import 本包
// 的进程（含测试）启动即红。
var defaultRegistry = func() *Registry {
	r := NewRegistry()
	for _, c := range builtins {
		r.MustRegister(c)
	}
	return r
}()

// Default 返回默认注册表。
func Default() *Registry { return defaultRegistry }

// 包级便捷面（Default 的直通）。

func Get(id string) (Code, bool) { return defaultRegistry.Get(id) }
func Len() int                   { return defaultRegistry.Len() }
func IDs() []string              { return defaultRegistry.IDs() }
func All() []Code                { return defaultRegistry.All() }
func Snapshot() string           { return defaultRegistry.Snapshot() }

// DocsURL 由 ID 派生文档锚点 URL。
func DocsURL(id string) string { return DocsURLPrefix + id }
