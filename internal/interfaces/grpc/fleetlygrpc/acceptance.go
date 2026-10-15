package fleetlygrpc

// 受理位（Acceptance，ADR-0024/F1.1 阶段 3）：api 层创建型/删除型写请求
// 的受理判定面 + 统一写原语。此前四件一拍（聚合写 + Outbox 事件 + 审计）
// 在 api 手写 29 处、受理守卫 7 族散落各 handler。段序与回滚的唯一拥有者
// 是写序列 kernel（internal/writes，ADR-0064——与 engine 驱动域 commitWrite
// 同一序列真源）；本文件是受理位的形态面：
//
//   - commit 拥有受理位的写路径入口（受理检查 → 聚合写 → 事件 → 审计的
//     段序由 kernel 执法）——新写面必须走本原语，手写 DB.Tx 编排在守卫
//     TestAcceptanceWritePathsGoThroughCommit / TestNoHandRolledTxChoreography
//     反扫下红；
//   - 受理检查全部事务内运行（所见即受理终局）：父资源存活、配额、删除
//     守卫、FK 归属。配额读由此移入事务（PutConfig TOCTOU 收口）。
//
// 幂等执法（internal/idem 拦截器）位于本层之外、auth 之后——受理位答
// "收不收"，幂等答"重放还是执行"，Admission（engine）答"怎么排"。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// acceptanceCheck 是一条受理位检查：事务内运行，非 nil 错误即整单回滚。
type acceptanceCheck func(ctx context.Context, tx *sql.Tx) error

// eventFact 是一条 Outbox 事件事实（与聚合写同生共死）。team 是资源轴
// 归属 Team（P0-2）：” = 平台级事件（对全部 Team 可见）。projectID 是
// 资源轴解析锚——team 未显式铸入时由 commit 依 Project 行解析（Project
// 的 Team 归属不可变，受理解析与事务写入无 TOCTOU 面）。
type eventFact struct {
	name      string
	aggregate string
	id        string
	payload   []byte
	team      string
	projectID string
}

// writeFact 声明一次写路径的全部事实；零值段（无受理检查/无事件）合法。
// auditsFrom 在 write 段之后求值——审计字段依赖事务内写的返回行、或审计
// 本身条件性（如 Revision 内容寻址复用不落新审计）的场合。
type writeFact struct {
	checks     []acceptanceCheck
	write      func(ctx context.Context, tx *sql.Tx) error
	events     []eventFact
	audits     []*audit.Entry
	auditsFrom func() []*audit.Entry
}

// commit 在单个事务内落完一次写路径：受理检查先行（拒绝零副作用）、
// 聚合写居中、事件与审计殿后。段序与段间失败语义由写序列 kernel 拥有
// （internal/writes，ADR-0064）；本原语是受理位入口与 writeFact 形态面。
// 进事务前先补铸事件的资源轴 Team（P0-2）。
func (s *Services) commit(ctx context.Context, fact writeFact) error {
	s.resolveEventTeams(ctx, fact.events)
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		return writes.Sequence(ctx, tx, s.OutboxEvents, s.Audits, s.writeKernelFact(fact))
	})
}

// resolveEventTeams 补铸事件的资源轴 Team（P0-2）：显式 team 优先；其余
// 按 projectID 锚经 Project 行解析（行缺失/存储故障 → ” 平台级可见，
// 不为归属解析新增失败面；全量补齐由 P2 事实 kernel 承载）。就地写切片
// 元素——kernel 闭包按同一底层数组读取（writeKernelFact 注释同款契约）。
func (s *Services) resolveEventTeams(ctx context.Context, events []eventFact) {
	for i := range events {
		if events[i].team != "" || events[i].projectID == "" {
			continue
		}
		p, err := s.Projects.Get(ctx, s.DB.Runner(), events[i].projectID)
		if err != nil {
			continue // 解析失败降级 ''：事件可见面不窄于止血前行为
		}
		events[i].team = p.TeamID
	}
}

