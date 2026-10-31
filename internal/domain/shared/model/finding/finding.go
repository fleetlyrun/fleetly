// Package finding 是 fleetly 诊断条目注册表（唯一真源，只增：条目一经发布
// 即冻结语义，废弃条目保留并注明——与 errcode/eventcode 同座同构，CONTEXT.md
// Finding 词条，ADR-0085）。三链咬合：
//
//  1. 构造期 fail-fast：坏条目（ID 格式/重复/Source 空/Severity·FaultClass
//     非法）使进程启动即 panic；
//  2. golden 快照：testdata/findings.golden 钉死条目集（-update 联动再生成）；
//  3. usage 反扫：零引用的条目即红（确属预留进豁免清单，带理由且必须仍在册，
//     双向保鲜）。
//
// 全纯值类型（domainpurity 红线：零 genproto、零 I/O）；Fix 是结构化动作
// （docs/design/2026-10-30-zeabur-oss-borrowing.md §2.2）：verb 可选（空 =
// 无直达动作，console 深链即动线）、params 预填、console_path 深链模板
// （命名占位符 {app_id} 形态，装配面填充）。
package finding

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// idPattern 钉死条目 ID 格式：F_ 前缀 + 大写蛇形（F_WORKLOAD_CRASH_LOOP）。
var idPattern = regexp.MustCompile(`^F_[A-Z0-9]+(_[A-Z0-9]+)*$`)

// Severity 是诊断严重度（已裁两级，docs §2.2 / ADR-0085）。
type Severity string

const (
	// SeverityWarning 是「现在坏」的解释档。
	SeverityWarning Severity = "warning"
	// SeverityAdvice 是「还没坏/解释态」的建议档（v1 仅稳态 stopped 一条）。
	SeverityAdvice Severity = "advice"
)

// severities 是合法 Severity 全集（fail-fast 判定单源）。
var severities = map[Severity]bool{
	SeverityWarning: true,
	SeverityAdvice:  true,
}

// FaultClass 是错误归属轴（X-Class 族词条，CONTEXT.md FaultClass；四档
// 枚举，已裁合一不再细分）。自托管语义在「往哪看」非「谁来修」：platform
// 的错永远不读成用户的错。
type FaultClass string

const (
	// FaultClassApp：用户代码/镜像/依赖（崩溃循环、退出码、OOM、健康检查不过）。
	FaultClassApp FaultClass = "app"
	// FaultClassDeclaration：声明/配置（healthcheck 缺失、端口不符、探针
	// 路径错、命令不可执行）。
	FaultClassDeclaration FaultClass = "declaration"
	// FaultClassPlatform：受管组件/控制面/收敛环/集群（三分合一，ADR-0085）。
	FaultClassPlatform FaultClass = "platform"
	// FaultClassExternal：上游外部（镜像源、ACME/DNS、Provider API）。
	FaultClassExternal FaultClass = "external"
)

// faultClasses 是合法 FaultClass 全集（fail-fast 判定单源）。
var faultClasses = map[FaultClass]bool{
	FaultClassApp:         true,
	FaultClassDeclaration: true,
	FaultClassPlatform:    true,
	FaultClassExternal:    true,
}

// Fix 是结构化修复动作（纯值）。动词面（§2.2 已裁）：终归 Deploy 的声明
// 改动不带 verb（console 深链进 DeploySheet 走 B1 预览）；rollback/restart/
// scale 是直达动作 verb（Fix 按钮直发带票动作）；platform 类对 project 操作者
// Fix = Components 深链或显式空 + 说明文案。
type Fix struct {
	// Verb 是动作动词（"restart"/"rollback"/"scale"）；空 = 无直达动作。
	Verb string
	// Params 是动词/深链的预填参数（注册表模板层；实例层由规则合并补全）。
	Params map[string]string
	// ConsolePath 是 console 深链模板（"/p/{project_id}/apps/{app_id}"）；
	// 显式空 = 无深链（platform 等管理员修形态）。
	ConsolePath string
}

