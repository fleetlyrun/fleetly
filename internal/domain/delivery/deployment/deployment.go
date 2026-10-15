// Package deployment 是 Deployment 聚合的领域面（CONTEXT.md Deployment 词条：
// 从旧 Revision 到新 Revision 的受监督迁移）：行类型、状态机词表与 Repo
// 接口在此单源，SQLite 实现住 infrastructure/persistence/deployment
// （ADR-0075 Repo 接口层路线）。行本身即 admission 持久真源（state=queued，
// 2026-09-30 裁决：库为真源 + 进程内索引，ADR-0016）；状态迁移一律经
// Transit CAS（四件一拍的"状态"件，Outbox/审计由调用方同事务组合）。
package deployment

import (
	"strconv"
	"strings"
)

// State 是 Deployment 状态机值（领域模型 §4；含 admission 排队态）。
type State string

const (
	StateQueued      State = "queued"       // admission 已受理，等待单写者拾取
	StatePreparing   State = "preparing"    // Spec 归一化/材料装配/前置 Job
	StateBuilding    State = "building"     // 构建中（镜像引用可跳过）
	StateReleasing   State = "releasing"    // 投影 Workload 下发 Runtime（L1 门）
	StateObserving   State = "observing"    // L3 观察窗 + L2 看门狗常驻
	StateSucceeded   State = "succeeded"    // 终态
	StateFailed      State = "failed"       // 终态（自动触发回滚）
	StateRollingBack State = "rolling-back" // Replay 上一成功 Revision
	StateSuperseded  State = "superseded"   // 被显式 supersede 抢占的终态
	StateCancelled   State = "cancelled"    // 排队中/在途被取消的终态
)

// Terminal 报告是否终态（终态行不再参与 admission 与单写者驱动）。
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateSuperseded, StateCancelled:
		return true
	}
	return false
}

// Active 报告是否活跃态（admission 去重窗口：排队 + 在途 + 回滚中）。
func (s State) Active() bool {
	return s != "" && !s.Terminal()
}

// ActiveStates 是全部活跃态。
func ActiveStates() []State {
	return []State{
		StateQueued, StatePreparing, StateBuilding,
		StateReleasing, StateObserving, StateRollingBack,
	}
}

// ActiveStateStrings 是活跃态的 SQL IN 参数形态。
func ActiveStateStrings() []string {
	out := make([]string, len(ActiveStates()))
	for i, s := range ActiveStates() {
		out[i] = string(s)
	}
	return out
}

// ActiveStatesNoQueued 是在途态（admission 显式 supersede 的抢占面：
// preparing..rolling-back；queued 由 latest-wins 合并处理）。
func ActiveStatesNoQueued() []State {
	return []State{
		StatePreparing, StateBuilding, StateReleasing, StateObserving, StateRollingBack,
	}
}

// Deployment 是聚合行（部署记录永不删——审计单位）。
type Deployment struct {
	ID                string
	Seq               int64
	AppID             string
	FromRevision      string // 上一 Revision ID（首次部署为空）
	ToRevision        string // 目标 Revision ID
	State             State
	Generation        uint64 // 本 Deployment 拟下发的 Generation（单调）
	FromGeneration    uint64 // 基线 Generation（最近 succeeded 行的 gen；ADR-0048 双代窗的旧代锚——窗口 Ensure 调用 gen 与服务代推导共用；0 = 无基线（首次部署/存量行））
	IdempotencyKey    string
	CommitSHA         string
	SupersededBy      string // 被哪个 Deployment 抢占（终态 superseded 时非空）
	Error             string // 失败原因（英文，用户可见）
	ObserveDeadline   string // 当前阶段绝对截止（job 等待 / L1 就绪等待 / L3 观察窗；RFC3339，空 = 不在限时阶段；消歧真源 = FirstBoot 游标，ADR-0030）
	FirstBoot         string // firstBootJobs 游标（'' 未开始 | '<idx>:<taskID>' 等待中 | 'done' 全部完成；ADR-0030）
	RollbackAttempted bool   // failed 自动回滚已尝试（=1 的 failed 是终态，不再自动重试）
	CreatedAt         string
	UpdatedAt         string
	FinishedAt        string
}

// firstBootDone 是 first_boot 游标的完成哨兵（区分"未开始"空值）。
const FirstBootDone = "done"

// FirstBootAnchor 解析游标：返回 (job 下标, 锚定 Task ID, 是否等待中)。
// 'done' 与非法形态返回 ok=false（等待外形态由调用方按序处理）。
func (d *Deployment) FirstBootAnchor() (idx int, taskID string, ok bool) {
	if d.FirstBoot == "" || d.FirstBoot == FirstBootDone {
		return 0, "", false
	}
	head, tail, found := strings.Cut(d.FirstBoot, ":")
	if !found || tail == "" {
		return 0, "", false
	}
	n, err := strconv.Atoi(head)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, tail, true
}
