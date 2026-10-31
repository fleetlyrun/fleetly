// Package bluegreen 承载蓝绿晋升/回退判据的纯规则面（P3a 批第 7 单元，
// ADR-0087 决策 1/2/7/8：健康门三态裁决〔晋升/回退/保持〕+ 新代进程组
// 齐备性 + L2 看门狗咬合 + 双代窗代差判定〔基线锚解析/在服代推导〕自
// application/engine 下沉 domain/delivery）。落点定界：晋升/回退判据是
// 跨源裁决（健康观测 × 部署行事实 × 截止时刻），非单一聚合的迁移表，
// 亦非多规则注册表族——按聚合域包邻域小策略包落位，与漂移判定核
// （domain/delivery/drift）和收敛策略（domain/delivery/converge）邻接
// 对称（检出漂移 → 收敛裁决 → 部署链健康门是同一交付产品线）。判据服务
// 全部部署变体：健康门是部署链相位门，blue-green 消费同一张表兑现「先验
// 后切」（ADR-0048 决策 1：BG 是 engine 编排变体，状态枚举不变）。
// 留驻 engine 的执行面：release/observe/rollback 驱动与 deadline 补设、
// repo/transit 行写、Ensure/载体操作、Route 重发布、emit、物化备忘——
// engine 侧形态 = 装配（锁内取观测 + 行事实 + 配置）→ 域裁决（本文件）
// → 执行。纯度铁律（ADR-0087 决策 2 + domain 三红线）：零 I/O、时间作
// 参数传入（禁 time.Now——domainpurity 纪律项）、不写 receiver、不
// import genproto。
package bluegreen