// Finding 是一个对外稳定的诊断条目（注册表值；诊断实例形态在 diagnosis
// 域环包，以 ID 回指本表）。
type Finding struct {
	// ID 是稳定条目字符串（如 "F_WORKLOAD_CRASH_LOOP"）。
	ID string
	// Summary 一句英文摘要（对外文档面；用户可见文本英文约定）。
	Summary string
	// Severity 是严重度两级（warning|advice）。
	Severity Severity
	// FaultClass 是错误归属四档（app|declaration|platform|external）。
	FaultClass FaultClass
	// Source 来源锚点：规则 file path（diagnosis 域环包内相对路径）+ 函数 +
	// 提案/ADR 锚。
	Source string
	// Fix 是结构化修复动作（纯值）。
	Fix Fix
}

// Registry 是注册表；构造后不可变。
type Registry struct {
	m map[string]Finding
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry { return &Registry{m: map[string]Finding{}} }

// MustRegister 登记一条条目；ID 格式非法、重复、Source 为空、Severity 或
// FaultClass 非法均为编程错误，直接 panic（fail-fast：坏条目不该等到首个
// 诊断请求才暴露）。
func (r *Registry) MustRegister(f Finding) {
	if !idPattern.MatchString(f.ID) {
		panic(fmt.Sprintf("finding: invalid id %q (want %s)", f.ID, idPattern.String()))
	}
	if _, dup := r.m[f.ID]; dup {
		panic(fmt.Sprintf("finding: duplicate id %s", f.ID))
	}
	if strings.TrimSpace(f.Source) == "" {
		panic(fmt.Sprintf("finding: %s missing Source (rule file path + function + design/ADR anchor)", f.ID))
	}
	if !severities[f.Severity] {
		panic(fmt.Sprintf("finding: %s has invalid severity %q (want warning|advice)", f.ID, f.Severity))
	}
	if !faultClasses[f.FaultClass] {
		panic(fmt.Sprintf("finding: %s has invalid fault class %q (want app|declaration|platform|external)", f.ID, f.FaultClass))
	}
	r.m[f.ID] = f
}

// Get 按 ID 查条目。
func (r *Registry) Get(id string) (Finding, bool) {
	f, ok := r.m[id]
	return f, ok
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
func (r *Registry) All() []Finding {
	out := make([]Finding, 0, len(r.m))
	for _, id := range r.IDs() {
		out = append(out, r.m[id])
	}
	return out
}

// Snapshot 输出规范化 TSV（golden 契约）：ID \t severity \t fault_class \t
// summary \t fix_verb \t fix_params \t fix_console_path \t source，按 ID
// 字典序。fix_params 是排序后的 k=v 逗号连接（空集 = 空列）。
func (r *Registry) Snapshot() string {
	var b strings.Builder
	for _, f := range r.All() {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			f.ID, f.Severity, f.FaultClass, f.Summary,
			f.Fix.Verb, formatFixParams(f.Fix.Params), f.Fix.ConsolePath, f.Source)
	}
	return b.String()
}

// formatFixParams 把 Fix.Params 序列化为排序后的 k=v 逗号连接（确定性）。
func formatFixParams(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, ",")
}

// defaultRegistry 在包初始化期逐条 MustRegister——坏条目使任何 import 本包
// 的进程（含测试）启动即红。
var defaultRegistry = func() *Registry {
	r := NewRegistry()
	for _, f := range builtins {
		r.MustRegister(f)
	}
	return r
}()

// Default 返回默认注册表。
func Default() *Registry { return defaultRegistry }

// 包级便捷面（Default 的直通）。

func Get(id string) (Finding, bool) { return defaultRegistry.Get(id) }
func Len() int                      { return defaultRegistry.Len() }
func IDs() []string                 { return defaultRegistry.IDs() }
func All() []Finding                { return defaultRegistry.All() }
func Snapshot() string              { return defaultRegistry.Snapshot() }
