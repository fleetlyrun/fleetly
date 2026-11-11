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

// latestMigrationVersion 是迁移链最新版本号（00047_core_graph_foreign_keys.sql，
// 核心资源图外键）；链前进即改这里（golden 同批纪律：新迁移带锚更新）。
const latestMigrationVersion = 47

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
	// v47 起 revisions.app_id 有外键：父链 teams → projects → apps 先落行
	//（v28 列形；迁移前的库允许孤儿，但换装后必须完整）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 1, 'default', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, seq, name, team_id, created_at, updated_at)
		VALUES ('project-era28-0001', 1, 'era28', 'default', '2026-10-11T00:00:00Z', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO apps (id, seq, project_id, name, created_at, updated_at)
		VALUES ('app-era28-0001', 1, 'project-era28-0001', 'web', '2026-10-11T00:00:00Z', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO revisions (id, app_id, seq, digest, spec, created_at)
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

	// v35 节点个体 relay 凭证表在场且无回填：凭证只在“节点已锚定”后由
	// 定向动作铸造，迁移不为存量节点编造材料（表空起步）。
	var relayRows int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_relay_credentials`).Scan(&relayRows),
		"node_relay_credentials must exist after rollforward")
	assert.Zero(t, relayRows, "v35 must not backfill relay credentials for existing nodes")
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

// TestMigrateForwardNodeRelayCredentials：00035 的换装锚——v34 库无凭证表，
// 前滚后新表在场、零回填（凭证只在节点锚定后由定向动作为单节点铸造），
// 存量 nodes 观测行无损（nodes 是观测缓存，不与凭证同表）。
func TestMigrateForwardNodeRelayCredentials(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 34))
	_, err := db.ExecContext(ctx, `INSERT INTO nodes
		(platform_id, seq, carrier_id, hostname, role, available, first_seen_at, last_seen_at)
		VALUES ('node-era34', 1, 'carrier-era34', 'era-host', 'worker', 1,
		        '2026-10-11T00:00:00Z', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'node_relay_credentials'`).Scan(&n))
	assert.Zero(t, n, "v34 must not carry the relay credential table")

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(latestMigrationVersion), version)

	// 新表在场但空——迁移无 backfill（不做存量节点的凭证编造）。
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM node_relay_credentials`).Scan(&n))
	assert.Zero(t, n, "v35 must start empty (no backfill)")

	// 存量节点行无损（v35 不触碰 nodes）。
	var carrier, role string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT carrier_id, role FROM nodes WHERE platform_id = 'node-era34'`).Scan(&carrier, &role))
	assert.Equal(t, "carrier-era34", carrier)
	assert.Equal(t, "worker", role)
}

// TestMigrateForwardTokenExpiry：00036 的换装锚——v35 库的 tokens 无
// expires_at，前滚后列在场且存量行默认 ”（永不过期；Login 会话 Token 才
// 带过期时刻，存量 bootstrap/CI Token 语义不变）。
func TestMigrateForwardTokenExpiry(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 35))
	// era 行按 v35 列形插入（无 expires_at）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('team-era35', 1, 'era', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES ('role-era35', 1, '', 'era-role', 0, '[]', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO tokens
		(id, seq, name, team_id, user_id, role_id, sha256, prefix, revoked, last_used_at, created_at)
		VALUES ('tok-era35', 1, 'era-token', 'team-era35', NULL, 'role-era35',
		        'sha-era35', 'flt_era', 0, '', '2026-10-11T00:00:00Z')`)
	require.NoError(t, err)

	var hasColumn int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('tokens') WHERE name = 'expires_at'`).Scan(&hasColumn))
	assert.Zero(t, hasColumn, "v35 must not carry tokens.expires_at")

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(latestMigrationVersion), version)

	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('tokens') WHERE name = 'expires_at'`).Scan(&hasColumn))
	assert.Equal(t, 1, hasColumn, "v36 must add tokens.expires_at")

	// 存量行无损且默认 ''（永不过期——迁移不编造过期时刻）。
	var name, expiresAt string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT name, expires_at FROM tokens WHERE id = 'tok-era35'`).Scan(&name, &expiresAt))
	assert.Equal(t, "era-token", name)
	assert.Empty(t, expiresAt, "v36 must backfill expires_at with '' (never expires)")
}

// TestMigrateForwardDiagnosisFacts：00037/00038 的换装锚（B2a W1b facts
// 地基）——v36 库无 outbox 聚合索引与 alert_rules.pending_since 列，前滚
// 后两者在场；存量 alert_rules 行回填 pending_since = ”（升级时无在途
// pending 窗可编造，重启续窗语义自前滚后的新起窗开始）。
func TestMigrateForwardDiagnosisFacts(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 36))
	// v47 起 alert_rules.app_id 有外键：父链先落行（v36 列形）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 1, 'default', '2026-10-30T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, seq, name, team_id, created_at, updated_at)
		VALUES ('project-era36', 1, 'era36', 'default', '2026-10-30T00:00:00Z', '2026-10-30T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO apps (id, seq, project_id, name, created_at, updated_at)
		VALUES ('app-era36', 1, 'project-era36', 'web', '2026-10-30T00:00:00Z', '2026-10-30T00:00:00Z')`)
	require.NoError(t, err)
	// era 规则行按 v36 列形插入（无 pending_since）。
	_, err = db.ExecContext(ctx, `INSERT INTO alert_rules
		(id, seq, app_id, metric, threshold, for_secs, enabled, state, state_since,
		 last_value, last_observed_at, created_at, updated_at)
		VALUES ('rule-era36', 1, 'app-era36', 'cpu_percent', 80, 30, 1, 'ok',
		        '2026-10-30T00:00:00Z', 0, '', '2026-10-30T00:00:00Z', '2026-10-30T00:00:00Z')`)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_outbox_aggregate'`).Scan(&n))
	assert.Zero(t, n, "v36 must not carry the outbox aggregate index")
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('alert_rules') WHERE name = 'pending_since'`).Scan(&n))
	assert.Zero(t, n, "v36 must not carry alert_rules.pending_since")

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// v37 索引在场（挂在 outbox 表上）。
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_outbox_aggregate'
		 AND tbl_name = 'outbox'`).Scan(&n))
	assert.Equal(t, 1, n, "v37 must add the outbox aggregate index")

	// v38 列在场且存量行回填 ''。
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('alert_rules') WHERE name = 'pending_since'`).Scan(&n))
	assert.Equal(t, 1, n, "v38 must add alert_rules.pending_since")
	var pending string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT pending_since FROM alert_rules WHERE id = 'rule-era36'`).Scan(&pending))
	assert.Empty(t, pending, "v38 must backfill pending_since with '' (no in-flight window survives an upgrade)")
}

