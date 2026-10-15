package audit

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是审计聚合存取（只增）。方法集机械平移自既有 repo 导出面；Runner
// 一律由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件
// 一拍事务，ADR-0005）。
type Repo interface {
	// Append 落一条审计（四件一拍的"审计"件）。Team 轴统一铸入点：Entry 未
	// 显式携带时从 ctx 取（ADR-0035——全部调用方（api/engine/webhook）零改动
	// 获得 Team 标注；system/无身份动作显式留空 = 平台级行）。ID 由行装配处
	// 装配 aud-<seq>（ADR-0070——调用方不再自带 id）。
	Append(ctx context.Context, run state.Runner, e *Entry) error
	// List 返回审计（新→旧；limit 上界钳制）。
	List(ctx context.Context, run state.Runner, limit int) ([]Entry, error)
	// ListFiltered 按过滤返回审计（新→旧；limit 上界钳制；条件动态拼接但
	// 占位符参数化——无字符串拼接 SQL）。
	ListFiltered(ctx context.Context, run state.Runner, f Filter) ([]Entry, error)
}
