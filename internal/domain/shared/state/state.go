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

// DB 是控制面库的消费侧契约（ADR-0075 P1b 消费侧窄面）：application/
// interfaces 生产面只依赖本接口；打开/迁移/关闭等机械住在 infrastructure/
// persistence（生产实现 *persistence.DB，装配于组合根）。方法面是生产消费
// 实测清单，不臆造扩面：Runner/Clock/Tx/NextSeq/VacuumInto；Close 只被
// 组合根（wire cleanup）与离线 admin 面消费，留机械面不入契约。
type DB interface {
	// Clock 返回注入的时钟（repo 构造的时间接缝）。
	Clock() Clock
	// Runner 暴露独立执行面（聚合 repo 的非事务形态；*sql.DB 满足
	// Runner，不新增查询语义）。
	Runner() Runner
	// Tx 在单个写事务内执行 fn：四件一拍的组合点。fn 收到的 *sql.Tx 满足
	// Runner，聚合 repo 的事务形态据此完成 CAS + Outbox + 审计
	// (+tombstone)；返回 fn 的错误时自动回滚。
	Tx(ctx context.Context, fn func(tx *sql.Tx) error) error
	// NextSeq 铸表的下一创建序号（ADR-0070 单源语义：seq 是创建序唯一
	// 真源，兼任可读 id 后缀——id = <name>-<seq>；事务内 MAX+1 读-改-写，
	// UNIQUE(seq) 索引使违规双铸 fail-closed）。table 由各 repo 以字面量
	// 传入（非用户输入，无注入面）。
	NextSeq(ctx context.Context, run Runner, table string) (int64, error)
	// VacuumInto 铸在线一致快照到 target（Platform Backup 的 SQLite 面，
	// ADR-0039：VACUUM INTO 不阻塞并发写、产出独立完整库文件；target 已
	// 存在时报错，调用方先清）。
	VacuumInto(ctx context.Context, target string) error
}
