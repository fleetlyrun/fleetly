// 升级前进测试（P5-2）：goose 只前滚的换装演练锚——era 库（旧版本号
// 停留）携存量行无损前滚到最新。主锚 = staging 换装 dress rehearsal：
// staging 库经 ADR-0070 重置到 v28 基线再前滚 v29-v32；本测试在 temp 库
// 复刻该序列，断言 era 行存活 + 新默认列回填语义 + 中途版本表在场。
// 住 migrations/ 子包（守卫 statefacade 的根级聚合表引用豁免面——era 形
// 种子必须裸 SQL 落表名；embed 与 state.go 各自独立声明，互不相扰）。
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// 驱动注册（根包 persistence 不在 import 链上，独立测试包自带）。
	_ "modernc.org/sqlite"
)

//go:embed *.sql
var forwardFS embed.FS

// latestMigrationVersion 是迁移链最新版本号（00034_idempotency_caller_scope.sql）；
// 链前进即改这里（golden 同批纪律：新迁移带锚更新）。
const latestMigrationVersion = 34

// openMigrationDB 打开 temp 目录裸 SQLite（纯迁移面，不带 Open 的
// pragmas——本文件只验 goose 前滚语义；单写者收口与 Open 同纪律），
// goose 对准本包 embed 的迁移目录。t.Cleanup 收口连接。
func openMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "migrate-forward.db")))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	goose.SetBaseFS(forwardFS)
	require.NoError(t, goose.SetDialect(string(goose.DialectSQLite3)))
	return db
}

// seedEraRows 按 v28 列形（00001_core.sql 抄录）各插一行存量行：
// revisions 无 source_text（v31 才加）、outbox 无 team_id（v32 才加）；
// NOT NULL 无默认列给 era 合法值，id/seq 用字面量（NextSeq 语义不属
// 迁移面）。返回 outbox 行的 seq（AUTOINCREMENT 铸号，主键查回锚）。
func seedEraRows(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO revisions (id, app_id, seq, digest, spec, created_at)
		VALUES ('rev-era28-0001', 'app-era28-0001', 1, 'sha256:era28-seed', ?, '2026-10-11T00:00:00Z')`,
		[]byte(`{}`))
	require.NoError(t, err)
	res, err := db.ExecContext(ctx, `INSERT INTO outbox (name, aggregate, aggregate_id, payload, created_at)
		VALUES ('deployment.queued', 'deployment', 'dep-era28-0001', '{}', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	seq, err := res.LastInsertId()
	require.NoError(t, err)
	return seq
}

// TestMigrateForwardFromEra28ToLatest 是 staging 换装 dress rehearsal 的
// 库形态锚：v28 基线（era 种子行在场）前滚到最新，era 行无损、新列
// 按迁移默认回填、中途版本的两张表在场。
func TestMigrateForwardFromEra28ToLatest(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	// v28 基线（ADR-0070 重置后的现役形态）：停留 28。
	require.NoError(t, goose.UpToContext(ctx, db, ".", 28))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(28), version)

	outboxSeq := seedEraRows(t, db)

	// 前滚到最新（换装 = 新版二进制启动自动前滚，同 migrate() 语义）。
	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err = goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// era 行存活（主键查回）+ v31 默认回填：source_text = ''（存量冻结
	// 只有 IR、无原文——诚实边界）。
	var digest, sourceText string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT digest, source_text FROM revisions WHERE id = 'rev-era28-0001'`).
		Scan(&digest, &sourceText))
	assert.Equal(t, "sha256:era28-seed", digest)
	assert.Empty(t, sourceText, "v31 must backfill source_text with ''")

	// era outbox 行存活（AUTOINCREMENT seq 主键查回）+ v32 默认回填：
	// team_id = ''（平台级可见，不回填不编造）。
	var teamID string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT team_id FROM outbox WHERE seq = ?`, outboxSeq).
		Scan(&teamID))
	assert.Empty(t, teamID, "v32 must backfill team_id with ''")

	// 中途版本两表在场（v29 app_variables / v30 github_apps）：
	// COUNT(*) 可查即算在场。
	for _, table := range []string{"app_variables", "github_apps"} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n),
			"table %s must exist after rollforward", table)
	}
}