// writeKernelFact 把受理位 writeFact 归一化为写序列 kernel 形态：事件闭包
// 捕获现有可变 eventFact（withPayload 就地填充在追加时读取，语义逐字节
// 保持）；audits/auditsFrom 并为审计组——write 段后求值语义不变（组在
// kernel 事件段后求值，auditsFrom 依赖事务内写的返回行照旧）。
func (s *Services) writeKernelFact(fact writeFact) writes.Fact {
	checks := make([]func(context.Context, *sql.Tx) error, len(fact.checks))
	for i, c := range fact.checks {
		checks[i] = c
	}
	f := writes.Fact{Checks: checks, Write: fact.write}
	for i := range fact.events {
		// 闭包读切片元素（底层数组与调用方共享）：write 段的就地填充
		// （withPayload/直接赋值）在追加时可见。按值拷贝会让填充静默
		// 失效（事件落空 payload——本注释修订前正是这个形态）。
		f.Events = append(f.Events, func() writes.Event {
			e := fact.events[i]
			return writes.Event{Name: e.name, Aggregate: e.aggregate, ID: e.id, Payload: e.payload, Team: e.team}
		})
	}
	if len(fact.audits) > 0 || fact.auditsFrom != nil {
		audits, from := fact.audits, fact.auditsFrom
		f.Audits = append(f.Audits, func(context.Context) []*audit.Entry {
			if from == nil {
				return audits
			}
			return append(append([]*audit.Entry(nil), audits...), from()...)
		})
	}
	return f
}

// ---- 受理检查族（可枚举面；守卫对账对象） ----

// parentProjectAlive：Project 存活（ADR-0023 活跃行口径：tombstone 后一律
// 不存在）。全部"创建时引用父 Project"的写面共用。与 DeleteProject 的
// 活跃 App 守卫构成对偶——单写者事务串行下任一交错都满足"项目存活 ⇔
// 子资源可建"。
func (s *Services) parentProjectAlive(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		return s.requireActiveProject(ctx, tx, projectID)
	}
}

// projectAppAlive：App 在指定 Project 内存活（Route 的父引用面：apps
// 活跃行口径 + 归属一致——跨项目悬空引用按"本项目无此 App"拒绝）。
func (s *Services) projectAppAlive(projectID, appID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		return s.requireProjectApp(ctx, tx, projectID, appID)
	}
}

// noActiveApps：DeleteProject 的删除守卫——项目下有未删 App 即拒
// （E_CONFLICT，先删 App）。Project 级材料不级联、不代删（各自生命周期）。
// 事务内守卫与 DeleteProject 的无锁预检（先拒零副作用，再进库级联）经
// rejectActiveApps 共用同一核（文案单源）。
func (s *Services) noActiveApps(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		return s.rejectActiveApps(ctx, tx, projectID)
	}
}

// rejectActiveApps 是活跃 App 拒绝判定的运行器无关核（*sql.Tx 与
// *sql.DB 同满足 state.Runner）：事务内守卫与 DeleteProject 入口的
// 无锁预检共用。
func (s *Services) rejectActiveApps(ctx context.Context, run state.Runner, projectID string) error {
	apps, err := s.Apps.ListByProject(ctx, run, projectID)
	if err != nil {
		return err
	}
	if len(apps) > 0 {
		return apperr.New("E_CONFLICT",
			"project %s still holds %d app(s); delete them before deleting the project",
			projectID, len(apps))
	}
	return nil
}

// noActiveDatabases：DeleteProject 的级联复查守卫（2026-10-05 评审批
// 台账 #4 / ADR-0029 追记）——项目删除对活跃 Database 级联收口（先拆
// 载体后落账），本守卫闭合"级联与项目 tombstone 落账之间并发建库"的
// 窗口：命中即拒（重试项目删除即把新库纳入级联）。与 noActiveApps 的
// 处置分立：App 是手工前置面（先删 App 才受理），Database 是级联面
// （项目删除自己收口）。
func (s *Services) noActiveDatabases(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		n, err := s.Databases.CountByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.New("E_CONFLICT",
				"project %s still holds %d database(s) created while the delete was cascading; retry the project delete to cascade them",
				projectID, n)
		}
		return nil
	}
}

// configQuota：PutConfig 的数量配额（per-Project 配置名数；同名 put 是新
// 版本不占新位）。读在事务内（ADR-0024：配额读与写同事务，收口批 0 复核
// 指出的先读后写 TOCTOU）。
func (s *Services) configQuota(projectID, name string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		existing, err := s.Configs.Latest(ctx, tx, projectID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if existing != nil {
			return nil
		}
		latest, err := s.Configs.LatestByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if len(latest) >= maxConfigsPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d configs (limit %d)", projectID, len(latest), maxConfigsPerProject)
		}
		return nil
	}
}

// taskQuota：Task 数量与并发总量配额（ADR-0017 附录 A.1，F1.9）：非终态
// Task 行数与 desired_concurrency 之和双上限；desired 是本单新增量（one-shot
// 归一后 ≥1，resident 缩零初态合法为 0）。收口 F1.5 挂账的 per-Task
// desired_concurrency sanity 上限——per-Task 上限由项目总量承载。读在
// 事务内（SQLite 单写连接串行，与写同事务无 TOCTOU）。配额定律经
// engine.CheckTaskQuota 单源（ADR-0068）；哨兵经 mapTaskQuotaError 统一
// 转义（与 scale/fire 动词同文案）。
func (s *Services) taskQuota(projectID string, desired int64) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		active, sum, err := s.Tasks.StatsByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if err := engine.CheckTaskQuota(projectID, active, sum, desired, true); err != nil {
			return mapTaskQuotaError(err)
		}
		return nil
	}
}