// TestMigrateForwardSeqCounters：00043 的换装锚——v42 库无 seq_counters
// 与审计 Team 游标索引，前滚后两者在场；计数器按各表 COALESCE(MAX(seq),0)+1
// 回填（存量行之后继续，空表从 1 起），审计索引挂在 (team_id, seq)。
func TestMigrateForwardSeqCounters(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 42))
	// era 行：teams 与 projects 各一行（v42 列形；seq 用非 1 值验证回填
	// 取最大序号而非行数）。v47 起 projects.team_id 有外键：default 团队
	// 必须先在场（projects 行缺省 team_id='default'）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 1, 'default', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('team-era42', 7, 'era42', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, seq, name, created_at, updated_at)
		VALUES ('project-era42', 3, 'era42', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'seq_counters'`).Scan(&n))
	assert.Zero(t, n, "v42 must not carry seq_counters")
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_audit_team_seq'`).Scan(&n))
	assert.Zero(t, n, "v42 must not carry the audit team cursor index")

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// 审计游标索引在场。
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_audit_team_seq'
		 AND tbl_name = 'audit'`).Scan(&n))
	assert.Equal(t, 1, n, "v43 must add the audit (team_id, seq) index")

	// 计数器回填：存量表 = MAX(seq)+1，空表 = 1。
	for name, want := range map[string]int64{"teams": 8, "projects": 4, "audit": 1, "apps": 1} {
		var next int64
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT next FROM seq_counters WHERE name = ?`, name).Scan(&next))
		assert.Equal(t, want, next, "seq_counters.%s must resume after the largest existing seq", name)
	}

	// 每张带全局 seq 的表都有计数器行（36 张；revisions per-App 与
	// outbox AUTOINCREMENT 不在列）。
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seq_counters`).Scan(&n))
	assert.Equal(t, 36, n, "every table with a global seq column must be backfilled")
}

// TestMigrateForwardBackupSubject：00044 的换装锚——v43 库的 backups 行
// 携 database_id 列；前滚后列改名 subject_id、kind 按引擎回填
// （nats/kafka/redpanda → pubsub，其余 database）、索引改名重建。
func TestMigrateForwardBackupSubject(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 43))
	// v47 起 backups.project_id 有外键：父链先落行（v43 列形）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 1, 'default', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, seq, name, team_id, created_at, updated_at)
		VALUES ('project-era43', 1, 'era43', 'default', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	// era 行按 v43 列形插入（database_id；无 kind）。
	for i, row := range []struct {
		id, engine string
	}{
		{"bk-era43-pg", "postgres"},
		{"bk-era43-nats", "nats"},
		{"bk-era43-kafka", "kafka"},
		{"bk-era43-rp", "redpanda"},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO backups
			(id, seq, project_id, database_id, engine, created_at)
			VALUES (?, ?, 'project-era43', ?, ?, '2026-11-10T00:00:00Z')`,
			row.id, i+1, "subject-"+row.id, row.engine)
		require.NoError(t, err)
	}

	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('backups') WHERE name = 'kind'`).Scan(&n))
	assert.Zero(t, n, "v43 must not carry backups.kind")

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// 列改名 + 回填：存量行无损，kind 按引擎判据回填。
	for id, wantKind := range map[string]string{
		"bk-era43-pg": "database", "bk-era43-nats": "pubsub",
		"bk-era43-kafka": "pubsub", "bk-era43-rp": "pubsub",
	} {
		var subjectID, kind string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT subject_id, kind FROM backups WHERE id = ?`, id).Scan(&subjectID, &kind))
		assert.Equal(t, "subject-"+id, subjectID)
		assert.Equal(t, wantKind, kind, "kind backfill for %s", id)
	}

	// 索引改名重建（旧索引不残留）。
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_backups_subject'
		 AND tbl_name = 'backups'`).Scan(&n))
	assert.Equal(t, 1, n, "v44 must rebuild the subject index")
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_backups_database'`).Scan(&n))
	assert.Zero(t, n, "v44 must drop the old database index")
}

