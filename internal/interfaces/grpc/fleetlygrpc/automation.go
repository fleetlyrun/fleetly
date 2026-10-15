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
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/schedule"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// Task 配额缺省住 internal/engine（域拥有者：受理位、ScaleTask、Schedule
// 到期拍共用单一数值源；ADR-0017 附录 A.1）。desired_concurrency 的
// E_INVALID_ARGUMENT 面只保留非负检查——上限由项目并发总量配额承载
// （F1.5 sanity 上限 100 已收口）。

type TasksService struct {
	automationv1.UnimplementedTasksServiceServer
	s *Services
}

// normalizeCreateTask 归一化创建源：直投镜像 + Variables + secretRefs →
// TaskSpec 组装（schemaVersion 由平台钉当前版；form 空 = 按并发推导，
// one-shot 缺省并发 1）。身份注入（TaskRef/属主）与冻结经 engine.MintTaskRow
// （ADR-0062）；验证住本函数——ref 缺席面（ValidateTaskTemplate），规则与
// ValidateTask 同款（身份锚由铸造注入，project_id 非空已在头部受理面拒绝），
// 可观察文案逐字不变。本函数只管请求形状（E_INVALID_ARGUMENT 受理面）与
// spec 组装。
func normalizeCreateTask(req *automationv1.CreateTaskRequest) (*specv1.TaskSpec, error) {
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
	if req.GetDesiredConcurrency() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "desired_concurrency: must not be negative")
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
		Process:            p,
		TtlSeconds:         req.GetTtlSeconds(),
		NetworkGroup:       req.GetNetworkGroup(),
		DesiredConcurrency: desired,
		Form:               form,
	}
	// ref 缺席面验证（validateTaskCommon 同款规则）：身份锚由 MintTaskRow
	// 注入（task id 铸造时生成），ref 在场性由铸造构造保证——规则覆盖与
	// ValidateTask 逐条相同，错误文案逐字不变。
	if err := spec.ValidateTaskTemplate(s); err != nil {
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

// CreateTask 受理（行铸造经 engine.MintTaskRow 单源，ADR-0062）+ 落行
// （四件一拍：task.created 事件 + 审计）。
func (svc *TasksService) CreateTask(ctx context.Context, req *automationv1.CreateTaskRequest) (*automationv1.CreateTaskResponse, error) {
	if name := req.GetName(); name != "" {
		if err := validName("name", name); err != nil {
			return nil, err
		}
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	owner, err := svc.resolveOwnerToken(ctx, req)
	if err != nil {
		return nil, err
	}
	taskSpec, err := normalizeCreateTask(req)
	if err != nil {
		return nil, err
	}
	// 行铸造经 engine.MintTaskRow 单源（ADR-0062）；seq 在事务内取、铸造
	// 在 checks 段完成（拒绝零副作用），事件/审计依赖铸出的行 id 走就地
	// 填充与 auditsFrom（ADR-0070）。
	var seq int64
	var row *task.Task
	events := []eventFact{{name: engine.EventTaskCreated, aggregate: "task", projectID: req.GetProjectId()}}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			func(ctx context.Context, tx *sql.Tx) error {
				var err error
				if seq, err = persistence.NextSeq(ctx, tx, "tasks"); err != nil {
					return err
				}
				row, err = engine.MintTaskRow(req.GetProjectId(), req.GetName(), owner, taskSpec, seq)
				if err != nil {
					// 铸造错误面只有冻结 marshal（验证住 normalizeCreateTask，
					// nil spec 不可达）——文案与收编前逐字一致。
					return apperr.New("E_INVALID_ARGUMENT", "task spec: %s", err.Error()).WithCause(err)
				}
				return nil
			},
			func(ctx context.Context, tx *sql.Tx) error {
				return svc.s.taskQuota(req.GetProjectId(), row.DesiredConcurrency)(ctx, tx)
			},
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Tasks.Create(ctx, tx, row); err != nil {
				return err
			}
			// 事件 id/payload 依赖事务内铸出的行 id：write 段后就地填充。
			events[0].id = row.ID
			events[0].payload = engine.TaskCreatedEventJSON(row)
			return nil
		},
		auditsFrom: func() []*audit.Entry {
			return []*audit.Entry{{
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				Action: "task.create", Resource: "task/" + row.ID, AfterFP: row.Form,
			}}
		},
		events: events,
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
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &automationv1.GetTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) ListTasks(ctx context.Context, req *automationv1.ListTasksRequest) (*automationv1.ListTasksResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	rows, err := svc.s.Tasks.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterTaskSeq(), listLimit(req.GetLimit()))
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
	if req.GetDesiredConcurrency() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "desired_concurrency: must not be negative")
	}
	// 行级授权（ADR-0035）：engine 动词在 handler 前置。
	if err := svc.s.authorizeTaskID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	row, err := svc.s.Engine.ScaleTask(ctx, req.GetId(), req.GetDesiredConcurrency())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.ScaleTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) StopTask(ctx context.Context, req *automationv1.StopTaskRequest) (*automationv1.StopTaskResponse, error) {
	if err := svc.s.authorizeTaskID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	row, err := svc.s.Engine.StopTask(ctx, req.GetId(), req.GetForce())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.StopTaskResponse{Task: svc.s.taskMsg(row)}, nil
}

