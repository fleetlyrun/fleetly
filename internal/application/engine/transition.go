package engine

// 四件一拍的唯一序列真源（engine 侧；事实双行落库自 P2 刀 1 起同归
// 事实发射 kernel——internal/domain/services/writes 的 FactRow 扇出，
// ADR-0064 →收敛报告 §刀1）。2026-10-03 架构批收口：此前同一不变量在
// transit/transitAndReload/transitRunFour/transitTaskFour/transitBuild
// 五份手搓拷贝中各自演化（task 域拷贝已出现"注释承诺审计、代码无审计"
// 的漂移），规则与顺序自此单点拥有：
//
//   1. 状态迁移四件一 = CAS 迁移 + 刷新行 + Outbox 事件 + 审计，同一事务；
//   2. 原地迁移（落定状态仍在 from 集）不发事件——事件是状态迁移的既成
//      事实，状态未变不是迁移；审计照落（kernel 形态 = LedgerOnly 单行
//      事实，偏离理由在此注释）；
//   3. 事件名/负载/审计指纹取自刷新后行（既成事实），伴生字段改写随指纹
//      带出（"; key=value" 后缀形态）；
//   4. 非迁移写（建行/字段更新）同构：检查先行（拒绝零副作用）→ 写居中
//      → 事实扇出殿后——双行配对与段间失败语义由 kernel 执法（commitWrite
//      编排检查/写段与 steps，只做归一化）。
//
// 事务边界双形态：commit* 系列收调用方事务（组合进更大事务，如 Submit
// 受理序）；类型化前门保留原名原签名，驱动路径自开事务。

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/run"
	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// eventFact 是一条 Outbox 事件事实（与聚合写同生共死；概念同 API 侧
// acceptance.go，engine 本地形态）。team 是资源轴归属 Team（P0-2）：
// ” = 平台级事件（对全部 Team 可见）。
type eventFact struct {
	name      string
	aggregate string
	id        string
	payload   []byte
	team      string
}

// eventTeamOfProject 解析 Project 轴的资源 Team（P0-2 事件 Team 铸入）：
// 行缺失/存储故障 → ”（平台级可见——不劣于止血前行为，不为归属解析
// 新增失败面；全量补齐由 P2 事实 kernel 承载）。
func (e *Engine) eventTeamOfProject(ctx context.Context, run state.Runner, projectID string) string {
	p, err := e.projects.Get(ctx, run, projectID)
	if err != nil {
		e.log.Warn("events: resolve project team", "project", projectID, "err", err)
		return ""
	}
	return p.TeamID
}

// eventTeamOfApp 解析 App 轴的资源 Team（deployment/build 聚合两跳：
// App → Project）。走 Any 档——App 删除后的收尾迁移事件仍可归属。
func (e *Engine) eventTeamOfApp(ctx context.Context, run state.Runner, appID string) string {
	a, err := e.apps.GetAnyByID(ctx, run, appID)
	if err != nil {
		e.log.Warn("events: resolve app team", "app", appID, "err", err)
		return ""
	}
	return e.eventTeamOfProject(ctx, run, a.ProjectID)
}

// ownerTeam 按观测归属域解析载体事件的资源轴 Team（P2 批二补齐，
// ADR-0066 A 化挂账收口——P0-2 止血时三态统一留 ”）：app 域走
// App→Project；database 域走 Database→Project（ADR-0029 载体）；managed/
// browse 是平台域——无 owner Team，返回 ”（平台级事件，终态口径）。
// 解析失败降级 ”（resolveEventTeams 同款契约：不为归属解析新增失败面）。
func (e *Engine) ownerTeam(ctx context.Context, owner workloadOwner) string {
	switch owner.domain {
	case ownerApp:
		return e.eventTeamOfApp(ctx, e.db.Runner(), owner.id)
	case ownerDatabase:
		db, err := e.databases.Get(ctx, e.db.Runner(), owner.id)
		if err != nil {
			e.log.Warn("events: resolve database team", "database", owner.id, "err", err)
			return ""
		}
		return e.eventTeamOfProject(ctx, e.db.Runner(), db.ProjectID)
	default:
		return "" // managed/browse：平台域载体，无 owner Team（平台级可见）
	}
}

