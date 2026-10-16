package engine

// Task/Run 驱动（F1.5/F1.6，ADR-0012/0025）：Task 域单写者收敛环 + 观测
// 裁决。与部署链的分界（C3 观测 verdict owner + P1-7 缓存分家，批 0.5 裁决
// 台账 C3 行）：
//
//   - 部署链的观测缓存五 map（workloadApp/observations/ensuredGen/
//     ensuredSpec/expected）是"部署形状"——键按 App 投影、恢复真源是
//     succeeded 部署重放；Run 粒度混入即 gen 语义错位、per-Run 条目只增
//     不清。
//   - Task 域用独立缓存组（workloadRun/runObs + per-Task Ensure 签名），
//     恢复真源是 Task/Run 行与绝对 deadline（ADR-0018 墙钟续算），重启后
//     下一拍收敛自愈，不抄 rebuildBaselines。
//   - 观测裁决权分轨：handleObservation 按 workloadRun 归属路由——Run 观
//     测进 Run 状态机（handleRunObservation），App 观测走原部署面。
//
// 混合池拓扑（ADR-0025 决策 7/R-6）：resident 池 = per-Run service（控制
// 面 TTL/排空/per-Run DNS 的载体单元），池级 DNS 由全部 Run 的别名共同
// 铸出（DNS RR 行为 e2e 实证项）；swarm API 压力（Ensure ×N）以 torchwood
// 池规模压测锚点收口。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// taskDrivingStates 是 Run 驱动集合（Ensure 期望集来源）。
var taskDrivingStates = []run.State{run.StatePending, run.StateRunning, run.StateStopping}

// taskStep 是 Task 收敛环的单次推进：属主吊销拉式扫描 → 逐 Task 驱动
// （janitor/补足/Ensure/终态收口）。单写者（Loop 串行）；观测裁决在
// consumeWatch goroutine 经 CAS 并发写行，互不阻塞。
//
// 驱动 Run 集一次批量取回（N1 C18：此前逐 Task ListByTaskStates 是 N+1
// 查询——每秒 N 个 Task 即 N 次点查；runs.ListDriving 单查询全量后按
// Task 分组，行集与逐 Task 查询完全一致——同状态集、同组内 id DESC 方向
// 锚，僵尸 Run（终态 Task 名下）随分组丢弃，其收口是 sweepZombieRuns 的
// 专属路径）。
//
// 性能阈值锚(P5-5)：taskStep 轮询面分析容量 ≈500 driving task（分析容量
// 锚，评审容量结论非实测上限）。形态：每拍 ListDriving 全量单查询加逐行
// 收口，代价随 driving 行数线性走。重估触发：driving 行数常态逼近 500
// 量级。
func (e *Engine) taskStep(ctx context.Context) {
	tasks, err := e.tasks.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("task step: list driving", "err", err)
		return
	}
	e.sweepRevokedOwners(ctx, tasks)
	if err := e.sweepZombieRuns(ctx); err != nil {
		e.log.Error("task step: sweep zombie runs", "err", err)
	}
	runs, err := e.runs.ListDriving(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("task step: list driving runs", "err", err)
		return
	}
	byTask := groupDrivingRunsByTask(runs)
	for i := range tasks {
		e.driveTask(ctx, &tasks[i], byTask[tasks[i].ID])
	}
}

// groupDrivingRunsByTask（分组 + 有序视图构造）与 runsByTask 不变量
//（方向锚/双占用规则）收口在 taskview.go。

// sweepRevokedOwners 是属主吊销排空的拉式扫描（P1-8：吊销排空走 task 环
// 周期扫 revoked 属主——拉式、重启安全；Task 行记 owner_token_id 引用）。
// 默认宽限排空（宽限停止存量 Run）；TaskOwnerRevokedRunToTTL 配置跑完 TTL。
// 单 token 存储故障只记日志继续（B12 P3-3）：一坏 token 中断整轮会让其余
// token 名下 Task 的吊销排空集体滞留——拉式扫描的可用性真源是"扫完"。
func (e *Engine) sweepRevokedOwners(ctx context.Context, tasks []task.Task) {
	byToken := map[string][]*task.Task{}
	for i := range tasks {
		t := &tasks[i]
		if t.OwnerTokenID == "" || t.State != task.StateActive {
			continue
		}
		byToken[t.OwnerTokenID] = append(byToken[t.OwnerTokenID], t)
	}
	for tokenID, owned := range byToken {
		tok, err := e.tokens.Get(ctx, e.db.Runner(), tokenID)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 行不存在防御：Token 行只吊销不删
			}
			e.log.Error("task step: lookup owner token", "token", tokenID, "err", err)
			continue
		}
		if !tok.Revoked {
			continue
		}
		for _, t := range owned {
			e.drainTask(ctx, t, run.ReasonOwnerRevoked)
		}
	}
}

