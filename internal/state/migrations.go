package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// 迁移体系（goose embed 模式）：SQL 迁移内嵌进二进制，进程启动（Store
// 打开）即应用，依赖零外部工具。迁移只加法——已应用文件禁止改写（内容
// sha256 由 migrations_test.go 的 golden 钉死），回滚 = 恢复快照、不写
// down migration（架构 §2.8 契约版本化纪律）。

//go:embed migrations/*.sql
var migrationsFS embed.FS

// gooseVersionTableName 是 goose 版本表名（默认值；实机验收以
// `SELECT * FROM goose_db_version` 观察已应用版本）。
const gooseVersionTableName = "goose_db_version"

// migrationFiles 返回内嵌迁移文件系统（goose provider 直接消费；
// fs.Sub 剥掉 migrations/ 目录前缀，goose 以平铺文件名解析版本号）。
func migrationFiles() (fs.FS, error) {
	return fs.Sub(migrationsFS, "migrations")
}

// ensureMigrated 在 db 上应用全部未执行的迁移，返回迁移后的当前版本号
// （空库 → 最大版本；重复调用幂等，goose 版本表记录已应用版本）。
func ensureMigrated(ctx context.Context, db *sql.DB) (int64, error) {
	fsys, err := migrationFiles()
	if err != nil {
		return 0, fmt.Errorf("state: open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, fsys)
	if err != nil {
		return 0, fmt.Errorf("state: construct migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return 0, fmt.Errorf("state: apply migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("state: read migration version: %w", err)
	}
	return version, nil
}

// migrationHashes 返回内嵌迁移的文件名 → 内容 sha256 映射（升序文件名）。
// 供只加法纪律测试使用：已应用迁移一旦被改写，哈希即失配。
func migrationHashes() (map[string]string, error) {
	fsys, err := migrationFiles()
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		f, err := fsys.Open(e.Name())
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		out[e.Name()] = hex.EncodeToString(h.Sum(nil))
	}
	return out, nil
}