func (svc *TasksService) DeleteTask(ctx context.Context, req *automationv1.DeleteTaskRequest) (*automationv1.DeleteTaskResponse, error) {
	if err := svc.s.authorizeTaskID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	if err := svc.s.Engine.DeleteTask(ctx, req.GetId()); err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.DeleteTaskResponse{}, nil
}

func (svc *TasksService) RenewTask(ctx context.Context, req *automationv1.RenewTaskRequest) (*automationv1.RenewTaskResponse, error) {
	if err := svc.s.authorizeTaskID(ctx, req.GetId()); err != nil {
		return nil, err
	}
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
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &automationv1.GetRunResponse{Run: runMsg(row)}, nil
}

// ListRuns 收紧（ADR-0035 决策 4）：task_id 与 project_id 必须给其一——
// 无过滤的全平台 Run 列表是跨 Team 泄漏面本身；两者都给属参数矛盾。
func (svc *RunsService) ListRuns(ctx context.Context, req *automationv1.ListRunsRequest) (*automationv1.ListRunsResponse, error) {
	var rows []run.Run
	var err error
	switch {
	case req.GetTaskId() != "" && req.GetProjectId() != "":
		return nil, apperr.New("E_INVALID_ARGUMENT", "task_id and project_id are mutually exclusive; provide one filter")
	case req.GetTaskId() != "":
		tk, terr := svc.s.Tasks.Get(ctx, svc.s.DB.Runner(), req.GetTaskId())
		if terr != nil {
			return nil, mapStateError(terr, "task")
		}
		if aerr := svc.s.authorizeProjectID(ctx, tk.ProjectID); aerr != nil {
			return nil, aerr
		}
		rows, err = svc.s.Runs.ListByTask(ctx, svc.s.DB.Runner(), req.GetTaskId(), req.GetAfterRunSeq(), listLimit(req.GetLimit()))
	case req.GetProjectId() != "":
		if aerr := svc.s.authorizeProjectID(ctx, req.GetProjectId()); aerr != nil {
			return nil, aerr
		}
		rows, err = svc.s.Runs.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterRunSeq(), listLimit(req.GetLimit()))
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"task_id or project_id: one filter is required (an unfiltered run list spans every team's runs)")
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
	if err := svc.s.authorizeRunID(ctx, req.GetId()); err != nil {
		return nil, err
	}
	row, err := svc.s.Engine.StopRun(ctx, req.GetId())
	if err != nil {
		return nil, mapTaskVerbError(err)
	}
	return &automationv1.StopRunResponse{Run: runMsg(row)}, nil
}

// WaitRun：逐状态快照帧，终态（stopped/failed）帧后收流（F1.3 收口面——
// waitOnRow 单一循环，行重读是真源）。id/task_id 精确一（ADR-0063）：
// task_id = 任务维度，钉等待开始时该 Task 的最新 Run（one-shot 即唯一
// Run），无 Run 时事件驱动补获。
func (svc *RunsService) WaitRun(req *automationv1.WaitRunRequest, stream automationv1.RunsService_WaitRunServer) error {
	if (req.GetId() == "") == (req.GetTaskId() == "") {
		return apperr.New("E_INVALID_ARGUMENT", "exactly one of id, task_id is required")
	}
	ctx := stream.Context()
	if req.GetId() != "" {
		if err := svc.s.authorizeRunID(ctx, req.GetId()); err != nil {
			return err
		}
		return svc.waitRunByID(ctx, req.GetId(), stream)
	}
	if err := svc.s.authorizeTaskID(ctx, req.GetTaskId()); err != nil {
		return err
	}
	return svc.waitRunByTask(ctx, req.GetTaskId(), stream)
}

