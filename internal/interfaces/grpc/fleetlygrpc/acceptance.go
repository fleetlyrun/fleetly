package fleetlygrpc

// 受理位（Acceptance，ADR-0024/F1.1 阶段 3）：api 层创建型/删除型写请求
// 的受理判定面 + 统一写原语。此前四件一拍（聚合写 + Outbox 事件 + 审计）
// 在 api 手写 29 处、受理守卫 7 族散落各 handler。事实双行落库的唯一
// 拥有者是事实发射 kernel（internal/domain/services/writes，P2 刀 1）；
// 本文件是受理位的形态面：
//
//   - commit 拥有受理位的写路径入口（检查 → 聚合写 → 事实扇出：检查
//     拒绝零副作用、任一段失败整单回滚；事实的双行配对与段间失败语义
//     由 kernel 执法）——新写面必须走本原语，手写 DB.Tx 编排在守卫
//     TestAcceptanceWritePathsGoThroughCommit / TestNoHandRolledTxChoreography
//     反扫下红；
//   - 受理检查全部事务内运行（所见即受理终局）：父资源存活、配额、删除
//     守卫、FK 归属。配额读由此移入事务（PutConfig TOCTOU 收口）。
//
// A 化（ADR-0066 兑现，P2 批二）：写事实单声明（writeFact.facts 直接铸
// kernel 的 FactRow，默认双真——outbox 流面 + audit 取证面同源落库），
// 过渡映射层（events/audits 双段声明 + writeFactRows 机械并单）已收壳。
// 偏离双真（LedgerOnly/StreamOnly）在声明处显式带旗并注释理由。
//
// 幂等执法（internal/idem 拦截器）位于本层之外、auth 之后——受理位答
// "收不收"，幂等答"重放还是执行"，Admission（engine）答"怎么排"。

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
)

// acceptanceCheck 是一条受理位检查：事务内运行，非 nil 错误即整单回滚
// （事务作用域 = transact.Stores，ADR-0080）。
type acceptanceCheck func(ctx context.Context, st transact.Stores) error

// writeFact 声明一次写路径的全部事实；零值段（无受理检查/无事实）合法。
// facts 是 kernel 单声明事实（默认双真），惰性求值——Sequence 在聚合写
// 之后逐条求值，载荷/指纹可依赖事务内写的返回行（行 id 铸出、Revoke 的
// 返回行等经闭包捕获自然可见，无需就地填充面）。
type writeFact struct {
	checks []acceptanceCheck
	write  func(ctx context.Context, st transact.Stores) error
	facts  []func() writes.FactRow
}

// commit 在单个事务内落完一次写路径：受理检查先行（拒绝零副作用）、
// 聚合写居中、事实扇出殿后（双行配对与段间失败语义由事实发射 kernel
// 拥有，internal/writes）；本原语是受理位入口与 writeFact 形态面。
func (s *Services) commit(ctx context.Context, fact writeFact) error {
	return s.Tx.Do(ctx, func(st transact.Stores) error {
		for _, check := range fact.checks {
			if err := check(ctx, st); err != nil {
				return err
			}
		}
		if fact.write != nil {
			if err := fact.write(ctx, st); err != nil {
				return err
			}
		}
		return writes.Sequence(ctx, s.factRepos(st), fact.facts...)
	})
}

// factRepos 是 kernel 落库面的注入形态（tx 作用域 Stores 直取双面）。
func (s *Services) factRepos(st transact.Stores) writes.Repos {
	return writes.Repos{Outbox: st.Outbox, Audits: st.Audits}
}

// factOf 构造一条资源轴归属 Project 的事实声明：Team 在进事务前解析
// （P0-2 契约——显式空串/行缺失/存储故障 → "" 平台级可见，不为归属
// 解析新增失败面；事务外读与既有 resolveEventTeams 同款时序），row 闭包
// 在求值时把解析产物铸入 FactRow.TeamID。归属 Team 在手（identity 域）
// 或无 Team 轴（平台级材料）的场合不经本助手，直接声明闭包。
func (s *Services) factOf(ctx context.Context, projectID string, row func(team string) writes.FactRow) func() writes.FactRow {
	team := ""
	if projectID != "" {
		if p, err := s.Projects.Get(ctx, projectID); err == nil {
			team = p.TeamID
		}
	}
	return func() writes.FactRow { return row(team) }
}

