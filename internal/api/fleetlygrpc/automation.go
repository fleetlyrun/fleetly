package fleetlygrpc

// Automation 上下文服务实现（F1.5/F1.6，ADR-0012/0025）：Task/Run 聚合面。
// 创建走受理位（ADR-0024：父资源存活 + 归一化校验 + 统一写原语）；生命
// 周期动词（scale/stop/renew/delete）在 engine（驱动环互斥与事件四件一拍）。

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/run"
	"github.com/fleetlyrun/fleetly/internal/state/schedule"
	"github.com/fleetlyrun/fleetly/internal/state/task"
)

// maxTaskConcurrency 是 desired_concurrency 的受理位 sanity 域（治理配额
// 归 F1.9/ADR-0017；此处只拦误写）。
const maxTaskConcurrency = 100

type TasksService struct {
	automationv1.UnimplementedTasksServiceServer
	s *Services
}

// normalizeCreateTask 归一化创建源：直投镜像 + Variables + secretRefs →
// 冻结 TaskSpec（schemaVersion 由平台钉当前版）。form 空 = 按并发推导。
func normalizeCreateTask(req *automationv1.CreateTaskRequest, taskID string) (*specv1.TaskSpec, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if req.GetImage() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "image: must not be empty (tasks deploy images directly; build sources are an app surface)")
	}
	if req.GetForm() != "" && req.GetForm() != spec.FormOneShot && req.GetForm() != spec.FormResident {
		return nil, apperr.New("E_INVALID_ARGUMENT", "form: must be one of \"one-shot\" or \"resident\"")
	}
	if req.GetCpuMillis() < 0 || req.GetMemoryMb() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "cpu_millis/memory_mb: must not be negative")
	}
	if req.GetDesiredConcurrency() < 0 || req.GetDesiredConcurrency() > maxTaskConcurrency {
		return nil, apperr.New("E_INVALID_ARGUMENT", "desired_concurrency: must be within [0, %d]", int64(maxTaskConcurrency))
	}
	if req.GetTtlSeconds() < 0 || req.GetTtlSeconds() > 86400 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "ttl_seconds: must be within [0, 86400] (ADR-0018)")
	}
	p := &specv1.ProcessSpec{
		Name:        "run",
		ImageOrigin: &specv1.ProcessSpec_Image{Image: req.GetImage()},
		Command:     req.GetCommand(),
		Env:         req.GetEnv(),
		SecretRefs:  req.GetSecretRefs(),
	}
	if req.GetCpuMillis() > 0 || req.GetMemoryMb() > 0 {
		p.Resources = &specv1.ResourcesSpec{CpuMillis: req.GetCpuMillis(), MemoryMb: req.GetMemoryMb()}
	}
	form := req.GetForm()
	if form == "" {
		form = spec.FormOneShot
		if req.GetDesiredConcurrency() > 1 {
			form = spec.FormResident
		}
	}
	desired := req.GetDesiredConcurrency()
	if desired == 0 && form == spec.FormOneShot {
		desired = 1 // one-shot 缺省 1（resident 显式 0 = 合法缩零初态）
	}
	s := &specv1.TaskSpec{
		SchemaVersion:      spec.SchemaVersion,
		Task:               &specv1.TaskRef{Id: taskID, Project: req.GetProjectId()},
		Process:            p,
		TtlSeconds:         req.GetTtlSeconds(),
		OwnerToken:         req.GetOwnerTokenId(),
		NetworkGroup:       req.GetNetworkGroup(),
		DesiredConcurrency: desired,
		Form:               form,
	}
	if err := spec.ValidateTask(s); err != nil {
		return nil, mapValidationError(err)
	}
	return s, nil
}

// resolveOwnerToken 解析属主 Token 引用（缺省 = 调用方 Token；显式引用须
// 存活——吊销 Token 不能再当新 Task 的属主）。
func (svc *TasksService) resolveOwnerToken(ctx context.Context, req *automationv1.CreateTaskRequest) (string, error) {
	if id := req.GetOwnerTokenId(); id != "" {
		tok, err := svc.s.Tokens.Get(ctx, svc.s.DB.Runner(), id)
		if err != nil {
			return "", mapStateError(err, "owner token")
		}
		if tok.Revoked {
			return "", apperr.New("E_INVALID_ARGUMENT", "owner_token_id %s is revoked", id)
		}
		return id, nil
	}
	if id, ok := authn.FromContext(ctx); ok && id.TokenID != "" {
		return id.TokenID, nil
	}
	return "", nil // 匿名/引导面：无属主（resident 需显式属主才有租约保温）
}

