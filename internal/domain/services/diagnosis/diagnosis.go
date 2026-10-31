// Package diagnosis 是诊断域纯函数层（W1a，docs/design/2026-10-30-zeabur-
// oss-borrowing.md §2；CONTEXT.md「诊断与排障」词条，ADR-0085）：以 Facts
// 观测快照为输入、Finding 注册表条目的实例为输出，读时计算回答「为什么
// 现在坏」。零 I/O、零 genproto（domainpurity 三红线）；不评估趋势（Alert
// Rule 分立）、不收敛（Drift 分立）、不重复部署失败解释（deployment error
// 与 build log 承载）。装配（facts 面）与 RPC/CLI/Console 面是 W1b/B2b
// 领地，本包只有纯函数与纯值。
package diagnosis

import (
	"fmt"
	"maps"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/finding"
)

// EvidenceKind 是证据引用的闭式清单（独立审查轮 A7：fleetly 域引用——
// 不含组件 Details/内部地址、不含他队 App/Route；事件原文与事件面同口径
// 不转写放大）。
type EvidenceKind string

const (
	// EvidenceWorkload：本 App 的 workload 引用。
	EvidenceWorkload EvidenceKind = "workload"
	// EvidenceEvent：本 App 事件账本行引用（Seq 锚）。
	EvidenceEvent EvidenceKind = "event"
	// EvidenceAlert：本 App 告警规则行引用。
	EvidenceAlert EvidenceKind = "alert"
	// EvidenceComponent：组件名（不含组件内部细节）。
	EvidenceComponent EvidenceKind = "component"
	// EvidenceIncident：Platform Incident 条目 id（RewriteByIncident 回链）。
	// 值形 platform_incident（修复批 F4：裸 "incident" 违 ADR-0085 词汇红线
	// ——Platform Incident 应全称；B2b 事故面实装前收口）。
	EvidenceIncident EvidenceKind = "platform_incident"
)

// Evidence 是一条域引用证据。
type Evidence struct {
	// Kind 是引用类别（闭式清单）。
	Kind EvidenceKind
	// Ref 是引用（workload id / "seq:42" / 组件名 / Platform Incident id）。
	Ref string
}

// Finding 是一次诊断产出的条目实例：注册表条目（真源）+ 定位/证据/动态
// 解释（实例层）。FaultClass 初始 = 注册表条目值，可被 RewriteByIncident
// 改写为 platform（platform 的错永远不读成用户的错）。
type Finding struct {
	// Entry 是注册表条目（ID/Summary/Severity/Source/Fix 模板真源）。
	Entry finding.Finding
	// WorkloadID 是定位 workload（v1 规则全部 workload 锚定）。
	WorkloadID string
	// Process 是进程模板名（Fix 预填/页面定位）。
	Process string
	// FaultClass 是生效归属（初始 = Entry.FaultClass；RewriteByIncident 改写）。
	FaultClass finding.FaultClass
	// Fix 是实例修复动作（注册表模板 + 规则预填合并）。
	Fix finding.Fix
	// Params 是动态解释参数（exit_code/state/restart_count 等，纯字符串）。
	Params map[string]string
	// Evidence 是域引用证据链（闭式清单）。
	Evidence []Evidence
}

// ID 是注册表条目 ID 的直通。
func (f Finding) ID() string { return f.Entry.ID }

// Severity 是注册表严重度的直通（两级定形不随实例漂移）。
func (f Finding) Severity() finding.Severity { return f.Entry.Severity }

// Rule 是一条诊断规则：纯函数 Check，输入 Facts 快照，输出条目实例
// （零副作用、零状态；确定性：同输入同输出，输出序 = 观测序）。
type Rule struct {
	// ID 是规则 id（小写短横形，如 "crash-loop"；与注册表条目 id 分轴）。
	ID string
	// Check 是判定函数；事实面 None 时必须沉默（返回 nil）。
	Check func(Facts) []Finding
}

// explainers 是解释性规则（unexplained 之前的全量；顺序即 Evaluate 产出序）。
var explainers = []Rule{
	ruleCrashLoop,
	ruleOOM,
	ruleHealthCheck,
	ruleImagePull,
	ruleStopped,
}

// ruleUnexplained 的覆盖判定复用 explainers（单源：不维护第二份「哪些算
// 已解释」清单——完备性承诺由同一组规则推导）。
var ruleUnexplained = Rule{
	ID: "unexplained",
	Check: func(facts Facts) []Finding {
		covered := map[string]bool{}
		for _, f := range RunExplainers(facts) {
			covered[f.WorkloadID] = true
		}
		return unexplainedGaps(facts, covered)
	},
}

// Rules 是全量规则清单（静态数组、id 中心收口；unexplained 兜底居末）。
// 守卫测试断言其 = explainers + unexplained 且 id 唯一。
var Rules = []Rule{
	ruleCrashLoop,
	ruleOOM,
	ruleHealthCheck,
	ruleImagePull,
	ruleStopped,
	ruleUnexplained,
}

// RunExplainers 只跑解释性规则（unexplained 的覆盖判定与外部「不要兜底」
// 消费面共用）。
func RunExplainers(facts Facts) []Finding {
	var out []Finding
	for _, r := range explainers {
		out = append(out, r.Check(facts)...)
	}
	return out
}

// Evaluate 跑全量规则（含 unexplained 兜底）：产出序 = Rules 序 × 观测序，
// 确定性。完备性承诺：存在失败态观测（观测面可读）时产出永不为空。
func Evaluate(facts Facts) []Finding {
	var out []Finding
	for _, r := range Rules {
		out = append(out, r.Check(facts)...)
	}
	return out
}

// instantiate 由注册表条目实例化一个 Finding：未知条目 id 即 panic（规则
// 引用未入册条目是编程错误，构造期暴露）；fixParams 与注册表 Fix.Params
// 合并预填（实例覆盖模板同键）。
func instantiate(id string, o WorkloadObservationFact, fixParams, params map[string]string, extra ...Evidence) Finding {
	entry, ok := finding.Get(id)
	if !ok {
		panic(fmt.Sprintf("diagnosis: rule references finding %q not in the registry", id))
	}
	f := Finding{
		Entry:      entry,
		WorkloadID: o.WorkloadID,
		Process:    o.Process,
		FaultClass: entry.FaultClass,
		Fix: finding.Fix{
			Verb:        entry.Fix.Verb,
			Params:      maps.Clone(entry.Fix.Params),
			ConsolePath: entry.Fix.ConsolePath,
		},
		Params:   params,
		Evidence: append([]Evidence{{Kind: EvidenceWorkload, Ref: o.WorkloadID}}, extra...),
	}
	for k, v := range fixParams {
		if f.Fix.Params == nil {
			f.Fix.Params = map[string]string{}
		}
		f.Fix.Params[k] = v
	}
	return f
}

// latestEvent 取近窗最近事件（Seq 最大者；事件面 None 时无证据——不猜）。
func latestEvent(events []EventFact) (EventFact, bool) {
	var best EventFact
	var found bool
	for _, e := range events {
		if !found || e.Seq > best.Seq {
			best, found = e, true
		}
	}
	return best, found
}

// exitCodeString 是退出码的字符串化（nil = ""，便于 Params 纯字符串面）。
func exitCodeString(code *int) string {
	if code == nil {
		return ""
	}
	return fmt.Sprintf("%d", *code)
}