// auditFact 是一条审计事实（ID/时间由脊柱铸造；actorCtx=true 时
// actor/source 取请求 ctx——用户动词路径，默认 SourceSystem）。
type auditFact struct {
	action   string
	resource string
	afterFP  string
	actorCtx bool
}

// transitionFact 声明一次状态迁移的全部事实。move 承担 CAS+刷新并判定
// 原地迁移（抑制事件）；event/audit 在其后按序求值；team 是事件落库前
// 资源轴 Team 的解析面（nil = 平台级事件——node/platform 等无 Team 轴
// 聚合；解析失败由 helper 内部降级 ”，P0-2）。
type transitionFact struct {
	aggregate string
	id        string
	move      func(ctx context.Context, tx *sql.Tx) (freshState string, inPlace bool, err error)
	event     func() eventFact
	team      func(ctx context.Context, tx *sql.Tx) string
	audit     func(freshState string) auditFact
	extras    []func(ctx context.Context, tx *sql.Tx) error
}

// commitTransition 在调用方事务内落完一次状态迁移四件一：
// CAS+刷新 → 事实扇出（事件（原地抑制）+ 审计经 kernel 单声明 FactRow
// 双行落库）→ 附加事实。任何一段失败整单回滚。
func (e *Engine) commitTransition(ctx context.Context, tx *sql.Tx, f transitionFact) error {
	freshState, inPlace, err := f.move(ctx, tx)
	if err != nil {
		return err
	}
	a := f.audit(freshState)
	row := writes.FactRow{
		Aggregate: f.aggregate, ID: f.id,
		Action: a.action, Resource: a.resource, AfterFP: a.afterFP,
	}
	if a.actorCtx {
		row.Actor, row.Source = authn.ActorFromContext(ctx), authn.SourceFromContext(ctx)
	}
	if inPlace {
		// LedgerOnly：原地迁移（落定状态仍在 from 集）不发事件——事件是
		// 状态迁移的既成事实，状态未变不是迁移；审计照落。
		row.LedgerOnly = true
	} else {
		ev := f.event()
		row.Name, row.Payload = ev.name, ev.payload
		if f.team != nil {
			row.TeamID = f.team(ctx, tx)
		}
	}
	if err := writes.Sequence(ctx, tx, e.factRepos(), func() writes.FactRow { return row }); err != nil {
		return err
	}
	for _, extra := range f.extras {
		if err := extra(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

// ---- 非迁移写（建行/字段更新）的序列真源 ----

// writeStep 是事务内的一段附加写（字段 CAS/派生建行）与其伴生事实；
// 序列与主写一致（写 → 事件 → 审计）。
type writeStep struct {
	write  func(ctx context.Context, tx *sql.Tx) error
	events []func() eventFact
	audits []auditFact
}

// writeFact 声明一次非迁移写路径的全部事实；零值段合法。checks 先行
// （拒绝零副作用，语义同 API 侧受理检查）；events 在 write 之后求值
// （负载可依赖事务内写的行）。
type writeFact struct {
	checks []func(ctx context.Context, tx *sql.Tx) error
	write  func(ctx context.Context, tx *sql.Tx) error
	events []func() eventFact
	audits []auditFact
	steps  []writeStep
}

// commitWrite 在调用方事务内落完一次写路径：检查 → 写 → 事实扇出，
// 附加写步殿后逐段重放同一序列。事实扇出与双行配对由事实发射 kernel
// 拥有（internal/writes，ADR-0064→收敛报告 §刀1）；本函数只做 engine
// writeFact 归一化与 steps 编排（域差异段不入 kernel）。
func (e *Engine) commitWrite(ctx context.Context, tx *sql.Tx, f writeFact) error {
	for _, check := range f.checks {
		if err := check(ctx, tx); err != nil {
			return err
		}
	}
	if f.write != nil {
		if err := f.write(ctx, tx); err != nil {
			return err
		}
	}
	if err := writes.Sequence(ctx, tx, e.factRepos(), e.writeKernelRows(ctx, f.events, f.audits)...); err != nil {
		return err
	}
	for _, s := range f.steps {
		if s.write != nil {
			if err := s.write(ctx, tx); err != nil {
				return err
			}
		}
		if err := writes.Sequence(ctx, tx, e.factRepos(), e.writeKernelRows(ctx, s.events, s.audits)...); err != nil {
			return err
		}
	}
	return nil
}

// factRepos 是 kernel 落库面的注入形态（engine 双 repo 句柄直装）。
func (e *Engine) factRepos() writes.Repos {
	return writes.Repos{Outbox: e.outbox, Audits: e.audits}
}

// writeKernelRows 把 engine 事件/审计事实组归一化为 kernel 单声明形态
// （主序列与 writeStep 共用）。迁移注记：双段声明（events/audits 各自
// 独立）是按域 A 化（ADR-0066 挂账）的收编对象——批二逐域并成默认
// 双真单声明；当前机械映射为单行事实（事件 → StreamOnly、审计 →
// LedgerOnly），落库序与既有 kernel 形态逐字节一致（事件行全先、审计
// 行殿后）。
func (e *Engine) writeKernelRows(ctx context.Context, events []func() eventFact, audits []auditFact) []func() writes.FactRow {
	var rows []func() writes.FactRow
	for _, ev := range events {
		ev := ev
		rows = append(rows, func() writes.FactRow {
			fact := ev()
			return writes.FactRow{
				Name: fact.name, Aggregate: fact.aggregate, ID: fact.id,
				Payload: fact.payload, TeamID: fact.team,
				StreamOnly: true, // 迁移面：取证孪生由 audits 段独立声明（A 化前形态）
			}
		})
	}
	for _, a := range audits {
		a := a
		rows = append(rows, func() writes.FactRow {
			row := writes.FactRow{
				Action: a.action, Resource: a.resource, AfterFP: a.afterFP,
				LedgerOnly: true, // 迁移面：流面孪生由 events 段独立声明（A 化前形态）
			}
			if a.actorCtx {
				// actor/source 取请求 ctx——用户动词路径（默认 SourceSystem
				// 由 kernel 缺省铸型；求值时点随本闭包保持在扇出段）。
				row.Actor, row.Source = authn.ActorFromContext(ctx), authn.SourceFromContext(ctx)
			}
			return row
		})
	}
	return rows
}

// emitObservation 是观测事实的 best-effort 发射面（kernel Emit 的便捷
// 形态；非事务——无聚合写伴生，错误由调用方自记日志）。StreamOnly：
// 观测事实只落流面（保留窗内），取证面不落——节点观测/漂移检测/平台
// 备份节拍等自治观测动作无操作者动词可审计。team 是资源轴归属 Team
// （"" = 平台级事件）。
func (e *Engine) emitObservation(ctx context.Context, team, name, aggregate, id string, payload []byte) error {
	return writes.Emit(ctx, e.db.Runner(), e.factRepos(), writes.FactRow{
		Name: name, Aggregate: aggregate, ID: id,
		Payload: payload, TeamID: team,
		StreamOnly: true, // 观测事实：仅流面（best-effort，无事务伴生）
	})
}

// ---- 类型化前门（保留原名原签名，调用点零改动） ----

// transit 是 Deployment 状态迁移四件一的类型化前门（caller-tx：Submit
// 受理序与 firstboot 游标推进组合进更大事务；部署记录无 tombstone）。
func (e *Engine) transit(ctx context.Context, tx *sql.Tx, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) error {
	var fresh *deployment.Deployment
	return e.commitTransition(ctx, tx, transitionFact{
		aggregate: "deployment",
		id:        d.ID,
		move: func(ctx context.Context, tx *sql.Tx) (string, bool, error) {
			if err := e.deployments.Transit(ctx, tx, d.ID, from, to, mut); err != nil {
				return "", false, err
			}
			var err error
			if fresh, err = e.deployments.Get(ctx, tx, d.ID); err != nil {
				return "", false, err
			}
			return string(fresh.State), fresh.State == to && depStateIn(from, to), nil
		},
		event: func() eventFact {
			return eventFact{name: eventStateName(fresh.State), aggregate: "deployment", id: d.ID, payload: deploymentEventPayloadJSON(fresh)}
		},
		team: func(ctx context.Context, tx *sql.Tx) string {
			return e.eventTeamOfApp(ctx, tx, fresh.AppID)
		},
		audit: func(freshState string) auditFact {
			// 审计 AfterFP（C5）：回放收口会把 to_revision 改写为实际运行的
			// 回放目标（终态事实修正）——该改写随审计指纹带出，消除"行上
			// 改了、审计看不见"的弯折。
			afterFP := freshState
			if fresh.ToRevision != d.ToRevision && fresh.ToRevision != "" {
				afterFP = fmt.Sprintf("%s; to_revision=%s", freshState, fresh.ToRevision)
			}
			return auditFact{action: "deployment.transit", resource: "deployment/" + d.ID, afterFP: afterFP}
		},
	})
}

// depStateIn 报告 to 是否在 from 集（原地迁移判定）。
func depStateIn(set []deployment.State, s deployment.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// transitAndReload 是驱动路径的迁移入口：自开事务完成四件一拍并返回
// 刷新后的行（drive 循环据此链式推进）。
func (e *Engine) transitAndReload(ctx context.Context, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) (*deployment.Deployment, error) {
	var fresh *deployment.Deployment
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.transit(ctx, tx, d, from, to, mut); err != nil {
			return err
		}
		var err error
		fresh, err = e.deployments.Get(ctx, tx, d.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return fresh, nil
}

// transitRunTx 是 Run 状态迁移四件一（caller-tx 形态；原地回写调用方行；
// 行不删）。
func (e *Engine) transitRunTx(ctx context.Context, tx *sql.Tx, m *run.Run, from []run.State, to run.State, mut func(*run.Run)) error {
	var fresh *run.Run
	return e.commitTransition(ctx, tx, transitionFact{
		aggregate: "run",
		id:        m.ID,
		move: func(ctx context.Context, tx *sql.Tx) (string, bool, error) {
			if err := e.runs.Transit(ctx, tx, m.ID, from, to, mut); err != nil {
				return "", false, err
			}
			var err error
			if fresh, err = e.runs.Get(ctx, tx, m.ID); err != nil {
				return "", false, err
			}
			*m = *fresh
			return string(fresh.State), fresh.State == to && runStateIn(from, to), nil
		},
		event: func() eventFact {
			return eventFact{name: eventRunState(fresh.State), aggregate: "run", id: m.ID, payload: runEventPayloadJSON(fresh)}
		},
		team: func(ctx context.Context, tx *sql.Tx) string {
			return e.eventTeamOfProject(ctx, tx, fresh.ProjectID)
		},
		audit: func(freshState string) auditFact {
			return auditFact{action: "run.transit", resource: "run/" + m.ID, afterFP: freshState}
		},
	})
}

// runStateIn 报告 to 是否在 from 集（原地迁移判定）。
func runStateIn(set []run.State, s run.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// transitRunFour 是 Run 状态迁移四件一（自开事务；原地回写调用方行）。
func (e *Engine) transitRunFour(ctx context.Context, m *run.Run, from []run.State, to run.State, mut func(*run.Run)) error {
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.transitRunTx(ctx, tx, m, from, to, mut)
	})
}

// transitBuild 是 Build 的四件一（自开事务；终态触发日志缓冲回收）。
func (e *Engine) transitBuild(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error) {
	var fresh *build.Build
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.commitTransition(ctx, tx, transitionFact{
			aggregate: "build",
			id:        b.ID,
			move: func(ctx context.Context, tx *sql.Tx) (string, bool, error) {
				if err := e.builds.Transit(ctx, tx, b.ID, from, to, mut); err != nil {
					return "", false, err
				}
				var err error
				if fresh, err = e.builds.Get(ctx, tx, b.ID); err != nil {
					return "", false, err
				}
				return string(fresh.State), fresh.State == to && buildStateIn(from, to), nil
			},
			event: func() eventFact {
				return eventFact{name: eventBuildState(fresh.State), aggregate: "build", id: b.ID, payload: buildEventPayloadJSON(fresh)}
			},
			team: func(ctx context.Context, tx *sql.Tx) string {
				return e.eventTeamOfApp(ctx, tx, fresh.AppID)
			},
			audit: func(freshState string) auditFact {
				return auditFact{action: "build.transit", resource: "build/" + b.ID, afterFP: freshState}
			},
		})
	})
	if err != nil {
		return nil, err
	}
	if fresh.State.Terminal() {
		// 终态登记触发超龄缓冲回收（frames map 只增不清会泄漏，N0.1 P2-1）。
		e.build.logs.markTerminal(b.ID)
	}
	return fresh, nil
}

// buildStateIn 报告 to 是否在 from 集（原地迁移判定）。
func buildStateIn(set []build.State, s build.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// taskTransitOpts 覆盖 Task 迁移四件一的默认面（全部可选）：reason 进
// task.draining 等事件负载（起因）；auditAction 默认 "task.transit"；
// auditAfterFP 覆盖默认指纹（迁移后状态）；auditActorCtx 把审计来源从
// system 切到请求路径（用户动词：stop/update/renew/delete）；updateEvent
// 非空时原地迁移也发该事件（"变化"事件而非状态事件——ScaleTask 的
// task.updated）。
type taskTransitOpts struct {
	reason        string
	auditAction   string
	auditAfterFP  string
	auditActorCtx bool
	updateEvent   string
}

// transitTaskTx 是 Task 状态迁移四件一（caller-tx 形态；原地回写调用方
// 行）。审计强制落行（D4 裁决，2026-10-03：收口前该域拷贝注释承诺审计、
// 代码未落——五份拷贝漂移的实锤之一；架构 §6"一切写操作留痕"补齐）。
func (e *Engine) transitTaskTx(ctx context.Context, tx *sql.Tx, t *task.Task, from []task.State, to task.State, mut func(*task.Task), opts taskTransitOpts) error {
	var fresh *task.Task
	return e.commitTransition(ctx, tx, transitionFact{
		aggregate: "task",
		id:        t.ID,
		move: func(ctx context.Context, tx *sql.Tx) (string, bool, error) {
			if err := e.tasks.Transit(ctx, tx, t.ID, from, to, mut); err != nil {
				return "", false, err
			}
			var err error
			if fresh, err = e.tasks.Get(ctx, tx, t.ID); err != nil {
				return "", false, err
			}
			*t = *fresh
			inPlace := fresh.State == to && taskStateIn(from, to)
			if opts.updateEvent != "" {
				inPlace = false // 更新事实事件：原地也发（非状态事件不受抑制规则约束）
			}
			return string(fresh.State), inPlace, nil
		},
		event: func() eventFact {
			name := eventTaskState(fresh.State)
			if opts.updateEvent != "" {
				name = opts.updateEvent
			}
			return eventFact{name: name, aggregate: "task", id: t.ID, payload: taskEventPayloadJSON(fresh, opts.reason)}
		},
		team: func(ctx context.Context, tx *sql.Tx) string {
			return e.eventTeamOfProject(ctx, tx, fresh.ProjectID)
		},
		audit: func(freshState string) auditFact {
			action := opts.auditAction
			if action == "" {
				action = "task.transit"
			}
			afterFP := opts.auditAfterFP
			if afterFP == "" {
				afterFP = freshState
			}
			return auditFact{action: action, resource: "task/" + t.ID, afterFP: afterFP, actorCtx: opts.auditActorCtx}
		},
	})
}

// taskStateIn 报告 to 是否在 from 集（原地迁移判定）。
func taskStateIn(set []task.State, s task.State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

// transitTaskFour 是 Task 状态迁移四件一（自开事务；原地回写调用方行）。
func (e *Engine) transitTaskFour(ctx context.Context, t *task.Task, from []task.State, to task.State, mut func(*task.Task)) error {
	return e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.transitTaskTx(ctx, tx, t, from, to, mut, taskTransitOpts{})
	})
}
