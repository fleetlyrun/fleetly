// Package state 是持久化契约面（ADR-0075 P1b 根包拆分）：聚合 repo 的
// 执行接缝（Runner）、时间接缝（Clock）、落库时间语义（FormatTime）与
// 哨兵错误的唯一真源。纯契约零 I/O——连接、goose 前滚、驱动错误判定等
// 机械住在 internal/infrastructure/persistence 根包（包名 persistence）。
// 按聚合分包 repo 的纪律不变（架构 §6，守卫见 internal/guards）。
//
// 四件一拍（ADR-0005）：状态 CAS + tombstone + Outbox 事件 + 审计在同
// 事务落库。事务边界由调用方（engine/API）在机械侧收口；聚合 repo 构造
// 绑定执行面（组合根绑 *sql.DB 独立形态），事务态经 application/transact
// 的 Binder 以 *sql.Tx 重造（显式 bundle UoW，ADR-0080）——聚合 repo
// 签名零执行器词汇。
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

// Runner 是执行接缝：*sql.DB 与 *sql.Tx 共同满足。聚合 repo 构造绑定
// 执行面（独立形态绑 *sql.DB），事务态经 transact.Binder 以 *sql.Tx
// 重造（ADR-0080）；本类型只被 persistence 实现与 transact 装配引用，
// 不出现在任何聚合 repo 签名。
type Runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB 是控制面库的消费侧契约（ADR-0075 P1b 消费侧窄面）：application/
// interfaces 生产面只依赖本接口；打开/迁移/关闭等机械住在 infrastructure/
// persistence（生产实现 *persistence.DB，装配于组合根）。方法面是生产消费
// 实测清单，不臆造扩面：Runner/Clock/Tx/VacuumInto；Close 只被
// 组合根（wire cleanup）与离线 admin 面消费，留机械面不入契约。
type DB interface {
	// Clock 返回注入的时钟（repo 构造的时间接缝）。
	Clock() Clock
	// Runner 暴露独立执行面（组合根构造 repo 独立形态用；*sql.DB 满足
	// Runner，不新增查询语义）。
	Runner() Runner
	// Tx 在单个写事务内执行 fn：四件一拍的组合点。fn 收到的 *sql.Tx 满足
	// Runner，事务作用域 Stores（transact.Binder）据此重造各聚合 repo 的
	// 事务形态，完成 CAS + Outbox + 审计 (+tombstone)；返回 fn 的错误时
	// 自动回滚。
	Tx(ctx context.Context, fn func(run Runner) error) error
	// VacuumInto 铸在线一致快照到 target（Platform Backup 的 SQLite 面，
	// ADR-0039：VACUUM INTO 不阻塞并发写、产出独立完整库文件；target 已
	// 存在时报错，调用方先清）。
	VacuumInto(ctx context.Context, target string) error
}
