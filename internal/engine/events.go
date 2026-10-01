package engine

import (
	"encoding/json"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// 事件 payload schema（Outbox payload 列；JSON、字段只增）。
//
// deployment.* 的统一形态——state 是事件对应的状态机值（snake_case）：
type deploymentEventPayload struct {
	DeploymentID    string `json:"deployment_id"`
	AppID           string `json:"app_id"`
	State           string `json:"state"`
	FromRevision    string `json:"from_revision,omitempty"`
	ToRevision      string `json:"to_revision,omitempty"`
	Generation      uint64 `json:"generation"`
	Error           string `json:"error,omitempty"`
	SupersededBy    string `json:"superseded_by,omitempty"`
	ObserveDeadline string `json:"observe_deadline,omitempty"`
	Kind            string `json:"kind,omitempty"`
}

// workload.drift_detected（per-Workload 粒度 + 去抖，2026-09-30 裁决；
// ADR-0022 起 gen 偏离与 spec 失配双路径共用）。
type driftEventPayload struct {
	WorkloadID         string `json:"workload_id"`
	AppID              string `json:"app_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	ObservedGeneration uint64 `json:"observed_generation"`
	ObservedState      string `json:"observed_state"`
	Message            string `json:"message,omitempty"`
}

// workload.stopped（ADR-0022 稳态看门狗：最近部署 succeeded 的 App 在
// 当前 Generation 观测到 stopped——只观测不迁移，处置由人/Agent 决定）。
type stoppedEventPayload struct {
	WorkloadID string `json:"workload_id"`
	AppID      string `json:"app_id"`
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
}

// node.joined / node.left（nodes 表是观测缓存，事件是订阅面真源）。
type nodeEventPayload struct {
	NodeID    string `json:"node_id"`
	CarrierID string `json:"carrier_id,omitempty"`
	Minted    bool   `json:"minted,omitempty"`
}

// 事件名锚定（usage 反扫的字面量命中点）。
const (
	eventWorkloadDrift   = "workload.drift_detected"
	eventWorkloadStopped = "workload.stopped"
	eventNodeJoined      = "node.joined"
)

// build.* payload schema。
type buildEventPayload struct {
	BuildID    string `json:"build_id"`
	AppID      string `json:"app_id"`
	RevisionID string `json:"revision_id,omitempty"`
	State      string `json:"state"`
	Digest     string `json:"digest,omitempty"`
	Error      string `json:"error,omitempty"`
}

// task.* payload schema（F1.5/F1.6；字段只增）。draining 起因经 Reason
// 携带（lease_expired / owner_revoked / stopped_by_user）。
type taskEventPayload struct {
	TaskID             string `json:"task_id"`
	ProjectID          string `json:"project_id"`
	Form               string `json:"form"`
	State              string `json:"state"`
	Name               string `json:"name,omitempty"`
	DesiredConcurrency int64  `json:"desired_concurrency,omitempty"`
	DNSName            string `json:"dns_name,omitempty"`
	OwnerTokenID       string `json:"owner_token_id,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// run.* payload schema（终态帧携带 stop_reason 与 exit_code）。
type runEventPayload struct {
	RunID      string `json:"run_id"`
	TaskID     string `json:"task_id"`
	ProjectID  string `json:"project_id"`
	State      string `json:"state"`
	StopReason string `json:"stop_reason,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DNSName    string `json:"dns_name,omitempty"`
	Instance   string `json:"instance,omitempty"`
}

// lease.* payload schema（Owner Lease 事实；ADR-0018 绝对 deadline RFC3339）。
type leaseEventPayload struct {
	TaskID   string `json:"task_id"`
	Deadline string `json:"deadline,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Task/Run/Lease 事件名锚定（usage 反扫的字面量命中点）。
const (
	EventTaskCreated  = "task.created" // API 受理面发射（事件名与 payload 单源在 engine）
	eventTaskUpdated  = "task.updated"
	eventTaskDraining = "task.draining"
	eventTaskDeleted  = "task.deleted"
	eventLeaseRenewed = "lease.renewed"
	eventLeaseExpired = "lease.expired"
)

// TaskCreatedEventJSON 构造 task.created payload（API 受理面消费）。
func TaskCreatedEventJSON(t *task.Task) []byte { return taskEventPayloadJSON(t, "") }

// eventTaskState 把 Task 状态映射为事件名（字面量锚定）。
func eventTaskState(s task.State) string {
	switch s {
	case task.StateActive:
		return "task.active" // 复活迁移（RenewTask 的 draining/drained → active）
	case task.StateDraining:
		return eventTaskDraining
	case task.StateCompleted:
		return "task.completed"
	case task.StateFailed:
		return "task.failed"
	case task.StateDrained:
		return "task.drained"
	case task.StateDeleted:
		return eventTaskDeleted
	default:
		return "task." + strings.ReplaceAll(string(s), "-", "_")
	}
}

// eventRunState 把 Run 状态映射为事件名（字面量锚定）。
func eventRunState(s run.State) string {
	switch s {
	case run.StatePending:
		return "run.created"
	case run.StateRunning:
		return "run.running"
	case run.StateStopping:
		return "run.stopping"
	case run.StateStopped:
		return "run.stopped"
	case run.StateFailed:
		return "run.failed"
	default:
		return "run." + strings.ReplaceAll(string(s), "-", "_")
	}
}

func taskEventPayloadJSON(t *task.Task, reason string) []byte {
	b, _ := json.Marshal(taskEventPayload{
		TaskID:             t.ID,
		ProjectID:          t.ProjectID,
		Form:               t.Form,
		State:              string(t.State),
		Name:               t.Name,
		DesiredConcurrency: t.DesiredConcurrency,
		DNSName:            t.DNSName,
		OwnerTokenID:       t.OwnerTokenID,
		Reason:             reason,
	})
	return b
}

func runEventPayloadJSON(m *run.Run) []byte {
	b, _ := json.Marshal(runEventPayload{
		RunID:      m.ID,
		TaskID:     m.TaskID,
		ProjectID:  m.ProjectID,
		State:      string(m.State),
		StopReason: m.StopReason,
		ExitCode:   m.ExitCode,
		DNSName:    m.DNSName,
	})
	return b
}

func leaseEventPayloadJSON(t *task.Task, reason string) []byte {
	b, _ := json.Marshal(leaseEventPayload{
		TaskID:   t.ID,
		Deadline: t.LeaseDeadline,
		Reason:   reason,
	})
	return b
}

func buildEventPayloadJSON(b *build.Build) []byte {
	payload := buildEventPayload{
		BuildID: b.ID, AppID: b.AppID, RevisionID: b.RevisionID,
		State: string(b.State), Digest: b.Digest, Error: b.Error,
	}
	out, _ := json.Marshal(payload)
	return out
}

// eventBuildState 把 Build 状态映射为事件名（字面量锚定）。
func eventBuildState(s build.State) string {
	switch s {
	case build.StateQueued:
		return "build.queued"
	case build.StateBuilding:
		return "build.building"
	case build.StateSucceeded:
		return "build.succeeded"
	case build.StateFailed:
		return "build.failed"
	case build.StateCancelled:
		return "build.cancelled"
	case build.StateExpired:
		return "build.expired"
	default:
		return "build." + strings.ReplaceAll(string(s), "-", "_")
	}
}

func deploymentEventPayloadJSON(d *deployment.Deployment) []byte {
	p := deploymentEventPayload{
		DeploymentID:    d.ID,
		AppID:           d.AppID,
		State:           eventStateName(d.State),
		FromRevision:    d.FromRevision,
		ToRevision:      d.ToRevision,
		Generation:      d.Generation,
		Error:           d.Error,
		SupersededBy:    d.SupersededBy,
		ObserveDeadline: d.ObserveDeadline,
	}
	b, _ := json.Marshal(p) // 纯标量结构，Marshal 不失败；失败即编程错误
	return b
}

func driftEventPayloadJSON(ev capability.WorkloadEvent, appID string, expected uint64) []byte {
	b, _ := json.Marshal(driftEventPayload{
		WorkloadID:         ev.WorkloadID,
		AppID:              appID,
		ExpectedGeneration: expected,
		ObservedGeneration: uint64(ev.Generation),
		ObservedState:      string(ev.State),
		Message:            ev.Message,
	})
	return b
}

func stoppedEventPayloadJSON(wid, appID string, ev capability.WorkloadEvent) []byte {
	b, _ := json.Marshal(stoppedEventPayload{
		WorkloadID: wid,
		AppID:      appID,
		Generation: uint64(ev.Generation),
		State:      string(ev.State),
		Message:    ev.Message,
	})
	return b
}

func nodeJoinedPayloadJSON(nodeID, carrierID string, minted bool) []byte {
	b, _ := json.Marshal(nodeEventPayload{NodeID: nodeID, CarrierID: carrierID, Minted: minted})
	return b
}

func nodeLeftPayloadJSON(nodeID, carrierID string) []byte {
	b, _ := json.Marshal(nodeEventPayload{NodeID: nodeID, CarrierID: carrierID})
	return b
}

// eventStateName 把状态值映射为事件名（usage 反扫锚：每个事件名以字面量
// 出现在生产代码）。rolling-back → rolling_back：事件名 pattern 禁连字符，
// 状态存储保持 kebab——两套拼写各自冻结。
func eventStateName(s deployment.State) string {
	switch s {
	case deployment.StateQueued:
		return "deployment.queued"
	case deployment.StatePreparing:
		return "deployment.preparing"
	case deployment.StateBuilding:
		return "deployment.building"
	case deployment.StateReleasing:
		return "deployment.releasing"
	case deployment.StateObserving:
		return "deployment.observing"
	case deployment.StateSucceeded:
		return "deployment.succeeded"
	case deployment.StateFailed:
		return "deployment.failed"
	case deployment.StateRollingBack:
		return "deployment.rolling_back"
	case deployment.StateSuperseded:
		return "deployment.superseded"
	case deployment.StateCancelled:
		return "deployment.cancelled"
	default:
		return "deployment." + strings.ReplaceAll(string(s), "-", "_")
	}
}