// ---- 受理检查族（可枚举面；守卫对账对象） ----

// parentProjectAlive：Project 存活（ADR-0023 活跃行口径：tombstone 后一律
// 不存在）。全部"创建时引用父 Project"的写面共用。与 DeleteProject 的
// 活跃 App 守卫构成对偶——单写者事务串行下任一交错都满足"项目存活 ⇔
// 子资源可建"。
func (s *Services) parentProjectAlive(projectID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return s.requireActiveProject(ctx, st, projectID)
	}
}

// projectAppAlive：App 在指定 Project 内存活（Route 的父引用面：apps
// 活跃行口径 + 归属一致——跨项目悬空引用按"本项目无此 App"拒绝）。
func (s *Services) projectAppAlive(projectID, appID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return s.requireProjectApp(ctx, st, projectID, appID)
	}
}

// noActiveApps：DeleteProject 的删除守卫——项目下有未删 App 即拒
// （E_CONFLICT，先删 App）。Project 级材料不级联、不代删（各自生命周期）。
// 事务内守卫与 DeleteProject 的无锁预检（先拒零副作用，再进库级联）经
// rejectActiveApps 共用同一核（文案单源）。
func (s *Services) noActiveApps(projectID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return s.rejectActiveApps(ctx, st.Apps, projectID)
	}
}

// rejectActiveApps 是活跃 App 拒绝判定的核（repo 注入——事务内传
// st.Apps、DeleteProject 入口无锁预检传 db 绑定面，同一文案单源）。
func (s *Services) rejectActiveApps(ctx context.Context, apps app.Repo, projectID string) error {
	rows, err := apps.ListByProject(ctx, projectID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return apperr.New("E_CONFLICT",
			"project %s still holds %d app(s); delete them before deleting the project",
			projectID, len(rows))
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
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Databases.CountByProject(ctx, projectID)
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
	return func(ctx context.Context, st transact.Stores) error {
		existing, err := st.Configs.Latest(ctx, projectID, name)
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		if existing != nil {
			return nil
		}
		latest, err := st.Configs.LatestByProject(ctx, projectID)
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
	return func(ctx context.Context, st transact.Stores) error {
		active, sum, err := st.Tasks.StatsByProject(ctx, projectID)
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
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Apps.CountByProject(ctx, projectID)
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
	return func(ctx context.Context, st transact.Stores) error {
		n, err := st.Databases.CountByProject(ctx, projectID)
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
	return func(ctx context.Context, st transact.Stores) error {
		nets, err := st.Networks.ListByProject(ctx, projectID)
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

// ---- 归属校验族（identity FK 面；行不存在 → repo 哨兵 ErrNotFound →
// mapStateError 映射 E_NOT_FOUND） ----

// teamExists：创建型请求引用的 Team 必须存在。
func (s *Services) teamExists(id string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Teams.Get(ctx, id)
		return err
	}
}

// roleInTeam：Role 存在性 + 与 Team 归属一致性（Q-16，ADR-0028）。内置
// 角色是平台级模板（team_id 空），可在任意 Team 授予；自定义 Role 必须
// 属于同一 Team，跨 Team 引用 409。
func (s *Services) roleInTeam(roleID, teamID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		ro, err := st.Roles.Get(ctx, roleID)
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
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Users.Get(ctx, id)
		return err
	}
}

// userInTeam：有属主 Token 的铸造位收口（ADR-0038）——creator 必须在目标
// Team 持 membership，否则 Token 在实时收窄语义下铸出即死（求交恒空）。
// 无 owner 豁免：给无 membership 的用户铸跨队 Token 从来不是合法运维。
func (s *Services) userInTeam(userID, teamID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Memberships.GetByUser(ctx, userID, teamID)
		if err != nil {
			return apperr.New("E_CONFLICT",
				"user %s holds no membership in team %s; a user-bound token is capped by its creator's current access (ADR-0038) and would carry no authority", userID, teamID)
		}
		return nil
	}
}