// waitRunByID 是 Run 行型的等待 adapter（read/状态键/Terminal/帧构造四个
// variation points 注入 waitOnRow）。
func (svc *RunsService) waitRunByID(ctx context.Context, runID string, stream automationv1.RunsService_WaitRunServer) error {
	return waitOnRow(ctx, svc.s, "run", runID,
		func(ctx context.Context) (*run.Run, error) {
			row, err := svc.s.Runs.Get(ctx, svc.s.DB.Runner(), runID)
			if err != nil {
				return nil, mapStateError(err, "run")
			}
			return row, nil
		},
		func(r *run.Run) string { return string(r.State) },
		func(r *run.Run) bool { return r.State.Terminal() },
		func(r *run.Run) error {
			return stream.Send(&automationv1.WaitRunResponse{Run: runMsg(r)})
		})
}

// waitRunByTask 钉等待开始时该 Task 的最新 Run（one-shot 即唯一 Run；
// resident 钉首个观测 Run——钉定后跟随其终态，不随补足换靶）。现成 Run
// 直接钉；无 Run 先核对 Task 终态（终态无 Run = 诚实拒绝不挂流），再订阅
// run.* 事件——payload task_id 命中（engine.RunPayloadTaskID 单源提取键）
// 即重列钉定。订阅起点先于钉定读（waitOnAggregate 同款：两步间铸出的 Run
// 只多一次幂等重列）。钉定后与 by-id 同流。
func (svc *RunsService) waitRunByTask(ctx context.Context, taskID string, stream automationv1.RunsService_WaitRunServer) error {
	cursor, err := svc.s.OutboxEvents.LastSeq(ctx, svc.s.DB.Runner())
	if err != nil {
		return mapStateError(err, "events")
	}
	pin := func() (*run.Run, error) {
		rows, lerr := svc.s.Runs.ListByTask(ctx, svc.s.DB.Runner(), taskID, 0, 1)
		if lerr != nil {
			return nil, mapStateError(lerr, "run")
		}
		if len(rows) == 0 {
			return nil, nil
		}
		return &rows[0], nil
	}
	first, err := pin()
	if err != nil {
		return err
	}
	if first == nil {
		t, terr := svc.s.Tasks.Get(ctx, svc.s.DB.Runner(), taskID)
		if terr != nil {
			return mapStateError(terr, "task")
		}
		if t.State.Terminal() {
			// 终态拒绝走 E_CONFLICT（ErrTaskTerminal 同款形）：等待面无
			// 可等对象是状态冲突，不为边缘面开新 errcode。
			return apperr.New("E_CONFLICT",
				"task %s is terminal (%s) and has no runs to wait on", taskID, t.State)
		}
		// 订阅按调用方 Team 过滤（P0-2）：Run 事件已铸资源轴 Team，本队资源
		// 照常命中；平台级事件（''）不受影响。
		subTeam, terr := eventsTeamFilterOf(ctx)
		if terr != nil {
			return terr
		}
		subErr := svc.s.subscribeEvents(ctx, subTeam, cursor, true, func(ev *telemetryv1.Event) error {
			if ev.Aggregate != "run" || engine.RunPayloadTaskID([]byte(ev.Payload)) != taskID {
				return nil
			}
			row, rerr := pin()
			if rerr != nil {
				return rerr
			}
			if row == nil {
				return nil // 未铸出（同拍多事件竞态）：下一事件再试
			}
			first = row
			return errWaitDone // 补获阶段收流（哨兵语义=阶段完成，非错误）
		})
		if subErr != nil && !errors.Is(subErr, errWaitDone) {
			return subErr
		}
		if first == nil {
			return ctx.Err() // 订阅被取消且未补获：按取消收场
		}
	}
	return svc.waitRunByID(ctx, first.ID, stream)
}

