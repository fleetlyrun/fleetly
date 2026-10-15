// Package persistence 是控制面 SQLite 持久化的机械面（架构 §6）：内嵌 WAL
// 单文件 + goose 只前滚迁移、原生 SQL 不用 ORM（CAS 与四件一拍要显式
// SQL）。按聚合分包 repo（project/ app/ deployment/ …），本根包只承载
// 连接、事务边界、驱动错误判定与装配机械——禁止聚合门面（守卫见
// internal/guards）。契约面（Runner/Clock/哨兵/FormatTime，包名 state）住
// internal/domain/shared/state（ADR-0075 P1b 根包拆分：契约上移 domain，
// 机械留 infrastructure）。
//
// 四件一拍（ADR-0005）：状态 CAS + tombstone + Outbox 事件 + 审计在同
// 事务落库。事务边界由调用方（engine/API）经 Tx 收口；聚合 repo 方法一律
// 接受 state.Runner（*sql.DB 或 *sql.Tx 均满足），可独立执行也可组合进
// 事务。
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
// id 后缀）。连接池收口为 1（见 Open），事务内 MAX+1 读-改-写天然串行；
// UNIQUE(seq) 索引使违规双铸 fail-closed。table 由各 repo 以字面量传入
// （非用户输入，无注入面）。
func NextSeq(ctx context.Context, run state.Runner, table string) (int64, error) {
	var max sql.NullInt64
	if err := run.QueryRowContext(ctx, "SELECT MAX(seq) FROM "+table).Scan(&max); err != nil {
		return 0, fmt.Errorf("state: next seq %s: %w", table, err)
	}
	return max.Int64 + 1, nil
}

// DB 是打开的数据库句柄。非门面：不暴露任何聚合查询。
type DB struct {
	db    *sql.DB
	clock state.Clock
}

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
// state.Runner，聚合 repo 的事务形态据此完成 CAS + Outbox + 审计
// (+tombstone)。返回 fn 的错误时自动回滚；事务内错误原样上抛（调用方转
// apperr）。
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
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