// appQuota：活跃 App 数配额（ADR-0017 附录 A.1，F1.9；同族受理检查）。
func (s *Services) appQuota(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		n, err := s.Apps.CountByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if n >= maxAppsPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d apps (limit %d)", projectID, n, maxAppsPerProject)
		}
		return nil
	}
}

// databaseQuota：活跃 Database 数配额（ADR-0029 决策 10；同族受理检查——
// 库是长驻真实资源）。
func (s *Services) databaseQuota(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		n, err := s.Databases.CountByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if n >= maxDatabasesPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d databases (limit %d); delete unused ones first",
				projectID, n, maxDatabasesPerProject)
		}
		return nil
	}
}

// projectHasNetwork：CreateDatabase 的可达性前置（ADR-0029 决策 5）——
// 数据库挂全部活跃项目网，零网项目的库不可达；拒绝优于静默孤岛。
func (s *Services) projectHasNetwork(projectID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		nets, err := s.Networks.ListByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if len(nets) == 0 {
			return apperr.New("E_CONFLICT",
				"project %s has no networks; a database attaches the project's active networks and would be unreachable - create a network first ('fleetly networks create --project %s default')",
				projectID, projectID)
		}
		return nil
	}
}

// structureEvent 构造结构面事件的 Outbox 事实（负载字段只增）。projectID
// 同时充当资源轴 Team 的解析锚（P0-2，resolveEventTeams）。
func structureEvent(name, aggregate, id, projectID string) eventFact {
	payload, _ := json.Marshal(structureEventPayload{ID: id, ProjectID: projectID}) //nolint:errcheck // 结构体字段恒可序列化
	return eventFact{name: name, aggregate: aggregate, id: id, payload: payload, projectID: projectID}
}

// identityEvent 构造身份面事件的 Outbox 事实（payload 是最小 JSON 面）。
func identityEvent(name, aggregate, id string, payload any) eventFact {
	data, _ := json.Marshal(payload) //nolint:errcheck // map 载荷恒可序列化
	return eventFact{name: name, aggregate: aggregate, id: id, payload: data}
}

// withPayload 返回替换载荷后的事件事实（载荷字段来自事务内写的返回行时，
// 经切片元素就地填充：write 段先于 events 段执行）。
func (e eventFact) withPayload(v any) eventFact {
	data, _ := json.Marshal(v) //nolint:errcheck // map 载荷恒可序列化
	e.payload = data
	return e
}

// ---- 归属校验族（identity FK 面；行不存在 → repo 哨兵 ErrNotFound →
// mapStateError 映射 E_NOT_FOUND） ----

// teamExists：创建型请求引用的 Team 必须存在。
func (s *Services) teamExists(id string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.Teams.Get(ctx, tx, id)
		return err
	}
}

// roleInTeam：Role 存在性 + 与 Team 归属一致性（Q-16，ADR-0028）。内置
// 角色是平台级模板（team_id 空），可在任意 Team 授予；自定义 Role 必须
// 属于同一 Team，跨 Team 引用 409。
func (s *Services) roleInTeam(roleID, teamID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		ro, err := s.Roles.Get(ctx, tx, roleID)
		if err != nil {
			return err
		}
		if ro.Builtin {
			return nil
		}
		if ro.TeamID != teamID {
			return apperr.New("E_CONFLICT",
				"role %s belongs to team %s, not team %s; grant a role within the team", roleID, ro.TeamID, teamID)
		}
		return nil
	}
}

// userExists：创建型请求显式引用的 User 必须存在（可缺省）。
func (s *Services) userExists(id string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.Users.Get(ctx, tx, id)
		return err
	}
}

// userInTeam：有属主 Token 的铸造位收口（ADR-0038）——creator 必须在目标
// Team 持 membership，否则 Token 在实时收窄语义下铸出即死（求交恒空）。
// 无 owner 豁免：给无 membership 的用户铸跨队 Token 从来不是合法运维。
func (s *Services) userInTeam(userID, teamID string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		_, err := s.Memberships.GetByUser(ctx, tx, userID, teamID)
		if err != nil {
			return apperr.New("E_CONFLICT",
				"user %s holds no membership in team %s; a user-bound token is capped by its creator's current access (ADR-0038) and would carry no authority", userID, teamID)
		}
		return nil
	}
}