// sweepZombieRuns 收口僵尸 Run：终态 Task（completed/failed/drained/
// deleted）名下仍处 driving 态的 Run 行逐条终态化（stopped/
// platform_drained；幂等 CAS，冲突即跳过——并发已收口以行现状为准）。
// 正常链路不再产生此类行（one-shot 补足把 stopping 计入占位）；此处清扫
// pre-fix 存量（staging 库已有），否则 Schedule 重叠判定（Run 行为真源）
// 被僵尸行永久 skip。终态 Task 不进 ListDriving，本扫是其唯一收口路径。
func (e *Engine) sweepZombieRuns(ctx context.Context) error {
	zombies, err := e.runs.ListDrivingOfTerminalTasks(ctx, e.db.Runner())
	if err != nil {
		return err
	}
	for i := range zombies {
		if err := e.transitRunFour(ctx, &zombies[i],
			taskDrivingStates, run.StateStopped,
			func(r *run.Run) { r.StopReason, r.Deadline = run.ReasonPlatformDrained, "" }); err != nil {
			if !errors.Is(err, state.ErrConflict) {
				e.log.Error("task step: zombie run sweep", "run", zombies[i].ID, "err", err)
			}
		}
	}
	return nil
}

// driveTask 驱动单个 Task 一拍，显式序列（顺序即语义；行集不变量
// ——方向锚与双占用规则——收口在 taskview.go 的 runsByTask）：
//
//	lease 到期排空 → Run janitor（TTL/停止兜底）→ 排空补停 →
//	one-shot 终态镜像（早退段）→ 排空完成收口（早退段）→
//	补足/过量排空 → Ensure（期望集收敛）
//
// 持有 Task 级互斥（DeleteTask 的载体收口与本环互斥，与 appLocks
// 同款）。runs 是本拍批量预取的本 Task 驱动 Run 视图（新→旧，
// taskStep 分组产物——N1 C18）。
func (e *Engine) driveTask(ctx context.Context, t *task.Task, runs runsByTask) {
	mu := e.lockTask(t.ID)
	mu.Lock()
	defer mu.Unlock()

	spec, err := loadTaskSpec(t.Spec)
	if err != nil {
		e.log.Error("task drive: load spec", "task", t.ID, "err", err)
		return
	}
	now := e.clock.Now()

	e.driveLeaseExpiry(ctx, t, now)
	e.driveRunJanitor(ctx, runs, now)
	e.driveDrainReStop(ctx, t, runs, now)
	if e.driveOneshotMirror(ctx, t) {
		return // 终态镜像段已收口（或空集 Ensure 失败留拍重试）：本拍止步
	}
	if e.driveDrainCompletion(ctx, t, runs) {
		return // 排空完成段已收口（同上，Ensure 成功才迁移）
	}
	runs = e.driveReplenish(ctx, t, spec, runs, now)
	e.driveEnsure(ctx, t, spec, runs)
}

// driveLeaseExpiry：resident + active + 绝对 deadline 超宽限（ADR-0018
// 墙钟口径）→ 排空（停补足 + 宽限停止存量 Run）+ lease.expired 事件；
// 属主续期可复活（RenewTask 的补足半边）。不早退——janitor 与收口段
// 本拍照跑。
func (e *Engine) driveLeaseExpiry(ctx context.Context, t *task.Task, now time.Time) {
	if t.State != task.StateActive || t.Form != task.FormResident || t.LeaseDeadline == "" {
		return
	}
	if deadline := parseDeadline(t.LeaseDeadline); deadline != nil && now.After(deadline.Add(e.opts.Task.TaskLeaseGrace)) {
		e.drainTask(ctx, t, run.ReasonLeaseExpired)
		if err := e.emitObservation(ctx, e.eventTeamOfProject(ctx, e.db.Runner(), t.ProjectID),
			eventLeaseExpired, "task", t.ID,
			leaseEventPayloadJSON(t, "lease deadline exceeded grace")); err != nil {
			e.log.Error("task drive: lease.expired event", "task", t.ID, "err", err)
		}
	}
}

