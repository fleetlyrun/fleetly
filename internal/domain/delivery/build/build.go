// Package build 是 Build 聚合的领域面（CONTEXT.md Build 词条：从 Source
// 产出镜像的过程记录，输出不可变 digest）。状态机 queued → building →
// succeeded | failed | cancelled | expired（超时看门狗）由 engine 持有，
// repo 只承接持久化与 CAS。行类型、状态机词表与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/build（ADR-0075 Repo 接口层
// 路线）。
package build

// State 是 Build 状态机值（领域模型 §4）。
type State string

const (
	StateQueued    State = "queued"
	StateBuilding  State = "building"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
	StateExpired   State = "expired" // 超时看门狗收口
)

// Terminal 报告是否终态。
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateExpired:
		return true
	}
	return false
}

// Build 是聚合行（构建记录永不删）。
type Build struct {
	ID         string
	Seq        int64
	AppID      string
	RevisionID string
	State      State
	Digest     string
	// Repo 是产物所在仓库名（不含 registry 地址，<projectID>/<appID> 前
	// 纲；空 = 存量扁平布局 <appID>——投影期回退，ADR-0036 N2 兑现节 2）。
	// 创建时定型不可变：与 digest 同为产物定位事实。
	Repo       string
	Error      string
	CreatedAt  string
	UpdatedAt  string
	FinishedAt string
}