// CreateTask 受理 + 冻结 Spec + 落行（四件一拍：task.created 事件 + 审计）。
func (svc *TasksService) CreateTask(ctx context.Context, req *automationv1.CreateTaskRequest) (*automationv1.CreateTaskResponse, error) {
	owner, err := svc.resolveOwnerToken(ctx, req)
	if err != nil {
		return nil, err
	}
	taskID := newID()
	taskSpec, err := normalizeCreateTask(req, taskID)
	if err != nil {
		return nil, err
	}
	taskSpec.OwnerToken = owner
	body, err := marshalSpec(taskSpec)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "task spec: %s", err.Error()).WithCause(err)
	}
	row := &task.Task{
		ID: taskID, ProjectID: req.GetProjectId(), Name: req.GetName(),
		Form: spec.TaskForm(taskSpec), State: task.StateActive, Spec: body,
		OwnerTokenID: owner, DesiredConcurrency: taskSpec.GetDesiredConcurrency(),
		NetworkGroup: req.GetNetworkGroup(), DNSName: engine.TaskDNSName(taskID),
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Tasks.Create(ctx, tx, row)
		},
		events: []eventFact{{
			// payload 的字段全部在行上（时间戳不在 payload 面）——静态
			// 构造与 write 后求值等价。
			name: engine.EventTaskCreated, aggregate: "task", id: row.ID,
			payload: engine.TaskCreatedEventJSON(row),
		}},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "task.create", Resource: "task/" + row.ID, AfterFP: row.Form,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "task")
	}
	svc.s.Engine.KickTasks()
	return &automationv1.CreateTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) GetTask(ctx context.Context, req *automationv1.GetTaskRequest) (*automationv1.GetTaskResponse, error) {
	row, err := svc.s.Tasks.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "task")
	}
	return &automationv1.GetTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) ListTasks(ctx context.Context, req *automationv1.ListTasksRequest) (*automationv1.ListTasksResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	rows, err := svc.s.Tasks.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterTaskId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "task")
	}
	counts, err := svc.s.Runs.CountActiveByTasks(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "run")
	}
	out := &automationv1.ListTasksResponse{}
	for i := range rows {
		msg := svc.s.taskMsg(&rows[i])
		msg.ActiveRunCount = int64(counts[rows[i].ID])
		out.Tasks = append(out.Tasks, msg)
	}
	return out, nil
}

func (svc *TasksService) ScaleTask(ctx context.Context, req *automationv1.ScaleTaskRequest) (*automationv1.ScaleTaskResponse, error) {
	if req.GetDesiredConcurrency() < 0 || req.GetDesiredConcurrency() > maxTaskConcurrency {
		return nil, apperr.New("E_INVALID_ARGUMENT", "desired_concurrency: must be within [0, %d]", int64(maxTaskConcurrency))
	}
	row, err := svc.s.Engine.ScaleTask(ctx, req.GetId(), req.GetDesiredConcurrency())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.ScaleTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) StopTask(ctx context.Context, req *automationv1.StopTaskRequest) (*automationv1.StopTaskResponse, error) {
	row, err := svc.s.Engine.StopTask(ctx, req.GetId(), req.GetForce())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.StopTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) DeleteTask(ctx context.Context, req *automationv1.DeleteTaskRequest) (*automationv1.DeleteTaskResponse, error) {
	if err := svc.s.Engine.DeleteTask(ctx, req.GetId()); err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.DeleteTaskResponse{}, nil
}