// driveRunJanitor：Run janitor——TTL 到期 → stopping/ttl_expired（deadline
// 覆写为停止收口兜底）；stopping 超兜底 → stopped（观测缺位时的收口
// 保底，不悬挂）。行经 stopRunRow/transitRunFour 原地刷新，视图随迁。
func (e *Engine) driveRunJanitor(ctx context.Context, runs runsByTask, now time.Time) {
	for i := range runs {
		m := &runs[i]
		if m.State.Active() && m.Deadline != "" {
			if deadline := parseDeadline(m.Deadline); deadline != nil && now.After(*deadline) {
				if err := e.stopRunRow(ctx, m, run.ReasonTTLExpired, now); err != nil {
					e.log.Error("task drive: ttl expire", "run", m.ID, "err", err)
				}
				continue
			}
		}
		if m.State == run.StateStopping && m.Deadline != "" {
			if deadline := parseDeadline(m.Deadline); deadline != nil && now.After(*deadline) {
				if err := e.transitRunFour(ctx, m,
					[]run.State{run.StateStopping}, run.StateStopped, nil); err != nil && !errors.Is(err, state.ErrConflict) {
					e.log.Error("task drive: stop fallback", "run", m.ID, "err", err)
				}
			}
		}
	}
}

// driveDrainReStop：排空补停（P1 修复 2026-10-02：draining Task 的存量
// Run 收口保证）。排空路径（drainTask/StopTask force）对存量 Run 的停止
// 是一拍内尽力而为：单条失败只记日志、或旧代码 StopTask 不持 Task 互斥
// 时与补足竞态漏停新铸 Run——残留的 pending/running 被 Ensure 以 1 副本
// 持续保活，janitor 只管 TTL/停止兜底（resident 无 TTL 即永不触发），
// Task 永久卡 draining。
//
// 触发锚与起因都取行事实（drainAnchorReason）：同 Task 已有 stopping
// 兄弟 = 宽限排空确已发起，起因复用其 stop_reason（排空发起时的语义已
// 落行，重启后仍可判）。不做无条件补停的取舍：属主吊销的跑完 TTL 模式
// （TaskOwnerRevokedRunToTTL）与 StopTask 非强停的自然收口语义都刻意让
// 存量 Run 续跑到 TTL/完成——两者均不产生 stopping 行，天然豁免；Task
// 行无起因列，内存态重启即失，行事实是最小且诚实的真源。
func (e *Engine) driveDrainReStop(ctx context.Context, t *task.Task, runs runsByTask, now time.Time) {
	if t.State != task.StateDraining {
		return
	}
	drainReason := runs.drainAnchorReason()
	if drainReason == "" {
		return
	}
	for i := range runs {
		if !runs[i].State.Active() {
			continue
		}
		if err := e.stopRunRow(ctx, &runs[i], drainReason, now); err != nil {
			// 并发已迁移（观测路径收口）= 目标已达，静默让位。
			if !errors.Is(err, state.ErrConflict) {
				e.log.Error("task drive: drain re-stop", "run", runs[i].ID, "err", err)
			}
		}
	}
}

