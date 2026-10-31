// Package drift 承载漂移判定的纯规则面（P3a 批第 3 单元，ADR-0087 决策
// 1/2/7：漂移签名比对 + stopped 去抖自 application/engine/drift.go 下沉
// domain/delivery——「给定期望与观测 → 裁决 + 去抖签名」的纯部分归域，
// CONTEXT.md Drift 词条口径：实际状态偏离已下发 Spec 的信号）。留驻
// engine 的执行面：三张 memo 签名 map（spec 失配/滚动停摆/稳态 stopped）
// 的生命周期与锁、driftScan/driftLoop 环、Kick、事件发射与收敛信号注册
// （engine 装配事实 → 本包裁决 → engine 执刑）。纯度铁律（ADR-0087 决策
// 2 + domain 三红线）：零 I/O、禁 time.Now（本包规则无时间参与——stopped
// 去抖是 gen 作用域的签名去抖，无时刻/窗长）、不写 receiver、不 import
// genproto。
package drift

import (
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ---- 扫描面：spec 对照裁决（engine compareSpecs 的判定核）----

// ScanInput 是扫描面对一条载体观测的对照输入（engine 装配：观测账本 +
// 期望账本 + 基线锚——锁内取事实，锁外裁决）。
type ScanInput struct {
	// Obs 是一条载体观测（RuntimeInspector 域内枚举面）。
	Obs capability.WorkloadObservation
	// Managed 报告观测账本是否有该载体的期望条目（ensuredSpec 在场）；
	// false = 非平台管辖载体（孤儿面只登记原则）——零动作。
	Managed bool
	// Want 是期望侧投影 spec（最近 Ensure 的 spec 账本条目）。
	Want capability.Workload
	// LedgerWriter 是期望账本条目的写入部署 ID（ensuredBy）；
	// WriterKnown = false 为账本无写入者条目。
	LedgerWriter string
	WriterKnown  bool
	// BaselineID 是当前 LatestSucceeded 行 ID；空 = 无成功基线（无可收敛
	// 锚，spec/gen 面跳过）。
	BaselineID string
	// OwnerGen 是 owner 级期望 Generation（expect.expected；drift 事件锚
	// 的回落位）。
	OwnerGen uint64
	// EnsuredGen/HasEnsuredGen 是逐载体 gen 锚（ADR-0048 决策 2：双代窗
	// 两代并存，owner 级单值无法对照两代——在场即优先于 OwnerGen）。
	EnsuredGen    uint64
	HasEnsuredGen bool
}

// ScanVerdict 是扫描面对一条载体观测的裁决（三面独立：滚动停摆/gen 偏离/
// spec 失配——各自独立签名键互不覆盖，ADR-0082 §4 与 §1 行 1/3/4）。非
// Skip 裁决至少落一面动作（停摆面恒有 RolloutSig 或 RolloutClear 之一）。
type ScanVerdict struct {
	// Skip = 载体不参与本拍对照（非平台管辖/账本未采用/无成功基线）：
	// 方向安全——漏报不误报，签名与信号面零动作。
	Skip bool
	// RolloutSig 非空 = 滚动停摆签名（收敛信号值与 memo 去抖键同一字面）。
	RolloutSig string
	// RolloutClear = 停摆恢复：清 memo 停摆签名（下次停摆可再发）。
	RolloutClear bool
	// GenSig 非空 = gen 偏离信号（本载体跳过 spec 对照——gen 偏离与
	// spec 失配是两个独立信号不互斥，但 spec 面让位）。
	GenSig string
	// SpecMismatch 非空 = spec 失配明细（drift 事件 message 面）。
	SpecMismatch string
	// SpecSig 非空 = spec 失配签名（收敛信号值与 memo 去抖键同一字面）。
	SpecSig string
	// SpecClear = spec 相符：清 memo 失配签名（回归即清，下次偏离可再发）。
	SpecClear bool
	// Anchor 是 drift 事件载荷的对照锚（逐载体 gen 锚在场即用，否则
	// owner 级锚）。
	Anchor uint64
}

// AdjudicateScan 裁决扫描面对一条载体观测的对照（自 engine compareSpecs
// 的判定核下沉，ADR-0087 决策 7；裁决表）：
//
//	非平台管辖 / 无成功基线 / 账本写入者 ≠ 当前基线 → Skip
//	  （账本采用规则，ADR-0082 §6：条目必须属于当前生效基线——未成功
//	  终态（Cancel/failed/superseded）或进行中部署写下的条目是「上次
//	  下发了什么」，不是「期望什么」，不得当锚（否则遗留载体与脏账本
//	  一致 → spec/gen 零信号；R16-M3 的根因）。不采用 = 跳过该载体的
//	  spec/gen 对照，方向安全：漏报不误报。部署窗收窄的显式取舍：窗内
//	  这些载体的 spec/gen 不参与对照，窗口结束后条目归属新基线，对照
//	  自动恢复）
//	停摆（RolloutStalled）→ RolloutSig（"rollout|<编排器原话>"；同停摆
//	  不重复发，恢复即清——与 spec 失配分立去抖，runbook 记录·二十七。
//	  判定在 gen 锚之前：停摆时 spec 通常是最新 gen，但 gen 偏离与停摆
//	  是两个独立信号，不互斥）
//	停摆恢复 → RolloutClear
//	gen 锚在场 × 观测 gen ≠ 锚 → GenSig（独立签名键，ADR-0082 §1 行 3）
//	  + 跳过 spec 对照；无锚（detect 期望账本/冷窗）→ gen 面跳过
//	  （安全侧：漏报不误报——原「回落 App 级 expected」分支在 detect
//	  账本下会把 0 当锚，令全部载体产假 gen 偏离，故删）
//	spec 相符 → SpecClear
//	spec 失配 → SpecMismatch + SpecSig（image → 入口覆盖命令 → 副本数
//	  序，首中即返）
//
// 纯函数：不写 receiver；memo 签名的比对/落签/清签与事件发射由 engine
// 执行（去抖执行 = 锁内 compare-and-set，属执行机制，ADR-0087 决策 3）。
func AdjudicateScan(in ScanInput) ScanVerdict {
	if !in.Managed || !in.WriterKnown || in.BaselineID == "" || in.LedgerWriter != in.BaselineID {
		return ScanVerdict{Skip: true}
	}
	v := ScanVerdict{Anchor: in.OwnerGen}
	if in.HasEnsuredGen {
		v.Anchor = in.EnsuredGen
	}
	if in.Obs.RolloutStalled {
		v.RolloutSig = rolloutSig(in.Obs.RolloutDetail)
	} else {
		v.RolloutClear = true
	}
	if in.HasEnsuredGen && uint64(in.Obs.Generation) != in.EnsuredGen {
		v.GenSig = genSig(in.EnsuredGen, uint64(in.Obs.Generation))
		return v
	}
	if m := mismatch(in.Obs, in.Want); m != "" {
		v.SpecMismatch, v.SpecSig = m, specSig(m)
	} else {
		v.SpecClear = true
	}
	return v
}

// mismatch 报告观测与期望 spec 的首处失配明细（image → 入口覆盖命令 →
// 副本数序，首中即返；空 = 相符）。
//
// 镜像逐字比对是正确口径（2026-10-01 双腿实证，N0.1 P1-2）：dind 里
// `docker service create` CLI 会尝试把 tag 解析钉版为 repo:tag@sha256
// （registry 不可达时存原样 tag 并告警）；但 fleetly 走 moby API 直传
// spec，swarm 服务端不重写 spec.Image——staging 现役三个 tag 部署载体
// spec 全为原样 tag，且 6 次部署 + 全天 30s 扫描零 drift 事件。假 drift
// 假设（API 路径钉版）不成立；若未来出现钉版形态（如 CLI 人工改同 tag
// 镜像），以显式证据重开此案，不在此预放行。restart_seq 不对照
// （ADR-0083 边界裁决：意图字段非可观测态，WorkloadObservation 不携带）。
func mismatch(obs capability.WorkloadObservation, want capability.Workload) string {
	switch {
	case obs.Image != want.Image:
		return fmt.Sprintf("image %q != expected %q", obs.Image, want.Image)
	case !sameCommand(obs.Command, want.Command):
		// 入口覆盖命令对照（ADR-0022）：nil 与空切片等价（无覆盖 =
		// 镜像默认），逐元素比对防假 drift。
		return fmt.Sprintf("command %q != expected %q", obs.Command, want.Command)
	case obs.Replicas != want.Replicas:
		return fmt.Sprintf("replicas %d != expected %d", obs.Replicas, want.Replicas)
	}
	return ""
}

// sameCommand 逐元素比较入口覆盖命令（nil 与空切片等价：两者都是
// "无覆盖，镜像默认"——载体观测与投影 spec 的切片形态差异不得产假 drift）。
func sameCommand(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- Watch 流漂移判定（engine detectDrift 的判定核）----

// WatchInput 是 Watch 流漂移判定的输入（engine 装配：Expected 已按
// 「逐载体 gen 锚在场即优先，缺席回落 owner 级」解析——双代窗两代并存时
// owner 级单值无法对照两代，ADR-0048 决策 2）。
type WatchInput struct {
	Expected uint64 // 期望锚（0 = 无锚）
	Observed uint64 // 观测 Generation
	Drifted  bool   // 载体自报漂移（Provider Drift 旗标）
}

// WatchVerdict 是 Watch 流漂移裁决（三态互斥）。
type WatchVerdict struct {
	// Clear = 回归期望（锚在场、gen 匹配且载体不自报漂移）：清失配签名
	// （下次偏离可再发）。
	Clear bool
	// Silent = 无锚且非漂移：零动作（不动签名——冷窗不猜）。
	Silent bool
	// Sig 非空 = 漂移签名（engine 锁内比对 memo 去抖后发事件；字面 =
	// 期望锚|观测 gen|漂移旗标）。
	Sig string
}

// AdjudicateWatch 裁决 Watch 流的漂移判定（自 engine detectDrift 的判定
// 核下沉；架构 §5 Drift 判定，ADR-0022 Generation 偏离与载体自报漂移
// 双路径）：
//
//	有锚 × gen 匹配 × 不自报漂移 → Clear（回归即清）
//	无锚 × 不自报漂移             → Silent
//	其余（gen 偏离 / 自报漂移）    → Sig（engine 去抖后发
//	  workload.drift_detected）
func AdjudicateWatch(in WatchInput) WatchVerdict {
	if in.Expected != 0 && in.Observed == in.Expected && !in.Drifted {
		return WatchVerdict{Clear: true}
	}
	if in.Expected == 0 && !in.Drifted {
		return WatchVerdict{Silent: true}
	}
	return WatchVerdict{Sig: fmt.Sprintf("%d|%d|%v", in.Expected, in.Observed, in.Drifted)}
}

// ---- 稳态 stopped 看门狗（engine driftScan 的判定核）----

// SteadyStopCandidate 裁决稳态看门狗的一拍候选（ADR-0022 稳态 stopped；
// ADR-0082 §1 行 5——spec 相符 + stopped 只告警不收敛，任务重启是编排器
// 自责）：
//
//	期望锚缺席（0）          → 非候选（重启冷窗/非管辖：不猜）
//	App 域在途               → 非候选（部署中的停止是部署链的事；非 App
//	                           域（Database/受管）无在途概念恒可判——
//	                           engine 装配时 appInFlight 已按域归零）
//	观测 stopped × gen 匹配  → 候选（engine 按 StoppedSig 去抖后发
//	                           workload.stopped）
//
// 未观测载体（engine 传零值事件）恒非候选（State 零值 ≠ stopped）。
func SteadyStopCandidate(expected uint64, appInFlight bool, ev capability.WorkloadEvent) bool {
	if expected == 0 || appInFlight {
		return false
	}
	return ev.State == capability.WorkloadStopped && uint64(ev.Generation) == expected
}

// ---- 缺席/多余检测（期望集对照观测集的两个方向）----

// MissingIDs 计算缺席面：期望集 − 观测集（字典序；空 = 无缺席。调用方
// 喂入的期望集按 ID 排序，缺席序随之）。与 ExtraIDs 是同一对照的两个
// 方向（行2/行8 同源同序，便于对照测试）。
func MissingIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string {
	seen := make(map[string]bool, len(obs))
	for _, o := range obs {
		seen[o.WorkloadID] = true
	}
	var missing []string
	for _, w := range ws {
		if !seen[w.ID] {
			missing = append(missing, w.ID)
		}
	}
	return missing
}

// ExtraIDs 计算多余面：观测集 − 期望集（字典序；空 = 无多余）。数据源是
// InspectWorkloads 的**域内**枚举（swarm/k3s 两侧都按 fleetly.managed 标签
// + 域选择器过滤）——因此它是「域内多余载体」（行 8）而非「域外孤儿」
// （行 9，明确未落码）。部署窗内在途新代载体由裁决行 6 Driving 让位。
func ExtraIDs(ws []capability.Workload, obs []capability.WorkloadObservation) []string {
	expected := make(map[string]bool, len(ws))
	for _, w := range ws {
		expected[w.ID] = true
	}
	var extra []string
	for _, o := range obs {
		if !expected[o.WorkloadID] {
			extra = append(extra, o.WorkloadID)
		}
	}
	sort.Strings(extra)
	return extra
}

// ---- 去抖签名字面（engine 三张 memo map 的键值单源）----

// specSig 是 spec 失配签名（含 spec 指纹：不同失配不同签名，同失配
// 同签名）。
func specSig(mismatch string) string { return "spec|" + mismatch }

// genSig 是 gen 偏离签名（期望锚|观测 gen）。
func genSig(expected, observed uint64) string {
	return fmt.Sprintf("gen|%d|%d", expected, observed)
}

// rolloutSig 是滚动停摆签名（编排器原话入签：不同停摆不同签名）。
func rolloutSig(detail string) string { return "rollout|" + detail }

// StoppedSig 是稳态 stopped 的去抖签名（gen 作用域：同 gen 停止只发一次；
// 观测回 running/degraded 由 engine 清签名，下次停止可再发）。
func StoppedSig(gen uint64) string { return fmt.Sprintf("stopped|%d", gen) }
