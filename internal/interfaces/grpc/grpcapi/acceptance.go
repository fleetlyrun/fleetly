package grpcapi

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
)

// Check 是一条受理位检查的声明面（单源 api.Check：事务内运行，非 nil
// 错误即整单回滚；事务作用域 = transact.Stores，ADR-0080）。
type Check = api.Check

// WriteFact 声明一次写路径的全部事实（单源 api.WriteFact）；零值段
// （无受理检查/无事实）合法。Facts 是 kernel 单声明事实（默认双真），
// 惰性求值——Sequence 在聚合写之后逐条求值，载荷/指纹可依赖事务内写的
// 返回行（行 id 铸出、Revoke 的返回行等经闭包捕获自然可见，无需就地
// 填充面）。
type WriteFact = api.WriteFact

// WriteAPI 是受理位写原语的共享消费面（engine.Commit 的结构实现）。
type WriteAPI interface {
	Commit(ctx context.Context, f api.WriteFact) error
}

// Commit 是受理位的写路径入口：薄委托 engine 统一写原语（ADR-0087 决策
// 4——受理检查 → 聚合写 → 事实扇出的事务编排单份住 engine transition.go）。
func Commit(ctx context.Context, w WriteAPI, fact WriteFact) error {
	return w.Commit(ctx, fact)
}

// TeamExists 校验 Team 行存在（冻结等 Team 轴受理检查共用）。
func TeamExists(id string) Check {
	return func(ctx context.Context, st transact.Stores) error {
		_, err := st.Teams.Get(ctx, id)
		return err
	}
}

// FactRepos 是 kernel 落库面的注入形态（tx 作用域 Stores 直取双面）——
// 条件事实（写段内分支才知是否发射）经它在同一事务内直调 kernel，不导出
// 新的声明面。
func FactRepos(st transact.Stores) writes.Repos {
	return writes.Repos{Outbox: st.Outbox, Audits: st.Audits}
}

// FactOf 构造一条资源轴归属 Project 的事实声明：Team 在进事务前解析
// （P0-2 契约——事务外读与既有 resolveEventTeams 同款时序），row 闭包在
// 求值时把解析产物铸入 FactRow.TeamID。
//
// fail-closed（R08-M8）：归属解析失败（空 project_id / 行缺失 / 存储故障 /
// 解析结果空 Team）→ 返回 error，调用方整单拒绝（写与事件/审计均不落）。
// 绝不把解析失败吞成 TeamID=""——空 Team 行按 outbox 过滤口径对**全部
// Team** 可见，那正是跨队事件泄漏的 fail-open 单点。TeamID="" 的平台级
// 事实必须经 PlatformFact 显式构造（无 Team 轴的理由写在调用点）。
func FactOf(ctx context.Context, projects project.Repo, projectID string, row func(team string) writes.FactRow) (func() writes.FactRow, error) {
	if projectID == "" {
		return nil, apperr.New("E_INTERNAL", "event ownership cannot be resolved: the write carries no project id")
	}
	p, err := projects.Get(ctx, projectID)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "event ownership resolution failed for project %s", projectID).WithCause(err)
	}
	if p.TeamID == "" {
		return nil, apperr.New("E_INTERNAL", "event ownership resolution returned an empty team for project %s", projectID)
	}
	team := p.TeamID
	return func() writes.FactRow { return row(team) }, nil
}

// PlatformFact 是平台级事实（TeamID=""、全员可见）的唯一显式构造器：只用于
// 无 Team 轴的平台聚合（platform 聚合、node 平台事件、模板目录等）。资源轴
// 事实必须走 FactOf——此处是"空 Team 需显式理由"纪律的语法载体（调用点
// 注释必须写明为何对全体 Team 可见）。
func PlatformFact(row func() writes.FactRow) func() writes.FactRow { return row }