// driveOneshotMirror：one-shot 终态镜像（行事实 → Task 态）。收口次序
// （E29-2 反转）：空集 Ensure 先行且成功后才落终态迁移——修复前先迁移
// 后 Ensure，Ensure 失败/进程崩溃窗口下 Task 已终态退出驱动集
// （ListDriving 不拾取终态行），run service 残留（swarm 0/1 形态）无人
// 重试。失败路径 Task 保持 active 留在驱动集，下拍重试（重入安全：
// Ensure 幂等签名跳过、迁移 CAS 幂等）。
//
// 返回 true = 本段已接管本拍（镜像已落或空集 Ensure 失败留拍重试），
// 调用方跳过其后全部段。
func (e *Engine) driveOneshotMirror(ctx context.Context, t *task.Task) bool {
	if t.State != task.StateActive || t.Form != task.FormOneShot {
		return false
	}
	to := e.oneshotTerminalState(ctx, t)
	if to == "" {
		return false // 唯一 Run 未终态：进入补足/Ensure 段
	}
	// 唯一 Run 已终态：空集 Ensure 收口残留载体，成功后 Task 镜像其成败。
	if err := e.ensureTaskWorkloads(ctx, t, nil); err != nil {
		return true
	}
	if err := e.transitTaskFour(ctx, t, []task.State{task.StateActive}, to, nil); err != nil {
		e.log.Error("task drive: mirror terminal", "task", t.ID, "err", err)
	}
	return true
}

// driveDrainCompletion：排空完成收口——残留载体收敛移除（空集 Ensure）
// → drained。收口次序同 driveOneshotMirror（E29-2：Ensure 成功才迁移）。
// 返回 true = 本段已接管本拍。
func (e *Engine) driveDrainCompletion(ctx context.Context, t *task.Task, runs runsByTask) bool {
	if t.State != task.StateDraining || len(runs) != 0 {
		return false
	}
	if err := e.ensureTaskWorkloads(ctx, t, nil); err != nil {
		return true
	}
	if err := e.transitTaskFour(ctx, t,
		[]task.State{task.StateDraining}, task.StateDrained, nil); err != nil && !errors.Is(err, state.ErrConflict) {
		e.log.Error("task drive: drained", "task", t.ID, "err", err)
	}
	return true
}

// driveReplenish：补足与过量排空（active only）。one-shot 期望 1；
// resident 期望 desired_concurrency。过量排空（ScaleTask 缩容的收敛
// 半边，staging 真机实证缺口 2026-10-02：此前只有补足——缩容后池维持
// 过量直到停止/租约过期）：停新保老（方向锚：视图头部即最新，长者已
// 预热）。原因 = platform_drained（平台排空，ADR-0012 七枚举）。
// 突增上限防一拍海量创建（swarm API 压力面，决策 7 压测锚）。补足成功
// 后重列刷新视图（新 Run 同拍进期望集——否则首拍 Ensure 空集，对补位
// 拍会白白推迟一拍收敛）。
func (e *Engine) driveReplenish(ctx context.Context, t *task.Task, spec *specv1.TaskSpec, runs runsByTask, now time.Time) runsByTask {
	if t.State != task.StateActive {
		return runs
	}
	want := t.DesiredConcurrency
	if t.Form == task.FormOneShot {
		want = 1
	}
	live := runs.liveCount()
	for i := 0; i < len(runs) && live > int(want); i++ {
		if !runs[i].State.Active() {
			continue
		}
		if err := e.stopRunRow(ctx, &runs[i], run.ReasonPlatformDrained, now); err != nil {
			if !errors.Is(err, state.ErrConflict) {
				// 基础设施错误：本拍止步（一错即断会放大抖动，下拍重试）。
				e.log.Error("task drive: excess drain", "run", runs[i].ID, "err", err)
				break
			}
			// CAS 冲突 = 该 Run 已被并发迁移出活跃态（观测路径收口等）：
			// 槽位照常让出，继续排空其余（一错即 break 会把单条竞态放大
			// 成整拍排空停滞，下拍幂等收敛）。
			live--
			continue
		}
		live--
	}
	// 补足锚（双占用规则，见 taskview.go）：one-shot 按全量驱动行
	//（occupiedFor 判定点）；resident 按 live 账本——过量排空让出的槽位
	//（含 CAS 冲突行——并发已迁移，槽位事实已让出）即时可见，不能按
	// 行集现态重数。
	occupied := live
	if t.Form == task.FormOneShot {
		occupied = runs.occupiedFor(t.Form)
	}
	created := false
	for n := occupied; n < int(want) && n-occupied < e.opts.Task.TaskReplenishBurst; n++ {
		if err := e.createRun(ctx, t, spec, now); err != nil {
			e.log.Error("task drive: replenish", "task", t.ID, "err", err)
			break
		}
		created = true
	}
	if !created {
		return runs
	}
	refreshed, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, taskDrivingStates)
	if err != nil {
		e.log.Error("task drive: relist runs", "task", t.ID, "err", err)
		return runs
	}
	return newRunsByTask(refreshed)
}

