package engine

// runsByTask 是一个 Task 名下驱动 Run 集的有序视图（组内新→旧）。Task
// 域两条行集不变量的唯一拥有点（2026-10-03 架构评审候选 2 收口；此前以
// 注释散在 driveTask 的各消费点——P1#7 方向倒置缺陷与 2026-10-02 缩容
// 缺口都死在"规则只在注释里"）：
//
//   1. 方向锚：新→旧（ULID 字典序降序）——过量排空"停新保老"按此消费，
//      方向倒置即停老保新。序由构造保证（newRunsByTask 自排序），不依赖
//      查询方向契约：两个入口（ListDriving 批量分组、ListByTaskStates
//      补足后重列）都经同一构造。
//   2. 双占用规则：resident 的占用口径 = 活槽位（pending/running；
//      stopping 是收口态不占位）；one-shot 的占用口径 = 全部驱动行
//      （stopping 也占位——唯一 Run 停止收口中即补第二 Run = job 多跑
//      一次，且 Task 终态后第二 Run 成僵尸、Schedule 重叠判定永久
//      skip）。occupiedFor 是判定点。

import (
	"sort"

	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// runsByTask 本体是有序切片（新→旧；头部即最新）。
type runsByTask []run.Run

// newRunsByTask 组织驱动 Run 集为有序视图。输入顺序任意——序由本构造
// 保证（Run ID 是 ULID，时间单调可按字典序排）。
func newRunsByTask(rows []run.Run) runsByTask {
	out := make(runsByTask, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// groupDrivingRunsByTask 把全量 driving Run 行按 Task 分组为有序视图
// （N1 C18：runs.ListDriving 单查询全量后分组，替代逐 Task 点查的 N+1）。
// 僵尸 Run（终态 Task 名下）随分组丢弃，其收口是 sweepZombieRuns 的
// 专属路径。
func groupDrivingRunsByTask(runs []run.Run) map[string]runsByTask {
	groups := map[string][]run.Run{}
	for i := range runs {
		groups[runs[i].TaskID] = append(groups[runs[i].TaskID], runs[i])
	}
	out := make(map[string]runsByTask, len(groups))
	for id, g := range groups {
		out[id] = newRunsByTask(g)
	}
	return out
}

// liveCount 报活槽位数（pending/running）——resident 的占用口径与过量
// 排空的守门量。
func (v runsByTask) liveCount() int {
	n := 0
	for i := range v {
		if v[i].State.Active() {
			n++
		}
	}
	return n
}

// occupiedFor 报指定形态下的占用数（双占用规则唯一判定点；form 取
// task.FormOneShot / task.FormResident）。
func (v runsByTask) occupiedFor(form string) int {
	if form == task.FormOneShot {
		return len(v)
	}
	return v.liveCount()
}

// drainAnchorReason 报排空补停的触发锚起因：任一 stopping 行携带的
// stop_reason（行事实真源——排空发起时的语义已落行，重启后仍可判；
// 全无 stopping 行返回零值 = 宽限排空未发起过，补停不触发）。
func (v runsByTask) drainAnchorReason() string {
	for i := range v {
		if v[i].State == run.StateStopping && v[i].StopReason != "" {
			return v[i].StopReason
		}
	}
	return ""
}
