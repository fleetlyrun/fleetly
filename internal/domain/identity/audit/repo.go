package audit

import (
	"context"
)

// Repo 是审计聚合存取（只增）。方法集机械平移自既有 repo 导出面。
// 执行面构造绑定（ADR-0080）：组合根以 db 独立执行形态构造本 repo；事务态由 transact.Binder 以事务执行面重造。
type Repo interface {
	// Append 落一条审计（四件一拍的"审计"件）。Team 轴统一铸入点：Entry 未
	// 显式携带时从 ctx 取（ADR-0035——全部调用方（api/engine/webhook）零改动
	// 获得 Team 标注；system/无身份动作显式留空 = 平台级行）。ID 由行装配处
	// 装配 aud-<seq>（ADR-0070——调用方不再自带 id）。
	Append(ctx context.Context, e *Entry) error
	// List 返回审计（新→旧；limit 上界钳制）。
	List(ctx context.Context, limit int) ([]Entry, error)
	// ListFiltered 按过滤返回审计（新→旧；limit 上界钳制；条件动态拼接但
	// 占位符参数化——无字符串拼接 SQL）。
	ListFiltered(ctx context.Context, f Filter) ([]Entry, error)
}