// driveEnsure：期望集收敛——期望集 = 驱动 Run 的 Workload（pending/
// running → 1 副本，stopping → 0 副本承载 SIGTERM+StopGrace）；域内
// 收敛移除终态 Run 的残留载体。签名比对 + 周期强制重放控制 API 压力。
func (e *Engine) driveEnsure(ctx context.Context, t *task.Task, spec *specv1.TaskSpec, runs runsByTask) {
	team, err := e.taskTeam(ctx, t)
	if err != nil {
		e.log.Error("task drive: resolve team", "task", t.ID, "err", err)
		return
	}
	ws := make([]capability.Workload, 0, len(runs))
	for i := range runs {
		w, _, err := ProjectTask(spec, team, runs[i].ID, runs[i].State != run.StateStopping, e.egressNetworkMap(ctx, t.ProjectID))
		if err != nil {
			e.log.Error("task drive: project run", "run", runs[i].ID, "err", err)
			continue
		}
		w.StopGrace = e.opts.Task.TaskStopGrace
		ws = append(ws, w)
	}
	// 托管库引用注入（pre-deploy job 铸面透传的 database_refs——值不进
	// TaskSpec 存储，Ensure 前解析）。
	for i := range ws {
		if err := e.applyDatabaseEnv(ctx, &ws[i], spec.GetProcess(), t.ProjectID); err != nil {
			e.log.Error("task drive: inject database env", "task", t.ID, "err", err)
			return
		}
	}
	// 周期收敛路径：失败日志已记、签名已清，下拍重试（终态收口路径见
	// driveOneshotMirror/driveDrainCompletion，以返回值门控迁移）。
	_ = e.ensureTaskWorkloads(ctx, t, ws)
}

// oneshotVerdict 是 one-shot Task 唯一 Run 终态对的判读档位（Run 终态对
// 是真源：Task 行的终态镜像随 taskStep 有拍延迟，成败判定回到 Run）。
// 五档是原始结局的判别——域语义映射归调用方：task 环只把 RunFailed 镜像
// 为 failed（ttl 到期是 Run 生命周期完结 → completed），predeploy 把
// TTLExpired/StoppedEarly 判为 job 失败（等待面语义）。判读单源收编的是
// 「读终态对 + 判别 + 人读描述」，不是各域的成败政策。
type oneshotVerdict int

const (
	oneshotPending      oneshotVerdict = iota // 唯一 Run 未终态（无终态 Run 可读）
	oneshotSucceeded                          // Run (stopped, completed)
	oneshotRunFailed                          // Run (failed)——why: exit code
	oneshotTTLExpired                         // Run (stopped, ttl_expired)——why: ttl 句
	oneshotStoppedEarly                       // Run (stopped, 其他 reason)——why: stop_reason
)

// oneshotRunVerdict 读 one-shot Task 唯一 Run 的终态对并判读结局（此前
// preDeployOutcome 与 oneshotTerminalState 各持一份读取口径——读取与判别
// 收编一处，F2.2 备份执行链直接消费 why）。why 文案与 deployment 失败
// 原因同面；读失败以 err 单独返回，处置策略归调用方（task 环下拍重试、
// predeploy 记失败）。
func (e *Engine) oneshotRunVerdict(ctx context.Context, taskID string) (oneshotVerdict, string, error) {
	runs, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), taskID, []run.State{run.StateStopped, run.StateFailed})
	if err != nil {
		return oneshotRunFailed, "", err
	}
	if len(runs) == 0 {
		return oneshotPending, "", nil
	}
	m := &runs[0] // ListByTaskStates 新→旧：首行 = 唯一 Run
	switch {
	case m.State == run.StateFailed:
		code := "unknown"
		if m.ExitCode != nil {
			code = fmt.Sprintf("%d", *m.ExitCode)
		}
		return oneshotRunFailed, fmt.Sprintf("failed (exit code %s)", code), nil
	case m.StopReason == run.ReasonCompleted:
		return oneshotSucceeded, "", nil
	case m.StopReason == run.ReasonTTLExpired:
		return oneshotTTLExpired, "exceeded its ttl and was stopped (raise ttl or fix the job)", nil
	default:
		return oneshotStoppedEarly, fmt.Sprintf("was stopped early (stop_reason %s)", m.StopReason), nil
	}
}