func (svc *TasksService) RenewTask(ctx context.Context, req *automationv1.RenewTaskRequest) (*automationv1.RenewTaskResponse, error) {
	row, err := svc.s.Engine.RenewTask(ctx, req.GetId())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.RenewTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

// ---- Runs ----

type RunsService struct {
	automationv1.UnimplementedRunsServiceServer
	s *Services
}

func (svc *RunsService) GetRun(ctx context.Context, req *automationv1.GetRunRequest) (*automationv1.GetRunResponse, error) {
	row, err := svc.s.Runs.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "run")
	}
	return &automationv1.GetRunResponse{Run: runMsg(row)}, nil
}

func (svc *RunsService) ListRuns(ctx context.Context, req *automationv1.ListRunsRequest) (*automationv1.ListRunsResponse, error) {
	var rows []run.Run
	var err error
	if tid := req.GetTaskId(); tid != "" {
		rows, err = svc.s.Runs.ListByTask(ctx, svc.s.DB.Runner(), tid, req.GetAfterRunId(), listLimit(req.GetLimit()))
	} else {
		rows, err = svc.s.Runs.ListAfter(ctx, svc.s.DB.Runner(), req.GetAfterRunId(), listLimit(req.GetLimit()))
	}
	if err != nil {
		return nil, mapStateError(err, "run")
	}
	out := &automationv1.ListRunsResponse{}
	for i := range rows {
		out.Runs = append(out.Runs, runMsg(&rows[i]))
	}
	return out, nil
}

func (svc *RunsService) StopRun(ctx context.Context, req *automationv1.StopRunRequest) (*automationv1.StopRunResponse, error) {
	row, err := svc.s.Engine.StopRun(ctx, req.GetId())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.StopRunResponse{Run: runMsg(row)}, nil
}

// WaitRun：逐状态快照帧，终态（stopped/failed）帧后收流（F1.3 收口面——
// 复用 subscribeEvents 单一订阅核心，行重读是真源）。
func (svc *RunsService) WaitRun(req *automationv1.WaitRunRequest, stream automationv1.RunsService_WaitRunServer) error {
	if req.GetId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "id: must not be empty")
	}
	ctx := stream.Context()
	last := ""
	send := func() error {
		row, err := svc.s.Runs.Get(ctx, svc.s.DB.Runner(), req.GetId())
		if err != nil {
			return mapStateError(err, "run")
		}
		if string(row.State) == last {
			return nil // 状态未变不重发（事件只是信号，帧只走状态迁移）
		}
		last = string(row.State)
		if err := stream.Send(&automationv1.WaitRunResponse{Run: runMsg(row)}); err != nil {
			return err
		}
		if row.State.Terminal() {
			return errWaitDone
		}
		return nil
	}
	return waitOnAggregate(ctx, svc.s, "run", req.GetId(), send)
}

// mapTaskVerbError 映射 Task 生命周期动词哨兵（形态/终态类错误是请求与
// 行现状的冲突面）。
func mapTaskVerbError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	return mapStateError(err, "task")
}

// ---- Schedules（F1.7，ADR-0018） ----

type SchedulesService struct {
	automationv1.UnimplementedSchedulesServiceServer
	s *Services
}