// TestMigrateForwardIdempotencyCallerScope：00034 重建表的存量处置锚——
// v33 era 幂等行（无 scope 列）前滚后落空作用域（scope = 空串，无调用方
// 可达的隔离区，仅等 Sweep）；新键空间 (scope, idem_key) 允许同键异作用
// 域并存；临时表已改名、过期索引在新表上重建（H11 / R14-H4）。
func TestMigrateForwardIdempotencyCallerScope(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 33))
	_, err := db.ExecContext(ctx, `INSERT INTO idempotency_records
		(idem_key, method, fingerprint, state, response_type, response_body, created_at, expires_at)
		VALUES ('legacy-key', '/fleetly.structure.v1.ProjectsService/CreateProject', 'fp-legacy',
		        'completed', 'type.googleapis.com/fleetly.structure.v1.CreateProjectResponse', ?,
		        '2026-10-11T00:00:00Z', '2026-10-12T00:00:00Z')`, []byte(`{"msg":"legacy"}`))
	require.NoError(t, err)

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(latestMigrationVersion), version)

	// 存量行完整保留但落不可达作用域 ''。
	var scope, method, responseType string
	var body []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT scope, method, response_type, response_body
		FROM idempotency_records WHERE idem_key = 'legacy-key'`).
		Scan(&scope, &method, &responseType, &body))
	assert.Empty(t, scope, "legacy rows must land in the unreachable empty scope")
	assert.Equal(t, "/fleetly.structure.v1.ProjectsService/CreateProject", method)
	assert.Equal(t, "type.googleapis.com/fleetly.structure.v1.CreateProjectResponse", responseType)
	assert.Equal(t, []byte(`{"msg":"legacy"}`), body, "legacy response body must survive the rebuild")

	// 新键空间：同键不同作用域可并存（旧全局主键已拆为复合主键）。
	for _, scope := range []string{"token:tok-a", "token:tok-b"} {
		_, err = db.ExecContext(ctx, `INSERT INTO idempotency_records
			(scope, idem_key, method, fingerprint, state, created_at, expires_at)
			VALUES (?, 'legacy-key', 'POST', 'fp', 'inflight', '2026-10-11T00:00:00Z', '2026-10-11T00:01:30Z')`, scope)
		require.NoError(t, err)
	}

	// 临时表不残留、过期索引在新表名下重建。
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'idempotency_records_v2'`).Scan(&n))
	assert.Zero(t, n, "the rebuild staging table must not survive the rename")
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_idempotency_expires' AND tbl_name = 'idempotency_records'`).Scan(&n))
	assert.Equal(t, 1, n, "the expiry index must be rebuilt on the renamed table")
}

// TestMigrateForwardFromRecentCheckpoints 对每个近期检查点验证「该检查
// 点之后的库 = 在役库形态」：fresh 库钉在检查点后一滚到顶（真实升级
// 路径是逐版前滚，检查点代表中途接管的库）。
func TestMigrateForwardFromRecentCheckpoints(t *testing.T) {
	for _, cp := range []int64{30, 31, 32, 33} {
		t.Run(fmt.Sprintf("from_v%d", cp), func(t *testing.T) {
			ctx := context.Background()
			db := openMigrationDB(t)

			require.NoError(t, goose.UpToContext(ctx, db, ".", cp))
			version, err := goose.GetDBVersion(db)
			require.NoError(t, err)
			require.Equal(t, cp, version)

			require.NoError(t, goose.UpContext(ctx, db, "."))
			version, err = goose.GetDBVersion(db)
			require.NoError(t, err)
			assert.Equal(t, int64(latestMigrationVersion), version)
		})
	}
}