// oneshotTerminalState 返回 one-shot Task 唯一 Run 的终态迁移目标
// （completed/failed；唯一 Run 未终态或行集不可读返回零值——迁移与检测
// 分离，检测先于空集 Ensure，迁移在 Ensure 成功后，见 driveTask 收口次序）。
// 域映射：仅 RunFailed → failed（ttl 到期/早停在 task 域是生命周期完结
// → completed）。判读经 oneshotRunVerdict 单源；读失败记日志下拍重试。
func (e *Engine) oneshotTerminalState(ctx context.Context, t *task.Task) task.State {
	verdict, _, err := e.oneshotRunVerdict(ctx, t.ID)
	if err != nil {
		e.log.Error("task drive: list terminal runs", "task", t.ID, "err", err)
		return ""
	}
	switch verdict {
	case oneshotRunFailed:
		return task.StateFailed
	case oneshotSucceeded, oneshotTTLExpired, oneshotStoppedEarly:
		return task.StateCompleted
	default:
		return ""
	}
}

// ensureTaskWorkloads 以签名比对 + 周期强制收口下发 Task 域期望集（签名
// 未变且未到期 = 跳过，控制 per-tick swarm API 压力；失败清签名下拍重试；
// 重启后缓存丢失 → 首拍全量重放自愈）。返回值是收口成败信号：终态收口
// 路径（driveTask 的镜像/排空迁移）据此决定是否落终态迁移（E29-2 收口
// 次序：Ensure 成功才迁移），周期收敛路径忽略之（日志已记，下拍重试）。
func (e *Engine) ensureTaskWorkloads(ctx context.Context, t *task.Task, ws []capability.Workload) error {
	// 签名是全集 canonical（capability.WorkloadSetSignature）——「有没有变」
	// 由类型自己回答，手挑字段子集对投影期解析字段（如 EgressNetworks）盲
	// 的缺陷类已绝根（runbook 记录·三十七）。
	sig := capability.WorkloadSetSignature(ws)
	if _, fresh := e.ensureFresh(e.task.ensure, t.ID, sig, e.clock.Now()); fresh {
		return nil
	}
	// 维护互斥读半边（ADR-0046）：Ensure 族与网络重建的串行化锚；锁等待
	// 不占步预算（排队语义）。下发段带界（boundedStep，Options.
	// ManagedStepTimeout 的实证背景）：材料解析与 runtime.Ensure 都跑在
	// Task 单写者环上，无界 hang 卡死整个环（janitor/租约排空/停止兜底/
	// 补足全住环上）。Ensure 失败本就是清签名下拍重试语义，带界无损。
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	ctx, cancel := e.boundedStep(ctx)
	defer cancel()
	team, err := e.taskTeam(ctx, t)
	if err != nil {
		e.log.Error("task ensure: resolve team", "task", t.ID, "err", err)
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: t.ProjectID, Task: t.ID}
	materials, err := e.resolveTaskMaterials(ctx, t)
	if err != nil {
		e.log.Error("task ensure: materials", "task", t.ID, "err", err)
		return err
	}
	if _, err := e.runtime.Ensure(ctx, ns, ws, capability.Generation(1), materials); err != nil {
		e.log.Error("task ensure", "task", t.ID, "err", err)
		e.ensureForget(e.task.ensure, t.ID) // 失败清签名：下拍重试
		return err
	}
	// 归属登记（观测路由：Run 观测 → Run 状态机）。
	e.task.obsMu.Lock()
	for _, w := range ws {
		e.task.workloadRun[w.ID] = t.ID
	}
	e.task.obsMu.Unlock()
	now := e.clock.Now()
	e.ensureRemember(e.task.ensure, t.ID, ensureMemo{sig: sig, at: now})
	return nil
}