// TestMigrateForwardRestoreState：00045 的换装锚——v44 库的 databases/
// pubsubs 无 restore_state 列；前滚后列在场，存量 restore_from_backup 非空
// 行回填 pending（旧语义的非空即挂起），空行保持 ”。
func TestMigrateForwardRestoreState(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 44))
	// v47 起 databases/pubsubs.project_id 有外键：父链先落行（v44 列形）。
	_, err := db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 1, 'default', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO projects (id, seq, name, team_id, created_at, updated_at)
		VALUES ('project-era44', 1, 'era44', 'default', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	// era 行按 v44 列形插入（无 restore_state）：一行带恢复源、一行不带。
	_, err = db.ExecContext(ctx, `INSERT INTO databases
		(id, seq, project_id, name, engine, version, credentials_ref,
		 restore_from_backup, created_at, updated_at)
		VALUES ('db-era44-restore', 1, 'project-era44', 'restore-me', 'postgres', '18', 'database:restore-me',
		        'bk-era44', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO databases
		(id, seq, project_id, name, engine, version, credentials_ref,
		 created_at, updated_at)
		VALUES ('db-era44-clean', 2, 'project-era44', 'clean', 'postgres', '18', 'database:clean',
		        '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO pubsubs
		(id, seq, project_id, name, engine, version, credentials_ref,
		 restore_from_backup, created_at, updated_at)
		VALUES ('ps-era44-restore', 1, 'project-era44', 'bus', 'nats', '2.11', 'pubsub:bus',
		        'bk-era44', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z')`)
	require.NoError(t, err)

	var n int
	for _, table := range []string{"databases", "pubsubs"} {
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name = 'restore_state'`).Scan(&n))
		assert.Zero(t, n, "v44 must not carry %s.restore_state", table)
	}

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// 回填：源在场 → pending；无源 → ''。
	for _, tc := range []struct {
		table, id, want string
	}{
		{"databases", "db-era44-restore", "pending"},
		{"databases", "db-era44-clean", ""},
		{"pubsubs", "ps-era44-restore", "pending"},
	} {
		var state string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT restore_state FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&state))
		assert.Equal(t, tc.want, state, "%s.%s restore_state backfill", tc.table, tc.id)
	}
}

// TestMigrateForwardSoftDeleteNotNull：00046 的换装锚——v45 库的七张
// 早期表 deleted_at 可空；前滚后统一为 NOT NULL 且默认空串，NULL 存量行
// 归一为空串（旧代码未软删的活跃行恢复可见），tombstone 原值保留；全部
// 在役索引按当前形态重建（00010/00016 的历史改名不复活），app_hooks →
// apps 外键在父表重建后仍有效且 foreign_keys 开关恢复 ON。
func TestMigrateForwardSoftDeleteNotNull(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	// 生产 Open 带 foreign_keys(ON)：本测试同口径，验证 app_hooks 子行
	// 在场时 apps 重建仍安全（迁移内部临时关闭外键）。
	_, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 45))

	// v47 起 projects.team_id 有外键：default 团队先落行（v45 列形）。
	_, err = db.ExecContext(ctx, `INSERT INTO teams (id, seq, name, created_at)
		VALUES ('default', 900, 'default', '2026-11-11T00:00:00Z')`)
	require.NoError(t, err)

	legacyTables := []string{"projects", "apps", "routes", "databases", "secrets", "volumes", "networks"}
	for _, table := range legacyTables {
		var notNull int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT "notnull" FROM pragma_table_info(?) WHERE name = 'deleted_at'`, table).
			Scan(&notNull), "inspect %s.deleted_at", table)
		assert.Zero(t, notNull, "v45 %s.deleted_at must still be nullable", table)
	}

	// 每表两行：NULL（活跃，归一后 ''）与非空 tombstone（原值保留）。
	for _, seed := range []struct{ table, stmt string }{
		{"projects", `INSERT INTO projects (id, seq, name, team_id, created_at, updated_at, deleted_at) VALUES
			('project-era46-null', 101, 'null-project', 'default', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('project-era46-tomb', 102, 'tomb-project', 'default', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"apps", `INSERT INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at) VALUES
			('app-era46-null', 201, 'project-era46-null', 'null-app', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('app-era46-tomb', 202, 'project-era46-null', 'tomb-app', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"routes", `INSERT INTO routes
			(id, seq, project_id, host, path, app_id, process, port, protocol, tls_mode, created_at, updated_at, deleted_at) VALUES
			('route-era46-null', 301, 'project-era46-null', 'null.example.test', '', 'app-era46-null', 'web', 8080, 'http', 'auto', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('route-era46-tomb', 302, 'project-era46-null', 'tomb.example.test', '/', 'app-era46-null', 'web', 8080, 'http', 'auto', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"databases", `INSERT INTO databases
			(id, seq, project_id, name, engine, version, credentials_ref, created_at, updated_at, deleted_at) VALUES
			('db-era46-null', 401, 'project-era46-null', 'null-db', 'postgres', '18', 'database:null-db', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('db-era46-tomb', 402, 'project-era46-null', 'tomb-db', 'postgres', '18', 'database:tomb-db', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"secrets", `INSERT INTO secrets (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at) VALUES
			('secret-era46-null', 501, 'project-era46-null', 'null-secret', X'00', 'fp-null', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('secret-era46-tomb', 502, 'project-era46-null', 'tomb-secret', X'00', 'fp-tomb', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"volumes", `INSERT INTO volumes (id, seq, project_id, name, pinned_node_id, created_at, updated_at, deleted_at) VALUES
			('volume-era46-null', 601, 'project-era46-null', 'null-volume', '', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', NULL),
			('volume-era46-tomb', 602, 'project-era46-null', 'tomb-volume', '', '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
		{"networks", `INSERT INTO networks (id, seq, project_id, name, egress_none, created_at, deleted_at) VALUES
			('network-era46-null', 701, 'project-era46-null', 'null-network', 0, '2026-11-11T00:00:00Z', NULL),
			('network-era46-tomb', 702, 'project-era46-null', 'tomb-network', 0, '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`},
	} {
		_, err := db.ExecContext(ctx, seed.stmt)
		require.NoError(t, err, "seed %s", seed.table)
	}

	// app_hooks 子行：apps 重建期间必须存活，且外键仍指向重建后的行。
	_, err = db.ExecContext(ctx, `INSERT INTO app_hooks
		(app_id, repo, token_sha256, secret_ciphertext, created_at, updated_at)
		VALUES ('app-era46-null', 'fleetly/example', 'sha256:era46', X'00',
		        '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z')`)
	require.NoError(t, err)

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	assert.Equal(t, int64(latestMigrationVersion), version)

	// 列形态：七表 deleted_at 均 NOT NULL DEFAULT ''。
	for _, table := range legacyTables {
		var notNull int
		var dflt sql.NullString
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT "notnull", dflt_value FROM pragma_table_info(?) WHERE name = 'deleted_at'`, table).
			Scan(&notNull, &dflt), "inspect %s.deleted_at", table)
		assert.Equal(t, 1, notNull, "%s.deleted_at must be NOT NULL", table)
		assert.Equal(t, "''", dflt.String, "%s.deleted_at must default to ''", table)
	}

	// 数据：NULL 归一为 ''，tombstone 原值保留。
	for _, tc := range []struct{ table, id, want string }{
		{"projects", "project-era46-null", ""},
		{"projects", "project-era46-tomb", "2026-11-10T00:00:00Z"},
		{"apps", "app-era46-null", ""},
		{"apps", "app-era46-tomb", "2026-11-10T00:00:00Z"},
		{"routes", "route-era46-null", ""},
		{"routes", "route-era46-tomb", "2026-11-10T00:00:00Z"},
		{"databases", "db-era46-null", ""},
		{"databases", "db-era46-tomb", "2026-11-10T00:00:00Z"},
		{"secrets", "secret-era46-null", ""},
		{"secrets", "secret-era46-tomb", "2026-11-10T00:00:00Z"},
		{"volumes", "volume-era46-null", ""},
		{"volumes", "volume-era46-tomb", "2026-11-10T00:00:00Z"},
		{"networks", "network-era46-null", ""},
		{"networks", "network-era46-tomb", "2026-11-10T00:00:00Z"},
	} {
		var got string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT deleted_at FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&got),
			"%s.%s must survive the rebuild", tc.table, tc.id)
		assert.Equal(t, tc.want, got, "%s.%s deleted_at", tc.table, tc.id)
	}

	// 索引：在役名字/归属/partial 谓词全部存活。
	for _, idx := range []struct {
		name, table string
		partial     bool
	}{
		{"idx_projects_seq", "projects", false},
		{"idx_projects_team_name", "projects", true},
		{"idx_apps_seq", "apps", false},
		{"idx_apps_project_name", "apps", true},
		{"idx_routes_seq", "routes", false},
		{"idx_routes_host_path", "routes", true},
		{"idx_routes_app", "routes", false},
		{"idx_databases_seq", "databases", false},
		{"idx_databases_project_name", "databases", true},
		{"idx_secrets_seq", "secrets", false},
		{"idx_secrets_project_name", "secrets", true},
		{"idx_volumes_seq", "volumes", false},
		{"idx_volumes_project_name", "volumes", true},
		{"idx_networks_seq", "networks", false},
		{"idx_networks_project_name", "networks", true},
	} {
		var table, sqlText string
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?`, idx.name).
			Scan(&table, &sqlText), "index %s must survive the rebuild", idx.name)
		assert.Equal(t, idx.table, table, "index %s table", idx.name)
		if idx.partial {
			assert.Contains(t, sqlText, "WHERE deleted_at = ''",
				"partial index %s must keep the active-row predicate", idx.name)
		}
	}
	// 历史索引不复活（00010/00016 已改名/全局化）。
	for _, name := range []string{"idx_projects_name", "idx_routes_project_host_path"} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n))
		assert.Zero(t, n, "historical index %s must not be resurrected", name)
	}
	// 临时表不残留。
	for _, table := range []string{
		"projects_new", "apps_new", "routes_new", "databases_new",
		"secrets_new", "volumes_new", "networks_new",
	} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n))
		assert.Zero(t, n, "rebuild staging table %s must not survive the rename", table)
	}

	// 外键：子行存活、无违规、开关恢复 ON（孤儿插入必须被拒）。
	var hookApp string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT app_id FROM app_hooks WHERE app_id = 'app-era46-null'`).Scan(&hookApp))
	assert.Equal(t, "app-era46-null", hookApp)

	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	assert.False(t, rows.Next(), "apps rebuild must leave no foreign key violations")
	require.NoError(t, rows.Err())

	_, err = db.ExecContext(ctx, `INSERT INTO app_hooks
		(app_id, repo, token_sha256, secret_ciphertext, created_at, updated_at)
		VALUES ('app-era46-missing', 'fleetly/example', 'sha256:orphan', X'00',
		        '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z')`)
	assert.Error(t, err, "foreign_keys must be back ON after the rebuild")

	// partial unique 语义：活跃重名拒、tombstone 重名放行。
	_, err = db.ExecContext(ctx, `INSERT INTO projects
		(id, seq, name, team_id, created_at, updated_at, deleted_at)
		VALUES ('project-era46-dup', 103, 'null-project', 'default',
		        '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '')`)
	assert.Error(t, err, "active duplicate name must hit the partial unique index")
	_, err = db.ExecContext(ctx, `INSERT INTO projects
		(id, seq, name, team_id, created_at, updated_at, deleted_at)
		VALUES ('project-era46-dup-tomb', 104, 'null-project', 'default',
		        '2026-11-11T00:00:00Z', '2026-11-11T00:00:00Z', '2026-11-10T00:00:00Z')`)
	assert.NoError(t, err, "tombstoned duplicates stay outside the partial unique index")
}

