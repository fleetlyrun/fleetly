package fleetlygrpc

// 受理位（Acceptance，ADR-0024/F1.1 阶段 3）：api 层创建型/删除型写请求
// 的受理判定面。此前四件一拍（聚合写 + Outbox 事件 + 审计）在 api 手写
// 29 处、受理守卫 7 族散落各 handler。事实双行落库的唯一拥有者是事实
// 发射 kernel（internal/domain/services/writes，P2 刀 1）；写编排
// （事务作用域 + 检查 + 写 + 事实扇出）的唯一拥有者自 P3b 起是 engine
// 写原语（internal/application/engine 的 Commit/WriteFact，ADR-0087
// 决策 4 写原语腿——ADR-0024 决策 4「engine transit 与 api 写路径共用」
// 的兑现）：
//
//   - commit 是受理位的写路径入口，薄委托 engine 单份原语——受理检查
//     全部同事务（ADR-0024 决策 3：父资源存活、配额、删除守卫、FK 归属
//     的读在事务内，所见即受理终局；配额读由此移入事务，PutConfig
//     TOCTOU 收口）。新写面必须走本入口，手写 DB.Tx 编排在守卫
//     TestAcceptanceWritePathsGoThroughCommit / TestNoHandRolledTxChoreography
//     反扫下红；
//   - 受理检查/写事实声明经别名单源 engine（acceptanceCheck/writeFact
//     = api.Check/api.WriteFact）——两侧各持一份声明形态的双真源
//     面已收口，配对与段序单点在 transition.go。
//
// A 化（ADR-0066 兑现，P2 批二）：写事实单声明（Facts 直接铸 kernel 的
// FactRow，默认双真——outbox 流面 + audit 取证面同源落库），过渡映射层
// （events/audits 双段声明 + writeFactRows 机械并单）已收壳。偏离双真
// （LedgerOnly/StreamOnly）在声明处显式带旗并注释理由。
//
// 幂等执法（internal/idem 拦截器）位于本层之外、auth 之后——受理位答
// "收不收"，幂等答"重放还是执行"，Admission（engine）答"怎么排"。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// acceptanceCheck 是一条受理位检查的声明面（单源 api.Check：事务内
// 运行，非 nil 错误即整单回滚；事务作用域 = transact.Stores，ADR-0080）。
type acceptanceCheck = grpcapi.Check

// writeFact 声明一次写路径的全部事实（单源 api.WriteFact）；零值段
// （无受理检查/无事实）合法。Facts 是 kernel 单声明事实（默认双真），
// 惰性求值——Sequence 在聚合写之后逐条求值，载荷/指纹可依赖事务内写的
// 返回行（行 id 铸出、Revoke 的返回行等经闭包捕获自然可见，无需就地
// 填充面）。
type writeFact = grpcapi.WriteFact

// commit 是受理位的写路径入口：薄委托 engine 统一写原语（ADR-0087
// 决策 4——受理检查 → 聚合写 → 事实扇出的事务编排单份住 engine
// transition.go，本层不再自带实现；检查拒绝零副作用、任一段失败整单
// 回滚，事实的双行配对与段间失败语义由 kernel 执法）。
func (s *Services) commit(ctx context.Context, fact writeFact) error {
	return grpcapi.Commit(ctx, s.writes, fact)
}

// factOf 构造一条资源轴归属 Project 的事实声明：Team 在进事务前解析
// （P0-2 契约——事务外读与既有 resolveEventTeams 同款时序），row 闭包在
// 求值时把解析产物铸入 FactRow.TeamID。
//
// fail-closed（R08-M8）：归属解析失败（空 project_id / 行缺失 / 存储故障 /
// 解析结果空 Team）→ 返回 error，调用方整单拒绝（写与事件/审计均不落）。
// 绝不把解析失败吞成 TeamID=""——空 Team 行按 outbox 过滤口径对**全部
// Team** 可见，那正是跨队事件泄漏的 fail-open 单点。TeamID="" 的平台级
// 事实必须经 platformFact 显式构造（无 Team 轴的理由写在调用点）。
func (s *Services) factOf(ctx context.Context, projectID string, row func(team string) writes.FactRow) (func() writes.FactRow, error) {
	return grpcapi.FactOf(ctx, s.Projects, projectID, row)
}

// platformFact 是平台级事实（TeamID=""、全员可见）的唯一显式构造器：只用于
// 无 Team 轴的平台聚合（platform 聚合、node 平台事件、模板目录等）。资源轴
// 事实必须走 factOf——此处是"空 Team 需显式理由"纪律的语法载体（调用点
// 注释必须写明为何对全体 Team 可见）。
func platformFact(row func() writes.FactRow) func() writes.FactRow {
	return grpcapi.PlatformFact(row)
}

// ---- 受理检查族（可枚举面；守卫对账对象） ----

// parentProjectAlive：Project 存活（ADR-0023 活跃行口径：tombstone 后一律
// 不存在）。全部"创建时引用父 Project"的写面共用。与 DeleteProject 的
// 活跃 App 守卫构成对偶——单写者事务串行下任一交错都满足"项目存活 ⇔
// 子资源可建"。
func (s *Services) parentProjectAlive(projectID string) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		return grpcapi.RequireActiveProject(ctx, st, projectID)
	}
}

// taskQuota：Task 数量与并发总量配额（ADR-0017 附录 A.1，F1.9）：非终态
// Task 行数与 desired_concurrency 之和双上限；desired 是本单新增量（one-shot
// 归一后 ≥1，resident 缩零初态合法为 0）。收口 F1.5 挂账的 per-Task
// desired_concurrency sanity 上限——per-Task 上限由项目总量承载。读在
// 事务内（SQLite 单写连接串行，与写同事务无 TOCTOU）。配额定律经
// api.CheckTaskQuota 单源（ADR-0068）；哨兵经 mapTaskQuotaError 统一
// 转义（与 scale/fire 动词同文案）。
func (s *Services) taskQuota(projectID string, desired int64) acceptanceCheck {
	return func(ctx context.Context, st transact.Stores) error {
		active, sum, err := st.Tasks.StatsByProject(ctx, projectID)
		if err != nil {
			return err
		}
		if err := api.CheckTaskQuota(projectID, active, sum, desired, true); err != nil {
			return mapTaskQuotaError(err)
		}
		return nil
	}
}

// ---- 归属校验族（identity FK 面；行不存在 → repo 哨兵 ErrNotFound →
// mapStateError 映射 E_NOT_FOUND） ----
