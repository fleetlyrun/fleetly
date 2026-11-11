package engine

// 输入面契约别名（P3-7 收口）：DTO/哨兵/输入面原语真源迁至
// internal/application/engine/api，engine 核与既有消费方经别名/声明委托
// 零漂移——方法签名与错误文案逐字节保持，别名不产生第二真源。

import "github.com/fleetlyrun/fleetly/internal/application/engine/api"

// 部署受理 DTO 与判定附注（真源 engine/api）。
type (
	MergeMode              = api.MergeMode
	SubmitSpecRequest      = api.SubmitSpecRequest
	SubmitOptions          = api.SubmitOptions
	FrozenSpecError        = api.FrozenSpecError
	AdmissionError         = api.AdmissionError
	AdmissionOutcome       = api.AdmissionOutcome
	Admission              = api.Admission
	ConfirmInput           = api.ConfirmInput
	PreviewSpecResult      = api.PreviewSpecResult
	TeardownCascadeEntry   = api.TeardownCascadeEntry
	NetworkRebuildResult   = api.NetworkRebuildResult
	ForeignAttachmentError = api.ForeignAttachmentError
)

// 部署受理常量（真源 engine/api）。
const (
	MergeNone     = api.MergeNone
	MergeMaterial = api.MergeMaterial
	MergeOverlay  = api.MergeOverlay

	AdmissionQueued       = api.AdmissionQueued
	AdmissionMerged       = api.AdmissionMerged
	AdmissionSuperseded   = api.AdmissionSuperseded
	AdmissionDeduplicated = api.AdmissionDeduplicated

	MaxTasksPerProject           = api.MaxTasksPerProject
	MaxTaskConcurrencyPerProject = api.MaxTaskConcurrencyPerProject

	EventTaskCreated     = api.EventTaskCreated
	EventScheduleCreated = api.EventScheduleCreated
)

// 事件载荷类型别名（schemareg 注册面与核内既名零改；真源 engine/api）。
type (
	taskEventPayload     = api.TaskEventPayload
	runEventPayload      = api.RunEventPayload
	scheduleEventPayload = api.ScheduleEventPayload
)

// 输入面哨兵（api/assembly 层 errors.Is 判定零改；真源 engine/api）。
var (
	ErrQueueFull                  = api.ErrQueueFull
	ErrNotCancellable             = api.ErrNotCancellable
	ErrActiveDeployment           = api.ErrActiveDeployment
	ErrNoSuccessfulBaseline       = api.ErrNoSuccessfulBaseline
	ErrCrossProjectRefNotApproved = api.ErrCrossProjectRefNotApproved
	ErrPreDeployNetworkUnknown    = api.ErrPreDeployNetworkUnknown
	ErrConfirmStale               = api.ErrConfirmStale
	ErrConfirmMismatch            = api.ErrConfirmMismatch
	ErrTaskNotRenewable           = api.ErrTaskNotRenewable
	ErrTaskTerminal               = api.ErrTaskTerminal
	ErrTaskDraining               = api.ErrTaskDraining
	ErrNotResident                = api.ErrNotResident
	ErrTaskQuota                  = api.ErrTaskQuota
	ErrScheduleTerminal           = api.ErrScheduleTerminal
	ErrScheduleOverlapping        = api.ErrScheduleOverlapping
	ErrProcessUnknown             = api.ErrProcessUnknown
	ErrRuntimeNoInspect           = api.ErrRuntimeNoInspect
	ErrRuntimeNoRestart           = api.ErrRuntimeNoRestart
	ErrVolumeUnresolved           = api.ErrVolumeUnresolved
	ErrLegacyVolumeCarrier        = api.ErrLegacyVolumeCarrier
)

// 输入面原语（真源 engine/api；核内调用点既名零改）。
var (
	CheckTaskQuota        = api.CheckTaskQuota
	MintTaskRow           = api.MintTaskRow
	TaskDNSName           = api.TaskDNSName
	PreDeployTaskID       = api.PreDeployTaskID
	TeardownCascadeDigest = api.TeardownCascadeDigest

	TaskCreatedEventJSON     = api.TaskCreatedEventJSON
	ScheduleCreatedEventJSON = api.ScheduleCreatedEventJSON
	ScheduleDeletedEventJSON = api.ScheduleDeletedEventJSON
	RunPayloadTaskID         = api.RunPayloadTaskID

	taskEventPayloadJSON     = api.TaskEventPayloadJSON
	scheduleEventPayloadJSON = api.ScheduleEventPayloadJSON
)