// drainTask 发起排空：task → draining（task.draining 事件携带起因）；
// 默认宽限停止存量 Run（stopping/<reason>），跑完 TTL 模式只停补足。
func (e *Engine) drainTask(ctx context.Context, t *task.Task, reason string) {
	if err := e.transitTaskFour(ctx, t, []task.State{task.StateActive}, task.StateDraining, nil); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return // 并发已迁移（观测路径/重复拍）：以行现状为准
		}
		e.log.Error("task drain", "task", t.ID, "err", err)
		return
	}
	if reason == run.ReasonOwnerRevoked && e.opts.Task.TaskOwnerRevokedRunToTTL {
		return // 跑完 TTL 模式：存量 Run 自然收口
	}
	now := e.clock.Now()
	runs, err := e.runs.ListByTaskStates(ctx, e.db.Runner(), t.ID, run.ActiveStates)
	if err != nil {
		e.log.Error("task drain: list runs", "task", t.ID, "err", err)
		return
	}
	for i := range runs {
		if err := e.stopRunRow(ctx, &runs[i], reason, now); err != nil {
			e.log.Error("task drain: stop run", "run", runs[i].ID, "err", err)
		}
	}
}

// stopRunRow 把一条活跃 Run 迁入 stopping（起因即写入，终态携带；deadline
// 覆写为停止收口兜底 = StopGrace × 2 + 观测余量）。
func (e *Engine) stopRunRow(ctx context.Context, m *run.Run, reason string, now time.Time) error {
	fallback := state.FormatTime(now.Add(2 * e.opts.Task.TaskStopGrace).Add(10 * time.Second))
	return e.transitRunFour(ctx, m,
		run.ActiveStates, run.StateStopping,
		func(r *run.Run) { r.StopReason, r.Deadline = reason, fallback })
}

// createRun 落一条 pending Run（四件一拍：行 + run.created 事件 + 审计）。
// TTL 绝对 deadline 按 ADR-0018 墙钟落库。
func (e *Engine) createRun(ctx context.Context, t *task.Task, spec *specv1.TaskSpec, now time.Time) error {
	deadline := ""
	if ttl := spec.GetTtlSeconds(); ttl > 0 {
		deadline = state.FormatTime(now.Add(time.Duration(ttl) * time.Second))
	}
	var m *run.Run
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		// run id = <task id>-<seq>（ADR-0070 派生类）：WorkloadID 与 Run DNS
		// 名随之全链可读（fleetly-run-nightly-backup-33-57）。
		rseq, err := e.db.NextSeq(ctx, tx, "runs")
		if err != nil {
			return err
		}
		id := entityid.Derive(t.ID, "r", rseq)
		m = &run.Run{
			ID: id, Seq: rseq, TaskID: t.ID, ProjectID: t.ProjectID,
			State: run.StatePending, WorkloadID: id, DNSName: RunDNSName(id),
			Deadline: deadline,
		}
		return e.commitWrite(ctx, tx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error { return e.runs.Create(ctx, tx, m) },
			facts: []func() writes.FactRow{func() writes.FactRow {
				return writes.FactRow{
					Name: eventRunState(run.StatePending), Aggregate: "run", ID: m.ID,
					Payload: runEventPayloadJSON(m), TeamID: e.eventTeamOfProject(ctx, tx, m.ProjectID),
					Action: "run.create", Resource: "run/" + m.ID,
				}
			}},
		})
	})
}

// projectTeam 从 Project 行实取团队（域解析的单一真源：appTeam/taskTeam/
// resolveBackend/activeProjectNetworks 全部经此——ADR-0028"零 default 字面量"
// 的落点）。跨域共享助手：browse/backup/managed/observ/builder/database/
// hygiene 八处消费，故留驱动文件（P3 批 3 判定）。
func (e *Engine) projectTeam(ctx context.Context, projectID string) (string, error) {
	p, err := e.projects.Get(ctx, e.db.Runner(), projectID)
	if err != nil {
		return "", fmt.Errorf("resolve project %s: %w", projectID, err)
	}
	return p.TeamID, nil
}