// TestMigrateForwardCoreGraphForeignKeys：00047 的换装锚——v46 库的核心
// 资源图（team/project/app/revision/deployment/… 全链）前滚后行无损，
// 重建表的外键生效（孤儿插入被拒），foreign_keys 开关恢复 ON。
func TestMigrateForwardCoreGraphForeignKeys(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 46))

	const ts = "2026-11-11T00:00:00Z"
	for _, stmt := range []string{
		`INSERT INTO teams (id, seq, name, created_at) VALUES ('team-era47', 1, 'era47', '` + ts + `')`,
		`INSERT INTO projects (id, seq, name, team_id, created_at, updated_at, deleted_at)
			VALUES ('project-era47', 1, 'era47', 'team-era47', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at)
			VALUES ('app-era47', 1, 'project-era47', 'web', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO revisions (id, app_id, seq, digest, spec, created_at, source_text)
			VALUES ('rev-era47', 'app-era47', 1, 'sha256:era47', x'7b7d', '` + ts + `', '')`,
		`INSERT INTO deployments (id, seq, app_id, to_revision, state, created_at, updated_at)
			VALUES ('dep-era47', 1, 'app-era47', 'rev-era47', 'succeeded', '` + ts + `', '` + ts + `')`,
		`INSERT INTO builds (id, seq, app_id, state, created_at, updated_at)
			VALUES ('build-era47', 1, 'app-era47', 'succeeded', '` + ts + `', '` + ts + `')`,
		`INSERT INTO routes (id, seq, project_id, host, app_id, process, port, created_at, updated_at, deleted_at)
			VALUES ('route-era47', 1, 'project-era47', 'era47.example.test', 'app-era47', 'web', 8080, '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO secrets (id, seq, project_id, name, ciphertext, fingerprint, created_at, updated_at, deleted_at)
			VALUES ('secret-era47', 1, 'project-era47', 's', x'00', 'fp', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO configs (id, seq, project_id, name, version, content, created_at)
			VALUES ('config-era47', 1, 'project-era47', 'c', 1, x'00', '` + ts + `')`,
		`INSERT INTO volumes (id, seq, project_id, name, created_at, updated_at, deleted_at)
			VALUES ('volume-era47', 1, 'project-era47', 'v', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO networks (id, seq, project_id, name, created_at, deleted_at)
			VALUES ('network-era47', 1, 'project-era47', 'n', '` + ts + `', '')`,
		`INSERT INTO network_peers (id, seq, network_id, peer_project_id, state, created_at, updated_at)
			VALUES ('peer-era47', 1, 'network-era47', 'project-era47', 'pending', '` + ts + `', '` + ts + `')`,
		`INSERT INTO tasks (id, seq, project_id, form, state, spec, created_at, updated_at)
			VALUES ('task-era47', 1, 'project-era47', 'one-shot', 'active', x'7b7d', '` + ts + `', '` + ts + `')`,
		`INSERT INTO runs (id, seq, task_id, project_id, state, created_at, updated_at)
			VALUES ('run-era47', 1, 'task-era47', 'project-era47', 'pending', '` + ts + `', '` + ts + `')`,
		`INSERT INTO schedules (id, seq, project_id, state, cron_expr, timezone, spec, created_at, updated_at)
			VALUES ('sched-era47', 1, 'project-era47', 'active', '* * * * *', 'UTC', x'7b7d', '` + ts + `', '` + ts + `')`,
		`INSERT INTO databases (id, seq, project_id, name, engine, credentials_ref, version, created_at, updated_at)
			VALUES ('db-era47', 1, 'project-era47', 'db', 'postgres', 'database:db', '18', '` + ts + `', '` + ts + `')`,
		`INSERT INTO backups (id, seq, project_id, subject_id, engine, created_at)
			VALUES ('backup-era47', 1, 'project-era47', 'db-era47', 'postgres', '` + ts + `')`,
		`INSERT INTO alert_rules (id, seq, app_id, metric, threshold, created_at, updated_at)
			VALUES ('rule-era47', 1, 'app-era47', 'cpu_percent', 80, '` + ts + `', '` + ts + `')`,
		`INSERT INTO shared_variables (id, seq, project_id, name, value, created_at, updated_at, deleted_at)
			VALUES ('sharedvar-era47', 1, 'project-era47', 'k', 'v', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO app_variables (id, seq, app_id, name, value, created_at, updated_at, deleted_at)
			VALUES ('appvar-era47', 1, 'app-era47', 'k', 'v', '` + ts + `', '` + ts + `', '')`,
		`INSERT INTO browse_sessions (id, seq, project_id, database_id, engine, read_only, created_at, expires_at)
			VALUES ('browse-era47', 1, 'project-era47', 'db-era47', 'postgres', 1, '` + ts + `', '` + ts + `')`,
		`INSERT INTO pubsubs (id, seq, project_id, name, engine, version, credentials_ref, created_at, updated_at)
			VALUES ('ps-era47', 1, 'project-era47', 'bus', 'nats', '2.11', 'pubsub:bus', '` + ts + `', '` + ts + `')`,
		`INSERT INTO bucket_stores (id, seq, project_id, name, engine, version, credentials_ref, created_at, updated_at)
			VALUES ('bs-era47', 1, 'project-era47', 'store', 'silo', '2024', 'bucketstore:store', '` + ts + `', '` + ts + `')`,
		`INSERT INTO topics (id, seq, project_id, pubsub_id, name, partitions, created_at, updated_at)
			VALUES ('topic-era47', 1, 'project-era47', 'ps-era47', 't', 1, '` + ts + `', '` + ts + `')`,
		`INSERT INTO streams (id, seq, project_id, pubsub_id, name, created_at, updated_at)
			VALUES ('stream-era47', 1, 'project-era47', 'ps-era47', 's', '` + ts + `', '` + ts + `')`,
		`INSERT INTO bucket_resources (id, seq, project_id, bucket_store_id, name, created_at, updated_at)
			VALUES ('bucket-era47', 1, 'project-era47', 'bs-era47', 'b', '` + ts + `', '` + ts + `')`,
		`INSERT INTO source_uploads (id, seq, project_id, digest, size_bytes, created_at)
			VALUES ('upload-era47', 1, 'project-era47', 'sha256:x', 1, '` + ts + `')`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.NoError(t, err, "seed at v46: %s", stmt)
	}

	require.NoError(t, goose.UpContext(ctx, db, "."))
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.Equal(t, int64(latestMigrationVersion), version)

	// 存量行无损：每张重建表按主键查回一行。
	for _, tc := range []struct{ table, id string }{
		{"teams", "team-era47"},
		{"projects", "project-era47"},
		{"apps", "app-era47"},
		{"revisions", "rev-era47"},
		{"deployments", "dep-era47"},
		{"builds", "build-era47"},
		{"routes", "route-era47"},
		{"secrets", "secret-era47"},
		{"configs", "config-era47"},
		{"volumes", "volume-era47"},
		{"networks", "network-era47"},
		{"network_peers", "peer-era47"},
		{"tasks", "task-era47"},
		{"runs", "run-era47"},
		{"schedules", "sched-era47"},
		{"databases", "db-era47"},
		{"backups", "backup-era47"},
		{"alert_rules", "rule-era47"},
		{"shared_variables", "sharedvar-era47"},
		{"app_variables", "appvar-era47"},
		{"browse_sessions", "browse-era47"},
		{"pubsubs", "ps-era47"},
		{"bucket_stores", "bs-era47"},
		{"topics", "topic-era47"},
		{"streams", "stream-era47"},
		{"bucket_resources", "bucket-era47"},
		{"source_uploads", "upload-era47"},
	} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&n),
			"%s.%s must survive the rebuild", tc.table, tc.id)
		assert.Equal(t, 1, n, "%s.%s must survive the rebuild", tc.table, tc.id)
	}

	// 外键开关恢复 ON，孤儿插入被拒（新外键生效）。
	var fk int
	require.NoError(t, db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk))
	assert.Equal(t, 1, fk, "foreign_keys must be back ON after the rebuild")
	_, err = db.ExecContext(ctx, `INSERT INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at)
		VALUES ('app-era47-orphan', 99, 'project-missing', 'orphan', ?, ?, '')`, ts, ts)
	assert.Error(t, err, "orphan app insert must be rejected after the rebuild")

	// 全库无外键违规。
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	assert.False(t, rows.Next(), "v47 must leave no foreign key violations")
	require.NoError(t, rows.Err())
}

