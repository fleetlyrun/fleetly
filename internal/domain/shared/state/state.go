// Package state 是持久化契约面（ADR-0075 P1b 根包拆分）：聚合 repo 的
// 执行接缝（Runner）、时间接缝（Clock）、落库时间语义（FormatTime）与
// 哨兵错误的唯一真源。纯契约零 I/O——连接、goose 前滚、驱动错误判定等
// 机械住在 internal/infrastructure/persistence 根包（包名 persistence）。
// 按聚合分包 repo 的纪律不变（架构 §6，守卫见 internal/guards）。
//
// 四件一拍（ADR-0005）：状态 CAS + tombstone + Outbox 事件 + 审计在同
// 事务落库。事务边界由调用方（engine/API）在机械侧收口；聚合 repo 方法
// 一律接受 Runner（*sql.DB 或 *sql.Tx 均满足），可独立执行也可组合进事务。
package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 哨兵错误（repo 层共用；聚合包转 apperr 的映射在 api 层收口）。
var (
	// ErrNotFound 是目标行不存在（读路径）。
	ErrNotFound = fmt.Errorf("state: not found")
	// ErrAlreadyExists 是唯一约束命中（同唯一键的活跃行已存在）。与
	// ErrConflict 分立：唯一冲突对外是 E_ALREADY_EXISTS（gRPC
	// AlreadyExists → REST 409），CAS 前置不符与 FK RESTRICT 是 E_CONFLICT
	// ——REST/SDK 消费方按 409 写的"重名重试"逻辑不得静默退化（批 0
	// 复核回归：IsUniqueViolation 归一点不变，只是分类更诚实）。
	ErrAlreadyExists = fmt.Errorf("state: already exists")
	// ErrConflict 是乐观并发冲突（CAS 前置状态不符 / FK RESTRICT 拒删）。
	ErrConflict = fmt.Errorf("state: conflict")
)

// Clock 是时间接缝（生产实现 persistence.WallClock，装配于组合根；
// hermetic 测试注入假时钟，架构 §11 自制 hermetic 夹具）。
type Clock interface {
	Now() time.Time
}

// FormatTime 规范化落库时间戳（UTC RFC3339 秒精度文本）。
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Runner 是聚合 repo 的执行面：*sql.DB 与 *sql.Tx 共同满足。方法签名带
// Runner 使同一聚合操作既可独立执行、也可组合进四件一拍事务。
type Runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