// normalizeCreateSchedule 归一化创建源：5 字段 cron + IANA 时区 + Task 模板
// 字段 → 冻结 TaskSpec 模板（task ref 留空——fire 时铸新 Task；form 恒
// one-shot：Schedule 的每一拍都是一次 one-shot 执行）与首拍绝对时刻
// （now 由调用方注入时钟提供）。
func normalizeCreateSchedule(req *automationv1.CreateScheduleRequest, now time.Time) (*specv1.TaskSpec, string, string, error) {
	if req.GetProjectId() == "" {
		return nil, "", "", apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if req.GetImage() == "" {
		return nil, "", "", apperr.New("E_INVALID_ARGUMENT", "image: must not be empty (schedules fire one-shot tasks; build sources are an app surface)")
	}
	if req.GetCpuMillis() < 0 || req.GetMemoryMb() < 0 {
		return nil, "", "", apperr.New("E_INVALID_ARGUMENT", "cpu_millis/memory_mb: must not be negative")
	}
	if req.GetTtlSeconds() < 0 || req.GetTtlSeconds() > 86400 {
		return nil, "", "", apperr.New("E_INVALID_ARGUMENT", "ttl_seconds: must be within [0, 86400] (ADR-0018)")
	}
	sched, err := schedule.ParseCron(req.GetCron(), req.GetTimezone())
	if err != nil {
		return nil, "", "", apperr.New("E_INVALID_ARGUMENT", "cron: %s", err.Error())
	}
	timezone := req.GetTimezone()
	if timezone == "" {
		timezone = "UTC" // 显式归一：时区随行持久化（空串不落库）
	}
	p := &specv1.ProcessSpec{
		Name:        "run",
		ImageOrigin: &specv1.ProcessSpec_Image{Image: req.GetImage()},
		Command:     req.GetCommand(),
		Env:         req.GetEnv(),
		SecretRefs:  req.GetSecretRefs(),
	}
	if req.GetCpuMillis() > 0 || req.GetMemoryMb() > 0 {
		p.Resources = &specv1.ResourcesSpec{CpuMillis: req.GetCpuMillis(), MemoryMb: req.GetMemoryMb()}
	}
	s := &specv1.TaskSpec{
		SchemaVersion:      spec.SchemaVersion,
		Process:            p,
		TtlSeconds:         req.GetTtlSeconds(),
		NetworkGroup:       req.GetNetworkGroup(),
		DesiredConcurrency: 1,
		Form:               spec.FormOneShot,
	}
	if err := spec.ValidateTaskTemplate(s); err != nil {
		return nil, "", "", mapValidationError(err)
	}
	return s, timezone, state.FormatTime(sched.Next(now)), nil
}

// taskMsg 把 Task 行投影为 proto 消息（镜像/命令/变量从冻结 Spec 还原）。
func (s *Services) taskMsg(row *task.Task) *automationv1.Task {
	msg := &automationv1.Task{
		Id: row.ID, ProjectId: row.ProjectID, Name: row.Name,
		Form: row.Form, State: string(row.State),
		DesiredConcurrency: row.DesiredConcurrency, NetworkGroup: row.NetworkGroup,
		OwnerTokenId: row.OwnerTokenID, DnsName: row.DNSName, LeaseDeadline: row.LeaseDeadline,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
	}
	ts := &specv1.TaskSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(row.Spec, ts); err == nil {
		if p := ts.GetProcess(); p != nil {
			msg.Image = p.GetImage()
			msg.Command = p.GetCommand()
		}
		msg.TtlSeconds = ts.GetTtlSeconds()
	}
	return msg
}

// runMsg 把 Run 行投影为 proto 消息。
func runMsg(row *run.Run) *automationv1.Run {
	msg := &automationv1.Run{
		Id: row.ID, TaskId: row.TaskID, ProjectId: row.ProjectID,
		State: string(row.State), StopReason: row.StopReason,
		DnsName: row.DNSName, Deadline: row.Deadline,
		CreatedAt: row.CreatedAt, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
	if row.ExitCode != nil {
		msg.ExitCode = int32(*row.ExitCode) //nolint:gosec // 退出码域 int→int32 无溢出面
	}
	return msg
}

// CreateSchedule 受理 + 冻结模板 + 落行（四件一拍：schedule.created 事件
// + 审计）+ 唤醒到期拍环。
func (svc *SchedulesService) CreateSchedule(ctx context.Context, req *automationv1.CreateScheduleRequest) (*automationv1.CreateScheduleResponse, error) {
	taskSpec, timezone, nextFire, err := normalizeCreateSchedule(req, svc.s.DB.Clock().Now())
	if err != nil {
		return nil, err
	}
	body, err := marshalSpec(taskSpec)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "schedule template: %s", err.Error()).WithCause(err)
	}
	row := &schedule.Schedule{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		State: schedule.StateActive, CronExpr: req.GetCron(), Timezone: timezone,
		Spec: body, NextFireAt: nextFire,
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Schedules.Create(ctx, tx, row)
		},
		events: []eventFact{{
			// payload 字段全部在行上（时间戳不在 payload 面）——静态构造
			// 与 write 后求值等价（task.created 同款口径）。
			name: engine.EventScheduleCreated, aggregate: "schedule", id: row.ID,
			payload: engine.ScheduleCreatedEventJSON(row),
		}},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "schedule.create", Resource: "schedule/" + row.ID, AfterFP: row.CronExpr,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "schedule")
	}
	svc.s.Engine.KickSchedules()
	return &automationv1.CreateScheduleResponse{Schedule: scheduleMsg(row)}, nil
}

