package engine

import (
	"encoding/json"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
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
}

// workload.drift_detected（per-Workload 粒度 + 去抖，2026-09-30 裁决）。
type driftEventPayload struct {
	WorkloadID         string `json:"workload_id"`
	AppID              string `json:"app_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
	ObservedGeneration uint64 `json:"observed_generation"`
	ObservedState      string `json:"observed_state"`
	Message            string `json:"message,omitempty"`
}

// node.joined / node.left（nodes 表是观测缓存，事件是订阅面真源）。
type nodeEventPayload struct {
	NodeID    string `json:"node_id"`
	CarrierID string `json:"carrier_id,omitempty"`
	Minted    bool   `json:"minted,omitempty"`
}

// 事件名锚定（usage 反扫的字面量命中点）。
const (
	eventWorkloadDrift = "workload.drift_detected"
	eventNodeJoined    = "node.joined"
)

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

func nodeJoinedPayloadJSON(nodeID, carrierID string, minted bool) []byte {
	b, _ := json.Marshal(nodeEventPayload{NodeID: nodeID, CarrierID: carrierID, Minted: minted})
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