import (
	"fmt"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ---- L1 健康门：新代进程组齐备性（engine observations.readyGen 的判定核）----

// Member 是健康门一个进程组成员的观测事实（engine 装配：ensuredGen 圈定
// gen 组成员，锁内取各成员最新观测快照——未观测成员 Observed=false）。
type Member struct {
	WorkloadID string
	// Observed 报告该成员是否已有任何观测（false = 未观测 = 未就绪）。
	Observed bool
	// State/Generation 是最新观测的活状态与代（Observed=false 时零值）。
	State      capability.WorkloadState
	Generation uint64
}

// GroupReady 裁决进程组齐备性：某 App 在指定 Generation 下发的全部
// Workload（ensuredGen 界定）均观测到 running@gen。空集 = 本代零滚动
// （纯 scale / 全跳过——内容锚定等价下的合法形态，ADR-0064）：
// vacuously ready。调用序保证查询前 materialize 已入账（重启冷缓存由
// 首拍全量物化重建——C17 备忘同进程内存态，冷启动首拍必全量物化），
// 空集不可能是缓存未建。
func GroupReady(members []Member, gen uint64) bool {
	for _, m := range members {
		// 就绪门：全部成员必须已观测且 running@gen（未观测 = 未就绪，
		// 与看门狗语义相反——后者未观测不咬合）。
		if !m.Observed || m.State != capability.WorkloadRunning || m.Generation != gen {
			return false
		}
	}
	return true
}

// ---- 健康门三态裁决（engine release/rollback 观测拍判定块）----

// Verdict 是健康门一拍的裁决三态（互斥；晋升/回退/保持，ADR-0087 决策 7
// 单元词汇）。各调用位的执行动作由 engine 映射（见两个 Adjudicate 的
// 裁决表注释）。
type Verdict uint8

const (
	// Hold：保持——等观测或到期（tick 再进；含截止缺席时的 engine 补设
	// 截止动作，补设是行写不在域内）。
	Hold Verdict = iota
	// Promote：晋升——健康门通过（就绪位 = releasing→observing 迁移即在服
	// 代翻转；观察窗位 = 收口 succeeded）。
	Promote
	// Rollback：回退——门失败收口（就绪位 = L1 超时回退；回放位 =
	// rollbackFailed 终态；观察窗位 = L2 咬合失败回滚）。
	Rollback
)

// String 返回裁决字面（测试/日志可读面）。
func (v Verdict) String() string {
	switch v {
	case Hold:
		return "hold"
	case Promote:
		return "promote"
	case Rollback:
		return "rollback"
	}
	return "unknown"
}

// AdjudicateGate 裁决就绪门的观测拍（engine drive.go release/rollback 两
// 调用位共用同一张表；装配 = 就绪事实〔GroupReady〕+ 截止时刻 + 当前
// 时刻）：
//
//	就绪                    → Promote（就绪优先于超时：迟到但就绪仍是晋升。
//	                           release 位 = 切换步，releasing→observing 迁移
//	                           即在服代翻转 + 即时 Route 重发布；rollback 位 =
//	                           回放就绪，rolling-back→observing 收口）
//	截止缺席                → Hold（engine 侧补设 L1 截止后 tick 再进——
//	                           release 位先补设再进裁决，rollback 位就绪快径
//	                           先行，两位对缺席的处置序差异是 engine 编排）
//	未就绪 × now > deadline → Rollback（L1 超时：release 位 = 新代失败回退
//	                           〔blue-green 走回滚零重建，rolling 走 failed〕；
//	                           rollback 位 = rollbackFailed 终态）
//	其余                    → Hold（窗内等观测，tick 再进）
//
// 边界：now == deadline = 未超（After 严格大于）——截止拍仍可等观测，
// 超时收口从下一拍起。
func AdjudicateGate(ready bool, deadline *time.Time, now time.Time) Verdict {
	if ready {
		return Promote
	}
	if deadline == nil || !now.After(*deadline) {
		return Hold
	}
	return Rollback
}

// AdjudicateObservation 裁决 L3 观察窗的观测拍（engine drive.go observe
// 判定块；装配 = 看门狗咬合描述〔WatchdogBite 首个非空〕+ 窗截止 + 当前
// 时刻）：
//
//	看门狗咬合（非空）→ Rollback（L2 劣化：observing → failed；BG 的切回
//	                     语义由 failed 态的服务代推导 + rolling-back 在基线
//	                     gen 重放兑现，ADR-0048 决策 1.2）
//	窗截止缺席        → Hold（engine 侧补设观察窗——崩溃窗口/半途行）
//	now < deadline    → Hold（窗内 tick 再进）
//	窗到期            → Promote（收口晋升：blue-green = Ensure 仅新代——
//	                     旧代载体由域内收敛移除、stable 别名随旧代退役新代
//	                     继承；rolling 直收 succeeded）
//
// 边界：now == deadline = 到期（Before 严格小于）——与就绪门的闭边界
// 互为镜像（观察窗到期即收口，不留死拍）。
func AdjudicateObservation(bite string, deadline *time.Time, now time.Time) Verdict {
	if bite != "" {
		return Rollback
	}
	if deadline == nil || now.Before(*deadline) {
		return Hold
	}
	return Promote
}

// ---- L2 看门狗咬合判定（engine watchdogBite 的判定核）----

// WatchdogBite 裁决 L2 看门狗对单条成员观测的咬合：当前 Generation 观测
// 到 stopped → 咬合描述（engine 扫描面取首个咬合即收口）；空串 = 未
// 咬合。旧代事件不咬合：观测槽按 Workload ID last-write-wins，滚动更新
// 期旧 task 的 stopped@旧代 迟到事件会覆盖 running@当前代——咬合只认
// 当前代（假咬合会让滚动更新秒败进回滚，F0.12 回归钉死）。未观测的
// Workload 不咬合（engine 扫描面跳过——等待/超时路径处理）。
func WatchdogBite(ev capability.WorkloadEvent, gen uint64) string {
	if ev.State == capability.WorkloadStopped && uint64(ev.Generation) == gen {
		return fmt.Sprintf("workload %s stopped (gen %d): %s", ev.WorkloadID, ev.Generation, ev.Message)
	}
	return ""
}

// ---- 双代窗代差判定：基线锚解析 + 在服代推导 ----

// BaselineGeneration 解析双代窗的旧代锚 gen（engine baselineGeneration 的
// 判定核；装配 = 行事实 + engine 实取的最近 succeeded 行）：
//
//	行上 FromGeneration（受理位从最近 succeeded 行取）优先
//	FromRevision 为空（首次部署）               → 0
//	最近 succeeded 行缺席（实取失败/无成功行）   → 0
//	最近 succeeded 行 ToRevision ≠ FromRevision → 0（行锚与最新成功行
//	  对不上时不采——宁可无锚退化，不猜基线）
//	否则 → 最近 succeeded 行 Generation（列加入前的存量行回退实取——
//	  迁移零回填，首个 blue-green 部署起自然落列）
//
// 0 = 无基线代，窗口退化为单代（首次部署形态）。
func BaselineGeneration(d *deployment.Deployment, latest *deployment.Deployment) uint64 {
	if d.FromGeneration > 0 {
		return d.FromGeneration
	}
	if d.FromRevision == "" || latest == nil || latest.ToRevision != d.FromRevision {
		return 0
	}
	return latest.Generation
}

// ServingGen 推导一行在途部署的在服代（ADR-0048 决策 1.2 切换步的状态化：
// 观测中的部署已把流量交给本代，服务代 = 本代 gen；其余在途（含 failed
// 待回滚/rolling-back）服务代 = 基线代——切回。部署行是唯一真源：重启/
// 抢占/回放的翻转自洽，无内存第二真源）。
func ServingGen(d *deployment.Deployment) uint64 {
	if d.State == deployment.StateObserving {
		return d.Generation
	}
	return d.FromGeneration
}