func (svc *SchedulesService) GetSchedule(ctx context.Context, req *automationv1.GetScheduleRequest) (*automationv1.GetScheduleResponse, error) {
	row, err := svc.s.Schedules.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "schedule")
	}
	return &automationv1.GetScheduleResponse{Schedule: scheduleMsg(row)}, nil
}

func (svc *SchedulesService) ListSchedules(ctx context.Context, req *automationv1.ListSchedulesRequest) (*automationv1.ListSchedulesResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	rows, err := svc.s.Schedules.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterScheduleId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "schedule")
	}
	out := &automationv1.ListSchedulesResponse{}
	for i := range rows {
		out.Schedules = append(out.Schedules, scheduleMsg(&rows[i]))
	}
	return out, nil
}

// DeleteSchedule tombstone（幂等：已删行直接成功）。已铸 Task 不受影响——
// 跑完自然收口（引擎无载体可拆，纯行面走 commit 原语）。
func (svc *SchedulesService) DeleteSchedule(ctx context.Context, req *automationv1.DeleteScheduleRequest) (*automationv1.DeleteScheduleResponse, error) {
	row, err := svc.s.Schedules.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "schedule")
	}
	if row.State.Terminal() {
		return &automationv1.DeleteScheduleResponse{}, nil // 幂等
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Schedules.Transit(ctx, tx, row.ID, schedule.StateActive, schedule.StateDeleted)
		},
		events: []eventFact{{
			name: "schedule.deleted", aggregate: "schedule", id: row.ID,
			payload: engine.ScheduleDeletedEventJSON(row),
		}},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: "schedule.delete", Resource: "schedule/" + row.ID,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "schedule")
	}
	return &automationv1.DeleteScheduleResponse{}, nil
}

// TriggerSchedule 手动触发（RunNow 语义）：engine 铸一拍 Task；重叠与
// 终态哨兵映射为可行动错误。
func (svc *SchedulesService) TriggerSchedule(ctx context.Context, req *automationv1.TriggerScheduleRequest) (*automationv1.TriggerScheduleResponse, error) {
	row, err := svc.s.Engine.TriggerSchedule(ctx, req.GetId())
	if err != nil {
		return nil, mapScheduleVerbError(err)
	}
	return &automationv1.TriggerScheduleResponse{Schedule: scheduleMsg(row)}, nil
}

// mapScheduleVerbError 映射 Schedule 动词哨兵。
func mapScheduleVerbError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	switch {
	case errors.Is(err, engine.ErrScheduleOverlapping):
		return apperr.New("E_CONFLICT",
			"the previous fired run is still in flight; stop it (runs stop) or wait for it to finish before triggering again").WithCause(err)
	case errors.Is(err, engine.ErrScheduleTerminal):
		return apperr.New("E_CONFLICT", "schedule already deleted").WithCause(err)
	}
	return mapStateError(err, "schedule")
}

// scheduleMsg 把 Schedule 行投影为 proto 消息（模板镜像/命令/TTL 从冻结
// Spec 还原——taskMsg 同款口径）。
func scheduleMsg(row *schedule.Schedule) *automationv1.Schedule {
	msg := &automationv1.Schedule{
		Id: row.ID, ProjectId: row.ProjectID, Name: row.Name, State: string(row.State),
		Cron: row.CronExpr, Timezone: row.Timezone,
		NextFireAt: row.NextFireAt, LastTaskId: row.LastTaskID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	ts := &specv1.TaskSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(row.Spec, ts); err == nil {
		if p := ts.GetProcess(); p != nil {
			msg.Image = p.GetImage()
			msg.Command = p.GetCommand()
		}
		msg.TtlSeconds = ts.GetTtlSeconds()
	}
	return msg
}

// listLimit 归一 List limit（ADR-0026 after_* + limit 惯例的缺省）。
func listLimit(n int32) int {
	if n <= 0 || n > 200 {
		return 50
	}
	return int(n)
}
