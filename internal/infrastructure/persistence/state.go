// Package persistence 是控制面 SQLite 持久化的机械面（架构 §6）：内嵌 WAL
// 单文件 + goose 只前滚迁移、原生 SQL 不用 ORM（CAS 与四件一拍要显式
// SQL）。按聚合分包 repo（project/ app/ deployment/ …），本根包只承载
// 连接、事务边界、驱动错误判定与装配机械——禁止聚合门面（守卫见
// internal/guards）。契约面（Runner/Clock/哨兵/FormatTime，包名 state）住
// internal/domain/shared/state（ADR-0075 P1b 根包拆分：契约上移 domain，
// 机械留 infrastructure）。
//
// 四件一拍（ADR-0005）：状态 CAS + tombstone + Outbox 事件 + 审计在同
// 事务落库。事务边界由调用方（engine/API）经 Tx 收口；聚合 repo 构造
// 绑定执行面（New(clock, run)——组合根绑 *sql.DB 独立形态），事务态由
// application/transact 的 Binder 以事务执行面重造（ADR-0080）。
package persistence

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // 驱动注册（纯 Go 零 cgo，交叉编译/容器形态友好）

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type wallClock struct{}

// Now 返回 UTC 墙钟（ADR-0018：审计与 Event 一律 UTC）。
func (wallClock) Now() time.Time { return time.Now().UTC() }

// WallClock 返回生产时钟。
func WallClock() state.Clock { return wallClock{} }

// IsUniqueViolation 报告 sqlite 唯一约束命中（modernc 驱动以
// SQLITE_CONSTRAINT 文本承载约束错误，类型面不完整；聚合 repo 据此把
// 唯一冲突归一为 state.ErrAlreadyExists）。
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed: UNIQUE")
}

// IsForeignKeyViolation 报告 sqlite 外键约束命中（RESTRICT 拒删等形态；
// 聚合 repo 据此把"仍被引用"归一为 state.ErrConflict——提示先解绑）。
func IsForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed: FOREIGN KEY")
}

// MapScanErr 把 *sql.Row.Scan 的 ErrNoRows 归一为 state.ErrNotFound（聚合
// repo 共用）。
func MapScanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return state.ErrNotFound
	}
	return err
}

// NextSeq 铸表的下一创建序号（ADR-0070：seq 是创建序唯一真源，兼任可读
// id 后缀）。序号来自 seq_counters 单调计数器（00043）：next 存“下一可
// 分配序号”，UPDATE ... RETURNING next - 1 单语句完成读-改-写——事务内
// 与事务外（*sql.DB 自动提交）都是原子面；计数器只增，硬删最大行后序号
// 不回收（never-reuse），唯一冲突/失败插入也会留下空洞——序号是创建序，
// 不承诺连续。UNIQUE(seq) 索引仍是违规双铸的 fail-closed 兜底。
//
// table 由各 repo 以字面量传入（非用户输入）；name 走占位符参数，无 SQL
// 注入面。计数器行缺失（新表/漏回填）时插入 next=2 并返回 1；与另一写者
// 的插入竞争按唯一冲突重试一次。包内聚合 repo 共用本包函数（事务态传
// 绑定的 tx 执行面；transact.Stores.NextSeq 是消费侧唯一入口）。
func NextSeq(ctx context.Context, run state.Runner, table string) (int64, error) {
	if table == "" {
		return 0, errors.New("state: next seq: empty table name")
	}
	var seq int64
	for attempt := 0; attempt < 2; attempt++ {
		// 常规径：计数器已回填（迁移保证）——单语句原子自增。
		err := run.QueryRowContext(ctx,
			`UPDATE seq_counters SET next = next + 1 WHERE name = ? RETURNING next - 1`, table).Scan(&seq)
		if err == nil {
			return seq, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("state: next seq %s: %w", table, err)
		}
		// 缺失径：新表或漏回填——插入计数器（首号 = 1，next 预置 2）。
		err = run.QueryRowContext(ctx,
			`INSERT INTO seq_counters (name, next) VALUES (?, 2) RETURNING next - 1`, table).Scan(&seq)
		if err == nil {
			return seq, nil
		}
		if !IsUniqueViolation(err) {
			return 0, fmt.Errorf("state: next seq %s: %w", table, err)
		}
		// 并发写者刚插入同表计数器：回到自增径重取。
	}
	return 0, fmt.Errorf("state: next seq %s: counter insert raced", table)
}

// DB 是打开的数据库句柄。非门面：不暴露任何聚合查询。实现 state.DB 消费
// 侧契约（ADR-0075 P1b：application/interfaces 生产面经接口消费，本类型
// 只在组合根与离线 admin 面以具体形态出现）。
type DB struct {
	db    *sql.DB
	clock state.Clock
}

// 编译期断言：*DB 满足 state.DB 契约面（方法集漂移即编译红）。
var _ state.DB = (*DB)(nil)

// Open 打开（或创建）path 处的 SQLite 库：WAL + busy timeout + 外键 +
// NORMAL 同步（WAL 推荐档），随后执行 goose 前滚迁移。
func Open(ctx context.Context, path string, clock state.Clock) (*DB, error) {
	if clock == nil {
		clock = WallClock()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("state: create data dir: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("state: open sqlite: %w", err)
	}
	// SQLite 单写者本性：连接池收口为 1，消灭并发写互踩（database is
	// locked 内讧；N0 单节点小团队规模吞吐足够，读池分离待实测瓶颈再议）。
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: ping sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db: db, clock: clock}, nil
}

// Close 关闭底层连接池。
func (d *DB) Close() error { return d.db.Close() }

// Clock 返回注入的时钟。
func (d *DB) Clock() state.Clock { return d.clock }

// Runner 暴露底层执行面（供聚合 repo 独立执行形态使用；*sql.DB 满足
// state.Runner，不新增查询语义）。
func (d *DB) Runner() state.Runner { return d.db }

// VacuumInto 铸在线一致快照到 target（Platform Backup 的 SQLite 面，
// ADR-0039：VACUUM INTO 不阻塞并发写、产出独立完整库文件；live 库三件
// 排除在备份集外，恢复面以快照为准）。target 已存在时报错（调用方先清）。
func (d *DB) VacuumInto(ctx context.Context, target string) error {
	if _, err := d.db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return fmt.Errorf("state: vacuum into %s: %w", target, err)
	}
	return nil
}

// migrate 执行 goose 前滚（只加法；失败=恢复 Platform Backup 重放，
// 架构 §8）。
func migrate(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect(string(goose.DialectSQLite3)); err != nil {
		return fmt.Errorf("state: goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("state: goose up: %w", err)
	}
	return nil
}

// Tx 在单个写事务内执行 fn：四件一拍的组合点。fn 收到的 *sql.Tx 满足
// state.Runner，事务作用域 Stores（transact.Binder）据此重造各聚合 repo
// 的事务形态，完成 CAS + Outbox + 审计 (+tombstone)。返回 fn 的错误时
// 自动回滚；事务内错误原样上抛（调用方转 apperr）。
func (d *DB) Tx(ctx context.Context, fn func(run state.Runner) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("state: rollback after %v (rollback: %w)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state: commit: %w", err)
	}
	return nil
}
