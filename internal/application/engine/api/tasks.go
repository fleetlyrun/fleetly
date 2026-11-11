package api

// Task/Run 生命周期输入面契约（真源自 engine 根包迁出，P3-7 输入面契约
// 收口）：配额定律（ADR-0017 附录 A.1/ADR-0068）、行铸造单源（ADR-0062）
// 与 Task 域哨兵在此单源——engine 与受理位同向消费。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// Task 域哨兵错误（API 层映射 errcode）。
var (
	// ErrTaskNotRenewable 是 RenewTask 目标形态不可续期（one-shot 无租约）。
	ErrTaskNotRenewable = errors.New("engine: task carries no owner lease")
	// ErrTaskTerminal 是生命周期动词命中终态行（终态事实不可改写）。
	ErrTaskTerminal = errors.New("engine: task is in a terminal state")
	// ErrTaskDraining 是缩容动词命中排空中行（B12 P3-2：排空中期望并发
	// 无意义——先复活或等排空收口；裸 ErrConflict 的"并发改动"文案误导）。
	ErrTaskDraining = errors.New("engine: task is draining")
	// ErrNotResident 是仅 resident 形态适用的动词面（ScaleTask）。
	ErrNotResident = errors.New("engine: this verb targets resident tasks only")
	// ErrTaskQuota 是 per-Project Task 配额命中（ADR-0017 附录 A.1；API 层
	// 映射 E_QUOTA_EXCEEDED）。
	ErrTaskQuota = errors.New("engine: project task quota exceeded")
	// ErrScheduleTerminal 是生命周期动词命中终态行（tombstone 事实不可改写）。
	ErrScheduleTerminal = errors.New("engine: schedule is in a terminal state")
	// ErrScheduleOverlapping 是手动触发时上一拍 Run 未终态（先停上一拍或等其收口）。
	ErrScheduleOverlapping = errors.New("engine: schedule's previous run is still in flight")
)

// Task 配额缺省（ADR-0017 附录 A.1，F1.9）：单一数值源——受理位
// （CreateTask）、ScaleTask 增量检查与 Schedule 拍共用；配置面接入
// AppConfig 后可覆盖。
const (
	// MaxTasksPerProject 是 per-Project 非终态 Task 行数上限。
	MaxTasksPerProject = 100
	// MaxTaskConcurrencyPerProject 是 per-Project 非终态 Task 的
	// desired_concurrency 之和上限（期望 Run 总量 = Workload 数量真源口径）。
	MaxTaskConcurrencyPerProject = 200
)

// CheckTaskQuota 是项目 Task 配额的唯一定律（ADR-0017 附录 A.1；ADR-0068
// 收口——此前 spawn/scale/API 受理位三处判定两条文案各自演化）。双上限：
// spawn 模态计活跃行数（铸造占新行位），scale 模态只计并发净增量（原地
// 改 desired 不占新行位）。active/sum 由调用方事务内 StatsByProject 读
// （SQLite 单写串行，无 TOCTOU）；命中返回 ErrTaskQuota 哨兵——API 层
// mapTaskQuotaError 统一转义，engine 域调用方转各自语义（predeploy 有界
// 等待 / 到期拍 skip / 手动拍诚实拒绝）。
func CheckTaskQuota(projectID string, active int, sum, delta int64, spawn bool) error {
	if spawn && active >= MaxTasksPerProject {
		return fmt.Errorf("%w: project %s holds %d active tasks (limit %d); delete or drain tasks first",
			ErrTaskQuota, projectID, active, MaxTasksPerProject)
	}
	if sum+delta > MaxTaskConcurrencyPerProject {
		return fmt.Errorf("%w: project %s desired concurrency %d + %d would exceed the limit %d",
			ErrTaskQuota, projectID, sum, delta, MaxTaskConcurrencyPerProject)
	}
	return nil
}

// MintTaskRow 铸一条 Task 行：TaskRef 与属主注入调用方 spec（原地变更）、
// 行缺省、冻结并装配行。name 允许空（系统级铸造不占项目名位）；ownerTokenID
// 允许空（匿名/机制铸造无属主，resident 无租约保温）。seq 由调用方在其写
// 事务内取（e.db.NextSeq 契约方法，行创建序真源）；now 是铸造时间（域不
// 持墙钟——调用方时钟给出，ADR-0087 决策 2）；id = entityid.Mint(now)
// （ADR-0092），身份注入先于冻结（CONTEXT.md Task Mint 词条：铸造单源不动）。
//
// 行装配不写 LeaseDeadline（R04-M1）：本函数是纯铸造（不持时钟），创建
// 受理面在 interfaces 层；租约由引擎首拍布防（task.go armTaskLease：权威
// 时钟 now+interval）——API 创建后 KickTasks 保证一拍内落地，此后不续期
// 即按 deadline+宽限排空，与"创建即布防"行为等价。
func MintTaskRow(projectID, name, ownerTokenID string, s *specv1.TaskSpec, seq int64, now time.Time) (*task.Task, error) {
	if s == nil {
		return nil, fmt.Errorf("mint task row: nil spec")
	}
	taskID := entityid.Mint(now)
	s.Task = &specv1.TaskRef{Id: taskID, Project: projectID}
	s.OwnerToken = ownerTokenID
	form := spec.TaskForm(s)
	desired := s.GetDesiredConcurrency()
	if desired == 0 && form == spec.FormOneShot {
		desired = 1 // one-shot 缺省 1（受理位同款规则收编入铸造）
	}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(s)
	if err != nil {
		return nil, err
	}
	return &task.Task{
		ID: taskID, Seq: seq, ProjectID: projectID, Name: name,
		Form: form, State: task.StateActive, Spec: body,
		OwnerTokenID: ownerTokenID, DesiredConcurrency: desired,
		NetworkGroup: s.GetNetworkGroup(), DNSName: TaskDNSName(taskID),
	}, nil
}

// TaskDNSName 铸 per-Task 池级稳定 DNS 名（活 Run 轮询解析；公式住平台
// ——名字是平台 API 面，N4 换 Runtime 名字不变，ADR-0025 决策 6/R-3）。
func TaskDNSName(taskID string) string {
	return "task-" + strings.ToLower(taskID)
}
