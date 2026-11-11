package api

// Task/Run/Schedule 事件名与载荷构造（真源自 engine 根包迁出，P3-7 输入面
// 契约收口）：受理位与 engine 驱动环共用同一 JSON 形状——字段只增，键名
// 单源在此，消费方不持键知识。

import (
	"encoding/json"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
)

// Task/Run/Schedule 事件名锚定（usage 反扫的字面量命中点）。
const (
	EventTaskCreated     = "task.created"     // API 受理面发射（事件名与 payload 单源在 engine/api）
	EventScheduleCreated = "schedule.created" // API 受理面发射（事件名与 payload 单源在 engine/api）
)

// TaskEventPayload 是 task.* payload schema（F1.5/F1.6；字段只增）。
// draining 起因经 Reason 携带（lease_expired / owner_revoked /
// stopped_by_user）。
type TaskEventPayload struct {
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

// RunEventPayload 是 run.* payload schema（终态帧携带 stop_reason 与
// exit_code）。
type RunEventPayload struct {
	RunID      string `json:"run_id"`
	TaskID     string `json:"task_id"`
	ProjectID  string `json:"project_id"`
	State      string `json:"state"`
	StopReason string `json:"stop_reason,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DNSName    string `json:"dns_name,omitempty"`
	Instance   string `json:"instance,omitempty"`
}

// ScheduleEventPayload 是 schedule.* payload schema（F1.7，ADR-0018；字段
// 只增）。fired 携带 source（cron | manual）与本拍铸出的 last_task_id；
// skipped 携带 reason（overlap）。
type ScheduleEventPayload struct {
	ScheduleID string `json:"schedule_id"`
	ProjectID  string `json:"project_id"`
	Name       string `json:"name,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	NextFireAt string `json:"next_fire_at,omitempty"`
	LastTaskID string `json:"last_task_id,omitempty"`
	Source     string `json:"source,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// TaskCreatedEventJSON 构造 task.created payload（API 受理面消费）。
func TaskCreatedEventJSON(t *task.Task) []byte { return TaskEventPayloadJSON(t, "") }

// ScheduleCreatedEventJSON 构造 schedule.created payload（API 受理面消费）。
func ScheduleCreatedEventJSON(s *schedule.Schedule) []byte {
	return ScheduleEventPayloadJSON(s, "", "")
}

// ScheduleDeletedEventJSON 构造 schedule.deleted payload（API 受理面消费）。
func ScheduleDeletedEventJSON(s *schedule.Schedule) []byte {
	return ScheduleEventPayloadJSON(s, "", "")
}

// TaskEventPayloadJSON 构造 task.* payload（engine 驱动环与受理位共用）。
func TaskEventPayloadJSON(t *task.Task, reason string) []byte {
	b, _ := json.Marshal(TaskEventPayload{
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

// ScheduleEventPayloadJSON 构造 schedule.* payload（engine 驱动环与受理位
// 共用）。
func ScheduleEventPayloadJSON(s *schedule.Schedule, source, reason string) []byte {
	b, _ := json.Marshal(ScheduleEventPayload{
		ScheduleID: s.ID,
		ProjectID:  s.ProjectID,
		Name:       s.Name,
		Timezone:   s.Timezone,
		NextFireAt: s.NextFireAt,
		LastTaskID: s.LastTaskID,
		Source:     source,
		Reason:     reason,
	})
	return b
}

// RunPayloadTaskID 从 run.* 事件 payload 提取 task_id（形状单源：本文件
// RunEventPayload 的 json 键，消费方不持键知识）。API 等待面的任务维度
// 过滤用（WaitRun task_id：run.created 事件是首 Run 铸出的信号，ADR-0063）。
// 非 run.* payload 或缺键返回空串。
func RunPayloadTaskID(payload []byte) string {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return ""
	}
	return p.TaskID
}