// TestMigrateForwardCoreGraphForeignKeysRejectsOrphans：孤儿行 fail-closed——
// v46 库允许无父行的子行（外键尚未落地），前滚到 v47 时复制后的全库
// foreign_key_check 命中即 CHECK 失败，迁移拒绝放行（不静默丢行/补父行）。
func TestMigrateForwardCoreGraphForeignKeysRejectsOrphans(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)

	require.NoError(t, goose.UpToContext(ctx, db, ".", 46))
	const ts = "2026-11-11T00:00:00Z"
	_, err := db.ExecContext(ctx, `INSERT INTO apps (id, seq, project_id, name, created_at, updated_at, deleted_at)
		VALUES ('app-era47-orphan', 1, 'project-missing', 'orphan', ?, ?, '')`, ts, ts)
	require.NoError(t, err, "v46 has no apps.project_id foreign key yet")

	err = goose.UpContext(ctx, db, ".")
	require.Error(t, err, "orphan rows must fail the v47 migration")
	assert.Contains(t, err.Error(), "CHECK constraint failed")
}

// TestMigrateForwardFromRecentCheckpoints 对每个近期检查点验证「该检查
// 点之后的库 = 在役库形态」：fresh 库钉在检查点后一滚到顶（真实升级
// 路径是逐版前滚，检查点代表中途接管的库）。
func TestMigrateForwardFromRecentCheckpoints(t *testing.T) {
	for _, cp := range []int64{34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46} {
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