// mapTaskVerbError 映射 Task 生命周期动词哨兵（形态/终态类错误是请求与
// 行现状的冲突面）。Task 域哨兵的唯一 owner——新增 Task 哨兵落此；通用
// 出口 mapStateError 内的 Task 分支只是防御 fallback（现役调用点全为
// state 读，不可达；ADR-0068 拓扑注记）。
func mapTaskVerbError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	if errors.Is(err, engine.ErrTaskQuota) {
		return mapTaskQuotaError(err)
	}
	if errors.Is(err, engine.ErrTaskDraining) {
		// 排空中缩容（B12 P3-2）：显式冲突 + 可行动建议，不落裸 CAS 文案。
		return apperr.New("E_CONFLICT", "%s", err.Error()).
			WithCause(err).
			WithSuggestion("Renew the task to revive the pool before scaling, or wait for the drain to finish.")
	}
	return mapStateError(err, "task")
}

// mapTaskQuotaError 是 ErrTaskQuota 的唯一 errcode 面（create/scale/fire
// 三 verb 共用一文案，ADR-0068——此前 scale 专属措辞与 schedule 复制体
// 两种说法各自演化；哨兵详情（项目现值）在 cause 链）。
func mapTaskQuotaError(err error) error {
	return apperr.New("E_QUOTA_EXCEEDED",
		"this request would exceed the project task quota (%d tasks / %d desired concurrency)",
		engine.MaxTasksPerProject, engine.MaxTaskConcurrencyPerProject).
		WithCause(err).
		WithSuggestion("Scale down other tasks in the project, or delete finished tasks to free quota.")
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
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, Name: row.Name,
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
		Id: row.ID, Seq: row.Seq, TaskId: row.TaskID, ProjectId: row.ProjectID,
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
	if name := req.GetName(); name != "" {
		if err := validName("name", name); err != nil {
			return nil, err
		}
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	taskSpec, timezone, nextFire, err := normalizeCreateSchedule(req, svc.s.DB.Clock().Now())
	if err != nil {
		return nil, err
	}
	body, err := marshalSpec(taskSpec)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "schedule template: %s", err.Error()).WithCause(err)
	}
	row := &schedule.Schedule{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
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
			payload: engine.ScheduleCreatedEventJSON(row), projectID: row.ProjectID,
		}},
		auditsFrom: func() []*audit.Entry {
			return []*audit.Entry{{
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				Action: "schedule.create", Resource: "schedule/" + row.ID, AfterFP: row.CronExpr,
			}}
		},
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
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &automationv1.GetScheduleResponse{Schedule: scheduleMsg(row)}, nil
}

func (svc *SchedulesService) ListSchedules(ctx context.Context, req *automationv1.ListSchedulesRequest) (*automationv1.ListSchedulesResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	rows, err := svc.s.Schedules.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterScheduleSeq(), listLimit(req.GetLimit()))
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
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
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
			payload: engine.ScheduleDeletedEventJSON(row), projectID: row.ProjectID,
		}},
		audits: []*audit.Entry{{
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
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
	if err := svc.s.authorizeScheduleID(ctx, req.GetId()); err != nil {
		return nil, err
	}
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
	case errors.Is(err, engine.ErrTaskQuota):
		// 手动拍超配额诚实拒绝（到期拍走 skip；ADR-0017 附录 A.1）——
		// 文案经 mapTaskQuotaError 单源（ADR-0068）。
		return mapTaskQuotaError(err)
	}
	return mapStateError(err, "schedule")
}

// scheduleMsg 把 Schedule 行投影为 proto 消息（模板镜像/命令/TTL 从冻结
// Spec 还原——taskMsg 同款口径）。
func scheduleMsg(row *schedule.Schedule) *automationv1.Schedule {
	msg := &automationv1.Schedule{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, Name: row.Name, State: string(row.State),
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

// listLimit 归一 List limit（ADR-0026 after_* + limit 惯例的缺省）：
// n<=0 回落缺省 50；n>200 钳到上界 200 而非回落缺省——调用方明确要大页
// （500）时静默砍到 50 是对显式意图的覆盖（N1 C21），钳上界既尊重请求
// 又守住查询面。
func listLimit(n int32) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return int(n)
}
