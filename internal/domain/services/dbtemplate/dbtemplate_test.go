package dbtemplate_test

// 模板面的零漂移钉板（架构评审第二轮候选 1）：per-engine adapter 的完整
// 输出面逐字钉死——重构批的行为不变证明。F2.1 加 mysql/mongo 时本表
// 追加行即验收面；F2.2 备份槽与 F2.7 digest 钉定已启用（ADR-0045）。B2a
//（ADR-0094）起本表按**版本矩阵全量**执法：每引擎×每在册版本一行（镜像
// tag@digest 双段、PGDATA 布局分叉、Meta 版本回显逐版本钉死）；digest 的
// 取值/对账工作面 = scripts/digests.sh（SOP：追 patch 经它回填 + 本表同
// commit 更新）。首版矩阵（ADR-0094 决策 7）：postgres 16/17/18(默认)、
// pgvector 0.8-pg17/0.8-pg18(默认)、mysql 8.4/9(默认)、redis 7.4/8.2(默认)、
// mongo 8.0/8.2(默认)——digest = 2026-11-05 Docker Hub index 实测。

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// defaultVersionFor 断言辅助：engine 的 Default 版本 ID（矩阵终值钉板）。
var defaultVersionFor = map[string]string{
	"postgres": "18",
	"pgvector": "0.8-pg18",
	"mysql":    "9",
	"redis":    "8.2",
	"mongo":    "8.2",
}

func TestRegistryValueDomain(t *testing.T) {
	assert.Equal(t, []string{"mongo", "mysql", "pgvector", "postgres", "redis"}, dbtemplate.Engines())

	_, err := dbtemplate.For("oracle", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, dbtemplate.ErrUnknownEngine, "unregistered engine must be a distinguishable error")
	assert.Contains(t, err.Error(), "available engines", "acceptance maps this into a message listing legal values")

	// InfoFor（B2c 定位）：engine 值域检查与 port 回显面——version 回显已
	// 换源行值直读（databases 行 version 列），本面不再承担 version 回显。
	info, ok := dbtemplate.InfoFor("postgres")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "18", Port: 5432}, info)

	info, ok = dbtemplate.InfoFor("mysql")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "9", Port: 3306}, info)

	info, ok = dbtemplate.InfoFor("mongo")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "8.2", Port: 27017}, info)

	_, ok = dbtemplate.InfoFor("oracle")
	assert.False(t, ok, "unregistered engine must not resolve")

	// VersionsFor：目录/校验面（排序稳定 = 声明序旧→新）。
	versions, ok := dbtemplate.VersionsFor("postgres")
	require.True(t, ok)
	assert.Equal(t, []string{"16", "17", "18"}, dbtemplate.VersionIDs(versions))
	_, ok = dbtemplate.VersionsFor("oracle")
	assert.False(t, ok)
}

// TestForVersionResolution：绑版解析面（ADR-0094 决策 2）——空 version =
// Default；绑版实例 Image/Meta 按版本分叉；未知/弃用可区分错误。
func TestForVersionResolution(t *testing.T) {
	// 空 version = Default 绑定（矩阵终值钉板）。
	for engine, wantID := range defaultVersionFor {
		tpl, err := dbtemplate.For(engine, "")
		require.NoError(t, err, "%s: empty version must resolve the default", engine)
		assert.Equal(t, wantID, tpl.Meta().Version, "%s: default version echo", engine)
	}

	// postgres 绑版分叉：Image/Meta/PGDATA 按版本。
	pg16, err := dbtemplate.For("postgres", "16")
	require.NoError(t, err)
	assert.Equal(t, "16", pg16.Meta().Version)
	assert.Equal(t, "postgres:16-bookworm@sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9", pg16.Image())
	pg18, err := dbtemplate.For("postgres", "18")
	require.NoError(t, err)
	assert.Equal(t, "18", pg18.Meta().Version)
	assert.Equal(t, "postgres:18-trixie@sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583", pg18.Image())
	assert.Equal(t, pg18.ImageDigest(), "sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583")

	env16, _ := pg16.Workload(nil)
	env18, _ := pg18.Workload(nil)
	assert.Equal(t, "/var/lib/postgresql/pgdata", env16["PGDATA"], "PG ≤17 keeps the explicit pgdata subdir (VOLUME shadow escape)")
	assert.NotContains(t, env18, "PGDATA", "PG18 walks the image-default 18/docker path (design §2.5)")
	assert.Equal(t, "/var/lib/postgresql", pg16.DataTarget())
	assert.Equal(t, "/var/lib/postgresql", pg18.DataTarget(), "DataTarget stays the parent mount across generations")

	// pgvector 复合短名绑版 + PGDATA 分叉随 pg major 轴。
	pgv17, err := dbtemplate.For("pgvector", "0.8-pg17")
	require.NoError(t, err)
	assert.Equal(t, "0.8-pg17", pgv17.Meta().Version)
	assert.Equal(t, "pgvector/pgvector:0.8.7-pg17-bookworm@sha256:7d0a41464797aeb9862dfdc0bbf1526b82cfd324cdd8036df19e3545f4a9ccb8", pgv17.Image())
	pgvEnv, _ := pgv17.Workload(nil)
	assert.Equal(t, "/var/lib/postgresql/pgdata", pgvEnv["PGDATA"])
	pgv18, err := dbtemplate.For("pgvector", "0.8-pg18")
	require.NoError(t, err)
	pgvEnv18, _ := pgv18.Workload(nil)
	assert.NotContains(t, pgvEnv18, "PGDATA", "0.8-pg18 walks the image-default path like postgres 18")

	// 未知版本：可区分错误 + 列合法值。
	_, err = dbtemplate.For("postgres", "15")
	require.Error(t, err)
	assert.ErrorIs(t, err, dbtemplate.ErrUnknownVersion)
	assert.Contains(t, err.Error(), "16", "error lists the legal version ids")
	assert.Contains(t, err.Error(), "18")

	_, err = dbtemplate.For("pgvector", "0.8-pg16")
	require.Error(t, err)
	assert.ErrorIs(t, err, dbtemplate.ErrUnknownVersion, "the pgvector adapter owns the legal vector×pg combination set (no 2-D registry)")
}

// TestResolveVersionSpecSemantics 住包内白昼测试（dbtemplate_resolve_test.go，
// 直访未导出纯核）；此处经公共 For 面钉弃用错误的可判别形态（真值域首版
// 矩阵无弃用条目——弃用语义经纯核合成表钉死，见彼处）。

func TestTemplateFaces(t *testing.T) {
	for _, tc := range []struct {
		engine     string
		version    string
		meta       dbtemplate.Info
		image      string
		digest     string
		dataTarget string
		env        map[string]string
		command    []string
		probe      []string
		materials  map[string][]byte
		connURL    string
	}{
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "postgres",
			version:    "16",
			meta:       dbtemplate.Info{Version: "16", Port: 5432},
			image:      "postgres:16-bookworm@sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9",
			digest:     "sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
				"PGDATA":                 "/var/lib/postgresql/pgdata",
			},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "postgres",
			version:    "17",
			meta:       dbtemplate.Info{Version: "17", Port: 5432},
			image:      "postgres:17-bookworm@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994",
			digest:     "sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
				"PGDATA":                 "/var/lib/postgresql/pgdata",
			},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "postgres",
			version:    "18",
			meta:       dbtemplate.Info{Version: "18", Port: 5432},
			image:      "postgres:18-trixie@sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583",
			digest:     "sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
				// PG18 无 PGDATA：镜像默认 18/docker 落父挂命名卷内部
				//（design §2.5 绑版分叉）。
			},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "pgvector",
			version:    "0.8-pg17",
			meta:       dbtemplate.Info{Version: "0.8-pg17", Port: 5432},
			image:      "pgvector/pgvector:0.8.7-pg17-bookworm@sha256:7d0a41464797aeb9862dfdc0bbf1526b82cfd324cdd8036df19e3545f4a9ccb8",
			digest:     "sha256:7d0a41464797aeb9862dfdc0bbf1526b82cfd324cdd8036df19e3545f4a9ccb8",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
				"PGDATA":                 "/var/lib/postgresql/pgdata", // pg17 基座：避开镜像 VOLUME 遮蔽（postgres.go pgDataDir 注）
			},
			command: []string{"bash", "-c",
				"mkdir -p /docker-entrypoint-initdb.d && " +
					"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
					"exec docker-entrypoint.sh postgres"},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "pgvector",
			version:    "0.8-pg18",
			meta:       dbtemplate.Info{Version: "0.8-pg18", Port: 5432},
			image:      "pgvector/pgvector:0.8.7-pg18-trixie@sha256:5895949dfcdf308eb0cd31c950c93c14895c77f33fbedfecf1fa511ab1d4bcd2",
			digest:     "sha256:5895949dfcdf308eb0cd31c950c93c14895c77f33fbedfecf1fa511ab1d4bcd2",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
			},
			command: []string{"bash", "-c",
				"mkdir -p /docker-entrypoint-initdb.d && " +
					"printf 'CREATE EXTENSION IF NOT EXISTS vector;\\n' > /docker-entrypoint-initdb.d/01-vector.sql && " +
					"exec docker-entrypoint.sh postgres"},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{
			engine:     "redis",
			version:    "7.4",
			meta:       dbtemplate.Info{Version: "7.4", Port: 6379},
			image:      "redis:7.4@sha256:2e3eb11462c4e3759e6972678dd051d150ff54b1e60714b3c33b701260172a90",
			digest:     "sha256:2e3eb11462c4e3759e6972678dd051d150ff54b1e60714b3c33b701260172a90",
			dataTarget: "/data",
			command:    []string{"redis-server", "/run/secrets/" + dbtemplate.RedisConfFile},
			probe:      []string{"redis-cli", "-p", "6379", "ping"},
			materials: map[string][]byte{
				dbtemplate.RedisConfFile: []byte("requirepass secretpw\nappendonly yes\n"),
			},
			connURL: "redis://:secretpw@db-01j8:6379/0",
		},
		{
			engine:     "redis",
			version:    "8.2",
			meta:       dbtemplate.Info{Version: "8.2", Port: 6379},
			image:      "redis:8.2@sha256:b0b01167ff47bc3868e56a967895a26be4230891a1f42a78ab61cdd227accc8d",
			digest:     "sha256:b0b01167ff47bc3868e56a967895a26be4230891a1f42a78ab61cdd227accc8d",
			dataTarget: "/data",
			command:    []string{"redis-server", "/run/secrets/" + dbtemplate.RedisConfFile},
			probe:      []string{"redis-cli", "-p", "6379", "ping"},
			materials: map[string][]byte{
				dbtemplate.RedisConfFile: []byte("requirepass secretpw\nappendonly yes\n"),
			},
			connURL: "redis://:secretpw@db-01j8:6379/0",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "mysql",
			version:    "8.4",
			meta:       dbtemplate.Info{Version: "8.4", Port: 3306},
			image:      "mysql:8.4@sha256:a81d043930e51745522bd5c430961cf8286f0a23f0a9a2a6e0108f7ea71b0902",
			digest:     "sha256:a81d043930e51745522bd5c430961cf8286f0a23f0a9a2a6e0108f7ea71b0902",
			dataTarget: "/var/lib/mysql",
			env: map[string]string{
				"MYSQL_DATABASE":           "fleetly",
				"MYSQL_USER":               "fleetly",
				"MYSQL_PASSWORD_FILE":      "/run/secrets/" + dbtemplate.MySQLPasswordFile,
				"MYSQL_ROOT_PASSWORD_FILE": "/run/secrets/" + dbtemplate.MySQLRootPasswordFile,
			},
			probe:     []string{"mysqladmin", "ping", "-h", "127.0.0.1"},
			materials: map[string][]byte{dbtemplate.MySQLPasswordFile: []byte("secretpw"), dbtemplate.MySQLRootPasswordFile: []byte("secretpw")},
			connURL:   "mysql://fleetly:secretpw@db-01j8:3306/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "mysql",
			version:    "9",
			meta:       dbtemplate.Info{Version: "9", Port: 3306},
			image:      "mysql:9.7@sha256:df90258da46f6e7ff8574ff84f17618aba0765cac749928299c69fd4e7cb8ad8",
			digest:     "sha256:df90258da46f6e7ff8574ff84f17618aba0765cac749928299c69fd4e7cb8ad8",
			dataTarget: "/var/lib/mysql",
			env: map[string]string{
				"MYSQL_DATABASE":           "fleetly",
				"MYSQL_USER":               "fleetly",
				"MYSQL_PASSWORD_FILE":      "/run/secrets/" + dbtemplate.MySQLPasswordFile,
				"MYSQL_ROOT_PASSWORD_FILE": "/run/secrets/" + dbtemplate.MySQLRootPasswordFile,
			},
			probe:     []string{"mysqladmin", "ping", "-h", "127.0.0.1"},
			materials: map[string][]byte{dbtemplate.MySQLPasswordFile: []byte("secretpw"), dbtemplate.MySQLRootPasswordFile: []byte("secretpw")},
			connURL:   "mysql://fleetly:secretpw@db-01j8:3306/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "mongo",
			version:    "8.0",
			meta:       dbtemplate.Info{Version: "8.0", Port: 27017},
			image:      "mongo:8.0@sha256:b9924c330a8bbc5800fe9c69f04a84e221eca7b28fd049a0354147d9908fa142",
			digest:     "sha256:b9924c330a8bbc5800fe9c69f04a84e221eca7b28fd049a0354147d9908fa142",
			dataTarget: "/data/db",
			command: []string{"sh", "-c",
				"cp /run/secrets/" + dbtemplate.MongoInitJSFile + " /docker-entrypoint-initdb.d/10-fleetly-user.js" +
					" && exec docker-entrypoint.sh mongod --config /run/secrets/" + dbtemplate.MongoConfFile},
			probe: []string{"mongosh", "--quiet", "--eval", "db.adminCommand('ping')"},
			materials: map[string][]byte{
				dbtemplate.MongoConfFile: []byte("storage:\n  dbPath: /data/db\nnet:\n  port: 27017\nsecurity:\n  authorization: enabled\n"),
				dbtemplate.MongoInitJSFile: []byte(
					"db.getSiblingDB('fleetly').createUser({user: 'fleetly', pwd: 'secretpw', roles: [{role: 'readWrite', db: 'fleetly'}]});\n"),
			},
			connURL: "mongodb://fleetly:secretpw@db-01j8:27017/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "mongo",
			version:    "8.2",
			meta:       dbtemplate.Info{Version: "8.2", Port: 27017},
			image:      "mongo:8.2@sha256:e0ce8c35124d4a9f9785532d1f268f39e9728ffa1cb38f46fa482436424c4bd3",
			digest:     "sha256:e0ce8c35124d4a9f9785532d1f268f39e9728ffa1cb38f46fa482436424c4bd3",
			dataTarget: "/data/db",
			command: []string{"sh", "-c",
				"cp /run/secrets/" + dbtemplate.MongoInitJSFile + " /docker-entrypoint-initdb.d/10-fleetly-user.js" +
					" && exec docker-entrypoint.sh mongod --config /run/secrets/" + dbtemplate.MongoConfFile},
			probe: []string{"mongosh", "--quiet", "--eval", "db.adminCommand('ping')"},
			materials: map[string][]byte{
				dbtemplate.MongoConfFile: []byte("storage:\n  dbPath: /data/db\nnet:\n  port: 27017\nsecurity:\n  authorization: enabled\n"),
				dbtemplate.MongoInitJSFile: []byte(
					"db.getSiblingDB('fleetly').createUser({user: 'fleetly', pwd: 'secretpw', roles: [{role: 'readWrite', db: 'fleetly'}]});\n"),
			},
			connURL: "mongodb://fleetly:secretpw@db-01j8:27017/fleetly",
		},
	} {
		t.Run(tc.engine+"/"+tc.version, func(t *testing.T) {
			tpl, err := dbtemplate.For(tc.engine, tc.version)
			require.NoError(t, err)
			assert.Equal(t, tc.engine, tpl.Engine())
			assert.Equal(t, tc.meta, tpl.Meta())
			assert.Equal(t, tc.image, tpl.Image())
			assert.Equal(t, tc.digest, tpl.ImageDigest())
			assert.Equal(t, tc.dataTarget, tpl.DataTarget())
			assert.Empty(t, tpl.ToolUser(), "database engines declare no tool user (image default root — B8-fix zero behavior change)")
			env, command := tpl.Workload(nil)
			assert.Equal(t, tc.env, env)
			assert.Equal(t, tc.command, command)
			assert.Equal(t, tc.probe, tpl.Probe())
			materials, err := tpl.Materials("secretpw", nil)
			require.NoError(t, err)
			assert.Equal(t, tc.materials, materials)
			assert.Equal(t, tc.connURL, tpl.ConnURL("db-01j8", "secretpw"))
		})
	}
}

// TestBackupRestoreFaces：备份/恢复渲染的零漂移钉板（F2.2 槽启用，
// ADR-0039 决策 5——argv 逐字钉死；改动引擎工具面即红）。备份/恢复工具
// 面跨版本零分叉（B2a 矩阵），按引擎钉 Default 版本即可。
func TestBackupRestoreFaces(t *testing.T) {
	for _, tc := range []struct {
		engine      string
		backupArgv  []string
		backupEnv   map[string]string
		backupMats  map[string][]byte
		restoreArgv []string
		restoreMode dbtemplate.RestoreMode
	}{
		{
			engine:      "postgres",
			backupArgv:  []string{"pg_dump", "-h", "db-01j8", "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--format=custom"},
			backupEnv:   map[string]string{"PGPASSFILE": "/run/secrets/database-backup-pgpass"}, //nolint:gosec // G101 误报：路径串非凭证
			backupMats:  map[string][]byte{"database-backup-pgpass": []byte("db-01j8:5432:fleetly:fleetly:secretpw\n")},
			restoreArgv: []string{"pg_restore", "-h", "db-01j8", "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--no-password", dbtemplate.BackupInputPath},
			restoreMode: dbtemplate.RestoreStream,
		},
		{
			engine:      "pgvector",
			backupArgv:  []string{"pg_dump", "-h", "db-01j8", "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--format=custom"},
			backupEnv:   map[string]string{"PGPASSFILE": "/run/secrets/database-backup-pgpass"}, //nolint:gosec // G101 误报：路径串非凭证
			backupMats:  map[string][]byte{"database-backup-pgpass": []byte("db-01j8:5432:fleetly:fleetly:secretpw\n")},
			restoreArgv: []string{"pg_restore", "-h", "db-01j8", "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--no-password", dbtemplate.BackupInputPath},
			restoreMode: dbtemplate.RestoreStream,
		},
		{
			engine: "mysql",
			backupArgv: []string{"mysqldump", "--defaults-extra-file=/run/secrets/database-backup-defaults",
				"--single-transaction", "--routines", "--triggers", "--events", "fleetly"},
			backupMats:  map[string][]byte{"database-backup-defaults": []byte("[client]\nhost=db-01j8\nuser=fleetly\npassword=secretpw\n")},
			restoreArgv: []string{"mysql", "--defaults-extra-file=/run/secrets/database-backup-defaults", "-e", "source " + dbtemplate.BackupInputPath, "fleetly"},
			restoreMode: dbtemplate.RestoreStream,
		},
		{
			engine: "mongo",
			backupArgv: []string{"mongodump", "--config=/run/secrets/database-backup-config",
				"--archive", "--gzip", "--db", "fleetly"},
			backupMats:  map[string][]byte{"database-backup-config": []byte(`{"uri":"mongodb://fleetly:secretpw@db-01j8:27017/fleetly"}`)},
			restoreArgv: []string{"mongorestore", "--config=/run/secrets/database-backup-config", "--archive=" + dbtemplate.BackupInputPath, "--gzip"},
			restoreMode: dbtemplate.RestoreStream,
		},
		{
			engine:     "redis",
			backupArgv: []string{"redis-cli", "-h", "db-01j8", "-p", "6379", "--rdb", "-"},
			backupEnv:  map[string]string{"REDISCLI_AUTH": "secretpw"},
			restoreArgv: []string{"sh", "-c",
				"cp /run/secrets/backup-input /seed/dump.rdb; " +
					"redis-server --dir /seed --port 6399 --daemonize no --appendonly no & " +
					"i=0; until redis-cli -p 6399 ping >/dev/null 2>&1; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
					"redis-cli -p 6399 BGREWRITEAOF; " +
					"i=0; until [ -f /seed/appendonlydir/appendonly.aof.manifest ]; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
					"redis-cli -p 6399 shutdown nosave; rm -f /seed/dump.rdb"},
			restoreMode: dbtemplate.RestorePreseed,
		},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			tpl, err := dbtemplate.For(tc.engine, "")
			require.NoError(t, err)
			backup, err := tpl.Backup("db-01j8", "secretpw")
			require.NoError(t, err)
			assert.Equal(t, tc.backupArgv, backup.Argv)
			assert.Equal(t, tc.backupEnv, backup.Env)
			assert.Equal(t, tc.backupMats, backup.SecretFiles)

			restore, err := tpl.Restore("db-01j8", "secretpw")
			require.NoError(t, err)
			assert.Equal(t, tc.restoreMode, restore.Mode)
			assert.Equal(t, tc.restoreArgv, restore.Argv)
			if tc.engine == "redis" {
				assert.Empty(t, restore.Env)
				assert.Empty(t, restore.SecretFiles)
			} else {
				assert.Equal(t, backup.Env, restore.Env)
				assert.Equal(t, backup.SecretFiles, restore.SecretFiles)
			}
		})
	}
}

// TestInjectionBackupRenders：P2 注入家族——渲染面的敌意载荷断言
// （ADR-0039 决策 11）。三面：① 敌意密码在渲染入口被字符集闸拒绝
// （pgpass 冒号/INI 换行/JS 引号/redis.conf 空白全类断源）；② 敌意
// host 不改变 argv 元素形状（数组构造性无 shell，钉死渲染器不拼接）；
// ③ 生成结构化文本经解析器断言（mysql INI 恰一节 [client]——"单语句
// 校验"的结构化文本等价物；mongo config JSON 往返）。矩阵全量执法
// （B2a）：敌意密码闸按引擎×版本反扫。
func TestInjectionBackupRenders(t *testing.T) {
	hostilePasswords := []string{
		"pw; touch /tmp/pwned",
		"pw`touch /tmp/pwned`",
		"pw$(touch /tmp/pwned)",
		"pw && touch /tmp/pwned",
		"pw\n[client2]\nhost=evil",
		"pw:5432:*:*:evil",
		"pw' , roles: []}); db.shutdown(); //",
		"pw house",
	}
	for _, engine := range dbtemplate.Engines() {
		versions, ok := dbtemplate.VersionsFor(engine)
		require.True(t, ok)
		for _, spec := range versions {
			t.Run(engine+"/"+spec.ID, func(t *testing.T) {
				tpl, err := dbtemplate.For(engine, spec.ID)
				require.NoError(t, err)
				for _, pw := range hostilePasswords {
					_, err := tpl.Backup("db-01j8", pw)
					assert.Error(t, err, "%s/%s: hostile password must be rejected at the render gate: %q", engine, spec.ID, pw)
					_, err = tpl.Restore("db-01j8", pw)
					assert.Error(t, err, "%s/%s: hostile password must be rejected at the render gate: %q", engine, spec.ID, pw)
					_, err = tpl.Materials(pw, nil)
					assert.Error(t, err, "%s/%s: hostile password must be rejected at the render gate: %q", engine, spec.ID, pw)
				}
			})
		}
	}

	// 敌意 host：形状不变性——argv 元素数与干净 host 渲染完全一致（host
	// 整体落进单元素，不分裂、不解释；平台铸名 db-<id> 到不了这种形态，
	// 纵深）。mysql defaults 里的 host 同样只进值位（解析器断言在下）。
	hostileHosts := []string{"db-x; touch /tmp/pwned", "db-x$(pwned)", "db-x -c shell"}
	for _, engine := range []string{"postgres", "mysql", "mongo", "redis"} {
		tpl, err := dbtemplate.For(engine, "")
		require.NoError(t, err)
		baseline, err := tpl.Backup("db-01j8", "secretpw")
		require.NoError(t, err)
		for _, host := range hostileHosts {
			backup, err := tpl.Backup(host, "secretpw")
			require.NoError(t, err)
			assert.Len(t, backup.Argv, len(baseline.Argv), "%s/%s: argv shape must not depend on host content", engine, host)
			for i := range backup.Argv {
				if backup.Argv[i] == host {
					continue // host 整体占一个元素是唯一合法位置
				}
				assert.Equal(t, baseline.Argv[i], backup.Argv[i], "%s/%s: non-host argv elements must be invariant", engine, host)
			}
			assert.NotContains(t, backup.Argv, "-c", "%s/%s: no interpreter flag may appear via host", engine, host)
		}
	}

	// mysql defaults 文件：解析器断言——恰一节 [client]，键集恰为
	// host/user/password（换行注入类载荷已被字符集闸拒之门外，这里钉
	// 生成器的结构不变性）。
	mysqlTpl, err := dbtemplate.For("mysql", "")
	require.NoError(t, err)
	mysqlBackup, err := mysqlTpl.Backup("db-01j8", "secretpw2")
	require.NoError(t, err)
	defaults := string(mysqlBackup.SecretFiles["database-backup-defaults"])
	sections, keys := parseINI(t, defaults)
	assert.Equal(t, []string{"client"}, sections, "defaults file must carry exactly one [client] section")
	assert.Equal(t, map[string]string{"host": "db-01j8", "user": "fleetly", "password": "secretpw2"}, keys)

	// mongo config：JSON 往返（uri 键回读 = ConnURL 单源）。
	mongoTpl, err := dbtemplate.For("mongo", "")
	require.NoError(t, err)
	backup, err := mongoTpl.Backup("db-01j8", "secretpw3")
	require.NoError(t, err)
	var cfg map[string]string
	require.NoError(t, json.Unmarshal(backup.SecretFiles["database-backup-config"], &cfg))
	assert.Equal(t, mongoTpl.ConnURL("db-01j8", "secretpw3"), cfg["uri"])

	// redis conf：行级断言（requirepass 单 token + appendonly 布尔行）。
	redisTpl, err := dbtemplate.For("redis", "")
	require.NoError(t, err)
	rmats, err := redisTpl.Materials("secretpw4", nil)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(rmats[dbtemplate.RedisConfFile]), "\n"), "\n")
	assert.Equal(t, []string{"requirepass secretpw4", "appendonly yes"}, lines)
}

// parseINI 是测试侧最小 INI 断言器（节头行 + key=value 行；P2"解析器级
// 断言"的 ini 面——非通用解析器，断言专用）。
func parseINI(t *testing.T, text string) ([]string, map[string]string) {
	t.Helper()
	var sections []string
	keys := map[string]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			current = line[1 : len(line)-1]
			sections = append(sections, current)
		default:
			if current == "" {
				t.Fatalf("ini: key outside any section: %q", line)
			}
			k, v, ok := strings.Cut(line, "=")
			require.True(t, ok, "ini: line is not key=value: %q", line)
			keys[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return sections, keys
}

// TestVolumeShadowContract：镜像 VOLUME 遮蔽契约（2026-10-04 staging
// 实录的静态执法面）。docker 对"嵌套在命名卷内部的镜像 VOLUME 路径"铸
// 匿名卷并遮蔽命名卷同名子目录——数据面卷模板必须二选一：
//   - DataTarget 与镜像 VOLUME 路径精确重合（挂载覆盖声明，无匿名卷；
//     PG18 起 postgres 系 VOLUME 声明迁到 /var/lib/postgresql 本身，父挂
//     即重合形态）；
//   - 或（postgres 系 ≤17 父目录挂法）显式 PGDATA 钉进命名卷内部、且
//     不落在镜像 VOLUME 声明路径之上。
//
// 违反任一形态 = 任务替换即空库服现（数据隐形丢失），本表钉死。B2a 起
// 按**引擎×版本**执法（同一引擎不同代的 VOLUME 声明可以不同——PG16/17
// 声明 data 子路径、PG18 声明父路径）。
//
// volumeShadowTable 是 VOLUME 契约的**唯一**预期表（共享夹具，2026-10-05
// N2 评审 P2-1 执法补强）：本测试消费其静态面（DataTarget/PGDATA 与声明
// 集的关系断言，形态派生自表 + DataTarget），TestVolumeShadowContractLive
// （dbtemplate_live_test.go）消费其镜像真源面（Docker Hub 钉定 index 实测
// 的 VOLUME 集与本表对账）——两个测试同源消费，禁止出现第二份表。
//
// 表值 = 镜像 config 的 Volumes 键集（2026-11-05 B2a 矩阵 11 镜像 registry
// 实测）：mongo 另声明 /data/configdb——在 DataTarget（/data/db）之外，
// 任务替换铸匿名卷（N2 评审 P2-4 泄漏台账的具体镜像遗产面；无数据丢失
// 面），如实记档而非视而不见。上游增删任何声明路径 = live 核对红。
type volumeShadowRow struct {
	engine       string
	version      string
	imageVolumes []string // 镜像声明的 VOLUME 路径全集（live 核对对账面）
}

var volumeShadowTable = []volumeShadowRow{
	{engine: "postgres", version: "16", imageVolumes: []string{"/var/lib/postgresql/data"}}, // 父挂 + PGDATA 重定向
	{engine: "postgres", version: "17", imageVolumes: []string{"/var/lib/postgresql/data"}},
	{engine: "postgres", version: "18", imageVolumes: []string{"/var/lib/postgresql"}}, // 18+ 布局：VOLUME=父路径，父挂即精确重合
	{engine: "pgvector", version: "0.8-pg17", imageVolumes: []string{"/var/lib/postgresql/data"}},
	{engine: "pgvector", version: "0.8-pg18", imageVolumes: []string{"/var/lib/postgresql"}},
	{engine: "mysql", version: "8.4", imageVolumes: []string{"/var/lib/mysql"}},
	{engine: "mysql", version: "9", imageVolumes: []string{"/var/lib/mysql"}},
	{engine: "mongo", version: "8.0", imageVolumes: []string{"/data/configdb", "/data/db"}}, // configdb 在 DataTarget 之外：匿名卷泄漏面（P2-4），无遮蔽险
	{engine: "mongo", version: "8.2", imageVolumes: []string{"/data/configdb", "/data/db"}},
	{engine: "redis", version: "7.4", imageVolumes: []string{"/data"}},
	{engine: "redis", version: "8.2", imageVolumes: []string{"/data"}},
}

func TestVolumeShadowContract(t *testing.T) {
	for _, tc := range volumeShadowTable {
		t.Run(tc.engine+"/"+tc.version, func(t *testing.T) {
			tpl, err := dbtemplate.For(tc.engine, tc.version)
			require.NoError(t, err)
			target := tpl.DataTarget()
			// 声明路径按与 DataTarget 的关系三分（形态断言全派生自表 +
			// DataTarget，无第二份语义）：
			//   精确重合 → 挂载覆盖声明（无匿名卷）；
			//   嵌套在 DataTarget 之下 → 遮蔽险（postgres 系 ≤17 父挂形态）
			//     → 显式 PGDATA 逃逸断言（逐嵌套路径）；
			//   在 DataTarget 之外 → 任务替换铸匿名卷（泄漏面 P2-4，无
			//     数据丢失面）——如实记档，不判红。
			// 数据面挂载必须落前两形态之一（二选一契约）。
			exact := false
			var nested []string
			for _, p := range tc.imageVolumes {
				switch {
				case p == target:
					exact = true
				case strings.HasPrefix(p, target+"/"):
					nested = append(nested, p)
				}
			}
			require.True(t, exact || len(nested) > 0,
				"%s/%s: DataTarget %q neither coincides with nor parents any declared image VOLUME %v (mount contract requires one of the two forms)",
				tc.engine, tc.version, target, tc.imageVolumes)
			for _, p := range nested {
				// 父挂形态：PGDATA 必须在场、落在 DataTarget 内部、且不在该
				// 嵌套声明路径之上或之下（其下任意路径都在匿名卷里）。
				env, _ := tpl.Workload(nil)
				pgdata, ok := env["PGDATA"]
				require.True(t, ok, "%s/%s: parent-mount DataTarget requires explicit PGDATA (shadowing path %q)", tc.engine, tc.version, p)
				assert.True(t, strings.HasPrefix(pgdata, target+"/"),
					"%s/%s: PGDATA %q must live inside DataTarget %q", tc.engine, tc.version, pgdata, target)
				assert.NotEqual(t, p, pgdata,
					"%s/%s: PGDATA must not sit on the image VOLUME path %q (anonymous volume shadows it)", tc.engine, tc.version, p)
				assert.False(t, strings.HasPrefix(pgdata, p+"/"),
					"%s/%s: PGDATA %q must not live under the image VOLUME path %q", tc.engine, tc.version, pgdata, p)
			}
		})
	}
}

// TestImageDigestsPinned：digest 钉定门禁（ADR-0045 决策 3；B2a 起按
// ADR-0094 扩容至**版本矩阵全量**）——每引擎×每在册版本反扫：
//   - digest 非空、形态合法（带算法前缀 + 64 hex）；
//   - Image() 恰为 tag@digest 双段拼装（透传完整性：钉定值在引用面不被
//     改写），tag 段携带版本可读面；
//   - 矩阵不变量：ID 唯一且非空、每引擎恰一 Default、Default 非弃用、
//     Deprecated ⇒ Note 非空（目录 Note 面的完整性）；
//   - Versions() 与绑版实例自洽（绑版 Image/Meta 与 spec 逐一对上）。
//
// 新增引擎/版本漏钉或拼装面绕开 PinnedRef 即红。
func TestImageDigestsPinned(t *testing.T) {
	digestRe := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for _, engine := range dbtemplate.Engines() {
		t.Run(engine, func(t *testing.T) {
			specs, ok := dbtemplate.VersionsFor(engine)
			require.True(t, ok)
			require.NotEmpty(t, specs, "%s: registry entry carries no versions", engine)
			seenIDs := map[string]bool{}
			defaults := 0
			for _, spec := range specs {
				require.NotEmpty(t, spec.ID, "%s: version entry without an id", engine)
				require.False(t, seenIDs[spec.ID], "%s: duplicate version id %q", engine, spec.ID)
				seenIDs[spec.ID] = true
				require.NotEmpty(t, spec.Tag, "%s/%s: version entry without a tag", engine, spec.ID)
				require.Regexp(t, digestRe, spec.Digest,
					"%s/%s: digest must be a pinned index digest with algorithm prefix (ADR-0045)", engine, spec.ID)
				if spec.Deprecated {
					require.NotEmpty(t, spec.Note, "%s/%s: deprecated versions must carry a note (catalog face, ADR-0094 decision 6)", engine, spec.ID)
				}
				if spec.Default {
					defaults++
					require.False(t, spec.Deprecated, "%s/%s: the default version must not be deprecated (first-matrix invariant; flipping default retires the old major explicitly)", engine, spec.ID)
				}

				tpl, err := dbtemplate.For(engine, spec.ID)
				require.NoError(t, err, "%s/%s: in-registry version must bind", engine, spec.ID)
				image := tpl.Image()
				assert.True(t, strings.HasSuffix(image, "@"+spec.Digest),
					"%s/%s: Image() must be exactly <tag>@<digest> — got %q", engine, spec.ID, image)
				ref := strings.TrimSuffix(image, "@"+spec.Digest)
				assert.NotContains(t, ref, "@", "%s/%s: reference carries more than one digest segment: %q", engine, spec.ID, image)
				assert.Contains(t, ref, ":", "%s/%s: tag segment must carry a version tag (readability face): %q", engine, spec.ID, image)
				assert.True(t, strings.HasSuffix(ref, ":"+spec.Tag),
					"%s/%s: Image() tag segment must be the spec tag %q — got %q", engine, spec.ID, spec.Tag, image)
				assert.Equal(t, spec.Digest, tpl.ImageDigest(), "%s/%s: ImageDigest must equal the spec digest", engine, spec.ID)
				assert.Equal(t, spec.ID, tpl.Meta().Version, "%s/%s: bound Meta echoes the version id", engine, spec.ID)
			}
			assert.Equal(t, 1, defaults, "%s: exactly one default version per engine (got %d)", engine, defaults)
		})
	}
}

func TestPasswordFromURL(t *testing.T) {
	pw, err := dbtemplate.PasswordFromURL("postgresql://fleetly:secretpw@db-01j8:5432/fleetly")
	require.NoError(t, err)
	assert.Equal(t, "secretpw", pw)

	pw, err = dbtemplate.PasswordFromURL("redis://:secretpw@db-01j8:6379/0")
	require.NoError(t, err)
	assert.Equal(t, "secretpw", pw)

	_, err = dbtemplate.PasswordFromURL("postgresql://db-01j8:5432/fleetly")
	assert.ErrorContains(t, err, "carries no credentials")

	_, err = dbtemplate.PasswordFromURL("postgresql://fleetly@db-01j8:5432/fleetly")
	assert.ErrorContains(t, err, "carries no password")

	_, err = dbtemplate.PasswordFromURL("://not a url")
	assert.ErrorContains(t, err, "parse database connection url")
}

// TestKernelReexports：enginetemplate 内核下沉的零漂移再导出面（B2a，
// engineports facts.go 先例）——dbtemplate 与既有消费面经别名消费，别名
// 集即公共契约面；内核真值的直测住 enginetemplate 包内测试。
func TestKernelReexports(t *testing.T) {
	// 常量/类型别名恒等（编译期已保证；此处钉「公共面存在」防误删）。
	assert.Equal(t, "database", dbtemplate.InstanceFamily)
	assert.Equal(t, enginetemplate.SeedMountPoint, dbtemplate.SeedMountPoint)
	assert.Equal(t, enginetemplate.BackupInputPath, dbtemplate.BackupInputPath)
	assert.Equal(t, enginetemplate.InstanceMarkerFile, dbtemplate.InstanceMarkerFile)
	assert.NotEmpty(t, dbtemplate.MintPassword())

	// 家族铸记薄面：entity 中性内核 + 数据库家族词面注入。
	m := dbtemplate.MintInstanceMarker("01jd0db0000000000000000000", time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	assert.Equal(t, "database", m.Family)
	back, err := dbtemplate.ParseInstanceMarker([]byte(`{"entity_id":"01jd0db0000000000000000000","family":"database","minted_at":"2026-11-04T12:00:00Z"}`))
	require.NoError(t, err)
	assert.Equal(t, m, back)
}

// —— B2b 旋钮批（ADR-0095 决策 1/2/4；design §3.1/§3.2/§3.4）——
// 首批旋钮面、owned 键闭集与双车道喂入律的守卫/金样。ValidateSettings 的
// 语义面单测住 enginetemplate 包内（内核零 per-engine 知识，合成夹具）；
// 本节钉五引擎真值域。

// knobSampleValue 为旋钮生成合法样本值（TestDynamicKeysAbsentFromStaticRender
// 的全键 settings 夹具；值随序位变化保证跨键唯一——值级缺席断言不被静态
// 车道合法出现的同值混淆）。
func knobSampleValue(k enginetemplate.Knob, i int) string {
	switch k.Kind {
	case enginetemplate.KnobBool:
		return "true"
	case enginetemplate.KnobInt:
		return strconv.Itoa(100 + i)
	case enginetemplate.KnobEnum:
		if len(k.Enum) == 0 {
			return "enumless"
		}
		return k.Enum[i%len(k.Enum)]
	case enginetemplate.KnobDuration:
		return strconv.Itoa(30+i) + "s"
	default: // KnobSize
		return strconv.Itoa(128*(i+1)) + "mb"
	}
}

// knobSampleSettings 构造覆盖全部旋钮键的 settings（static + dynamic 混合）。
func knobSampleSettings(knobs []enginetemplate.Knob) map[string]string {
	settings := map[string]string{}
	for i, k := range knobs {
		settings[k.Key] = knobSampleValue(k, i)
	}
	return settings
}

// TestFirstBatchKnobFaces：首批旋钮落位钉板（design §3.4，五引擎矩阵终
// 值）——键/Kind/class/范围/enum/缺省逐项钉死；增删旋钮即改本表（旋钮集
// 只增不改语义，ADR-0095 决策 3）。
func TestFirstBatchKnobFaces(t *testing.T) {
	for _, tc := range []struct {
		engine string
		knobs  []enginetemplate.Knob
	}{
		{
			engine: "postgres",
			knobs: []enginetemplate.Knob{
				{Key: "shared_buffers", Kind: enginetemplate.KnobSize, Default: "128mb", Class: enginetemplate.KnobStatic, Category: "memory"},
				{Key: "max_connections", Kind: enginetemplate.KnobInt, Min: "1", Max: "10000", Default: "100", Class: enginetemplate.KnobStatic, Category: "connections"},
				{Key: "work_mem", Kind: enginetemplate.KnobSize, Default: "4mb", Class: enginetemplate.KnobDynamic, Category: "memory"},
			},
		},
		{
			engine: "pgvector", // postgres 基座同集（嵌入继承）
			knobs: []enginetemplate.Knob{
				{Key: "shared_buffers", Kind: enginetemplate.KnobSize, Default: "128mb", Class: enginetemplate.KnobStatic, Category: "memory"},
				{Key: "max_connections", Kind: enginetemplate.KnobInt, Min: "1", Max: "10000", Default: "100", Class: enginetemplate.KnobStatic, Category: "connections"},
				{Key: "work_mem", Kind: enginetemplate.KnobSize, Default: "4mb", Class: enginetemplate.KnobDynamic, Category: "memory"},
			},
		},
		{
			engine: "redis",
			knobs: []enginetemplate.Knob{
				{Key: "appendonly", Kind: enginetemplate.KnobBool, Default: "true", Class: enginetemplate.KnobStatic, Category: "persistence"},
				{Key: "maxmemory", Kind: enginetemplate.KnobSize, Class: enginetemplate.KnobStatic, Category: "memory"},
				{Key: "maxmemory-policy", Kind: enginetemplate.KnobEnum, Default: "noeviction",
					Enum:     []string{"noeviction", "allkeys-lru", "allkeys-lfu", "volatile-lru", "volatile-lfu", "volatile-ttl", "volatile-random", "allkeys-random"},
					Class:    enginetemplate.KnobStatic,
					Category: "memory"},
			},
		},
		{
			engine: "mysql",
			knobs: []enginetemplate.Knob{
				{Key: "max_connections", Kind: enginetemplate.KnobInt, Default: "151", Class: enginetemplate.KnobDynamic, Category: "connections"},
				{Key: "innodb_buffer_pool_size", Kind: enginetemplate.KnobSize, Default: "128mb", Class: enginetemplate.KnobDynamic, Category: "memory"},
			},
		},
		{engine: "mongo", knobs: nil},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			// 各在册版本同集（版本键控面支持分叉，本批钉「不分叉」）。
			versions, ok := dbtemplate.VersionsFor(tc.engine)
			require.True(t, ok)
			for _, spec := range versions {
				tpl, err := dbtemplate.For(tc.engine, spec.ID)
				require.NoError(t, err)
				assert.Equal(t, tc.knobs, tpl.Knobs(), "%s/%s: knob set", tc.engine, spec.ID)
			}
		})
	}
}

// TestKnobSchemaWellFormed：旋钮表良构守卫（注册表完整性——TestImageDigestsPinned
// 对版本矩阵的同款纪律）：键形（引擎原生词、渲染安全字符集）、Kind/Class
// 值域、enum 非空、Min/Max/Default 各自过 ValidateKnobValue 自家口径。
func TestKnobSchemaWellFormed(t *testing.T) {
	keyRe := regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	for _, engine := range dbtemplate.Engines() {
		versions, ok := dbtemplate.VersionsFor(engine)
		require.True(t, ok)
		for _, spec := range versions {
			tpl, err := dbtemplate.For(engine, spec.ID)
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, k := range tpl.Knobs() {
				require.Regexp(t, keyRe, k.Key, "%s/%s: knob keys are engine-native words in the render-safe charset", engine, spec.ID)
				require.False(t, seen[k.Key], "%s/%s: duplicate knob key %q", engine, spec.ID, k.Key)
				seen[k.Key] = true
				require.Contains(t, []enginetemplate.KnobKind{
					enginetemplate.KnobBool, enginetemplate.KnobInt, enginetemplate.KnobEnum,
					enginetemplate.KnobDuration, enginetemplate.KnobSize,
				}, k.Kind, "%s/%s: knob %q carries an unregistered kind", engine, spec.ID, k.Key)
				require.Contains(t, []enginetemplate.KnobClass{
					enginetemplate.KnobStatic, enginetemplate.KnobDynamic,
				}, k.Class, "%s/%s: knob %q carries an unregistered class", engine, spec.ID, k.Key)
				if k.Kind == enginetemplate.KnobEnum {
					require.NotEmpty(t, k.Enum, "%s/%s: enum knob %q carries an empty domain", engine, spec.ID, k.Key)
				}
				// Min/Max/Default 以自身为值过闸：格式与范围自洽（Min 值落
				// [Min,Max] 内是构造性成立的下界检查）。
				for _, bound := range []string{k.Min, k.Max, k.Default} {
					if bound == "" {
						continue
					}
					require.NoError(t, enginetemplate.ValidateKnobValue(k, bound),
						"%s/%s: knob %q bound/default %q fails its own kind gate", engine, spec.ID, k.Key, bound)
				}
			}
		}
	}
}

// TestPlatformOwnedKeysUnreachable：ADR-0095 决策 2 验收锚——每引擎×每
// 在册版本：平台 owned 键闭集与旋钮 Knobs 键集交集恒空（白名单外键本就
// 不可设；本守卫钉死「owned 键永不回流成旋钮」——清单漏项即回流口，
// design §3.1 红队 F-04 补全版为对照基线）。
func TestPlatformOwnedKeysUnreachable(t *testing.T) {
	for _, engine := range dbtemplate.Engines() {
		versions, ok := dbtemplate.VersionsFor(engine)
		require.True(t, ok)
		for _, spec := range versions {
			t.Run(engine+"/"+spec.ID, func(t *testing.T) {
				tpl, err := dbtemplate.For(engine, spec.ID)
				require.NoError(t, err)
				owned := tpl.OwnedKeys()
				require.NotEmpty(t, owned, "%s: owned key set must stay modeled (guard comparison face)", engine)
				seen := map[string]bool{}
				for _, o := range owned {
					require.False(t, seen[o], "%s: duplicate owned key %q", engine, o)
					seen[o] = true
				}
				knobs := enginetemplate.KnobKeys(tpl.Knobs())
				for _, o := range owned {
					assert.NotContains(t, knobs, o,
						"%s/%s: platform-owned key %q must never register as a knob (ADR-0095 decision 2)", engine, spec.ID, o)
				}
			})
		}
	}
}

// TestDynamicKeysAbsentFromStaticRender：喂入律执法（ADR-0095 决策 4；
// design §3.2——验收锚 TestDynamicKeysAbsentFromProjection 的 B2b 射程 =
// static 渲染产物面）：dynamic 旋钮键/值不出现在 Materials 输出与 Workload
// env/command。对每引擎×版本构造覆盖全部旋钮键的 settings（static+dynamic
// 混合——比只喂 dynamic 键更强：逮住「整包 settings 回显」型实现），
// 断言 dynamic 子集双缺席；Materials 内部纵深过滤（staticKnobSettings）
// 与本守卫互为双保险。
func TestDynamicKeysAbsentFromStaticRender(t *testing.T) {
	for _, engine := range dbtemplate.Engines() {
		versions, ok := dbtemplate.VersionsFor(engine)
		require.True(t, ok)
		for _, spec := range versions {
			t.Run(engine+"/"+spec.ID, func(t *testing.T) {
				tpl, err := dbtemplate.For(engine, spec.ID)
				require.NoError(t, err)
				settings := knobSampleSettings(tpl.Knobs())
				staticOnly, dynamicOnly := dbtemplate.SplitSettings(tpl.Knobs(), settings)
				if len(dynamicOnly) == 0 {
					t.Log("engine carries no dynamic knobs (redis/mongo first batch) — vacuum assertion")
				}
				materials, err := tpl.Materials("secretpw", settings)
				require.NoError(t, err, "static subset must render cleanly: %v", staticOnly)
				env, command := tpl.Workload(nil)
				// 值级缺席断言仅对样本值唯一的键生效（bool/enum 值域窄，
				// static 与 dynamic 同值时值面不可判归因——键面缺席是主断言）。
				valueCount := map[string]int{}
				for _, v := range settings {
					valueCount[v]++
				}
				for key, value := range dynamicOnly {
					for name, body := range materials {
						assert.NotContains(t, string(body), key,
							"%s/%s: dynamic key %q leaked into material %q", engine, spec.ID, key, name)
						if valueCount[value] == 1 {
							assert.NotContains(t, string(body), value,
								"%s/%s: dynamic value %q leaked into material %q", engine, spec.ID, value, name)
						}
					}
					for k, v := range env {
						assert.NotContains(t, k, key, "%s/%s: dynamic key %q leaked into Workload env name", engine, spec.ID, key)
						assert.NotContains(t, v, key, "%s/%s: dynamic key %q leaked into Workload env value", engine, spec.ID, key)
						if valueCount[value] == 1 {
							assert.NotContains(t, v, value, "%s/%s: dynamic value %q leaked into Workload env", engine, spec.ID, value)
						}
					}
					for _, arg := range command {
						assert.NotContains(t, arg, key, "%s/%s: dynamic key %q leaked into Workload command", engine, spec.ID, key)
						if valueCount[value] == 1 {
							assert.NotContains(t, arg, value, "%s/%s: dynamic value %q leaked into Workload command", engine, spec.ID, value)
						}
					}
				}
			})
		}
	}
}

// TestStaticSettingsRenderFaces：static 旋钮渲染金样（B2b，design §3.2
// static 车道 + §3.4 落点）——空 settings 与现状逐字节一致（零漂移由
// TestTemplateFaces 钉板，此处另钉 nil/空 map 等价）；非空 static 子集的
// 折叠产物逐字钉死（redis.conf 增行 / PG postgresql.conf 覆盖片段材料）；
// 敌意值在渲染安全闸 fail-closed（受理位被绕过时的纵深）。
func TestStaticSettingsRenderFaces(t *testing.T) {
	// redis：appendonly 旋钮化 + 增行面（键序 = requirepass、appendonly 恒
	// 渲染行、其余 static 键排序增行）。
	redis, err := dbtemplate.For("redis", "")
	require.NoError(t, err)
	base, err := redis.Materials("secretpw", nil)
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		dbtemplate.RedisConfFile: []byte("requirepass secretpw\nappendonly yes\n"),
	}, base, "empty settings keep the pre-knob bytes (zero drift)")
	empty, err := redis.Materials("secretpw", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, base, empty, "nil and empty settings render identically")

	conf, err := redis.Materials("secretpw", map[string]string{
		"appendonly": "false", "maxmemory": "256mb", "maxmemory-policy": "allkeys-lru",
	})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		dbtemplate.RedisConfFile: []byte(
			"requirepass secretpw\nappendonly no\nmaxmemory 256mb\nmaxmemory-policy allkeys-lru\n"),
	}, conf)
	partial, err := redis.Materials("secretpw", map[string]string{"maxmemory": "256mb"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		dbtemplate.RedisConfFile: []byte("requirepass secretpw\nappendonly yes\nmaxmemory 256mb\n"),
	}, partial, "unset knobs add no lines; appendonly stays at its default yes")

	// postgres：postgresql.conf 覆盖片段材料（B2d：值渲染为 GUC 原生字面
	// ——受理口径的 size 单位名不分大小写，PG 单位名大小写敏感，"512mb"
	// 进 GUC 即启动 FATAL；pgNativeValue 折字节后取最大整除单位）。
	pg, err := dbtemplate.For("postgres", "")
	require.NoError(t, err)
	pgBase, err := pg.Materials("secretpw", nil)
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")}, pgBase)
	files, err := pg.Materials("secretpw", map[string]string{
		"shared_buffers": "512mb", "max_connections": "200",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("secretpw"), files[dbtemplate.PasswordFile], "password file is untouched by knob folding")
	assert.Equal(t, []byte("max_connections = 200\nshared_buffers = 512MB\n"),
		files[dbtemplate.PostgreSQLConfFile], "conf fragment carries sorted `key = value` lines with native GUC values")
	// dynamic-only 子集：不产片段（work_mem 走 B2d 热应用面）。
	files, err = pg.Materials("secretpw", map[string]string{"work_mem": "64mb"})
	require.NoError(t, err)
	assert.NotContains(t, files, dbtemplate.PostgreSQLConfFile)
	assert.Len(t, files, 1)

	// pgvector：嵌入继承同面（GUC 原生字面：1gb → 1GB）。
	pgv, err := dbtemplate.For("pgvector", "")
	require.NoError(t, err)
	pgvFiles, err := pgv.Materials("secretpw", map[string]string{"shared_buffers": "1gb"})
	require.NoError(t, err)
	assert.Equal(t, []byte("shared_buffers = 1GB\n"), pgvFiles[dbtemplate.PostgreSQLConfFile])

	// mysql（全 dynamic）/mongo（零旋钮）：任意 settings 输出恒基线。
	my, err := dbtemplate.For("mysql", "")
	require.NoError(t, err)
	myBase, err := my.Materials("secretpw", nil)
	require.NoError(t, err)
	myOut, err := my.Materials("secretpw", map[string]string{
		"max_connections": "500", "innodb_buffer_pool_size": "1gb",
	})
	require.NoError(t, err)
	assert.Equal(t, myBase, myOut, "all-dynamic engine renders the baseline regardless of settings")
	mo, err := dbtemplate.For("mongo", "")
	require.NoError(t, err)
	moBase, err := mo.Materials("secretpw", nil)
	require.NoError(t, err)
	moOut, err := mo.Materials("secretpw", map[string]string{"anything": "1"})
	require.NoError(t, err)
	assert.Equal(t, moBase, moOut, "knob-less engine drops every settings key")

	// 渲染安全闸 fail-closed：受理位被绕过时的敌意值全拒（conf/INI 注入
	// 防线——validatePassword 同族精神）。
	for _, tc := range []struct {
		engine, key, value string
	}{
		{"redis", "maxmemory", "256mb\ninclude /evil"},
		{"redis", "maxmemory-policy", "noeviction\n"},
		{"redis", "appendonly", "yes"}, // bool 用户面值域是 true/false
		{"postgres", "shared_buffers", "512mb; rm -rf /"},
		{"postgres", "max_connections", "200 # comment"},
	} {
		tpl, err := dbtemplate.For(tc.engine, "")
		require.NoError(t, err)
		_, err = tpl.Materials("secretpw", map[string]string{tc.key: tc.value})
		assert.Error(t, err, "%s: hostile knob value must fail the render gate: %s=%q", tc.engine, tc.key, tc.value)
	}
}

// TestSplitSettings：喂入律帮手的分流语义——static/dynamic 各归车道、
// 白名单外键两侧都不落（受理位已拒，此处是纵深）。
func TestSplitSettings(t *testing.T) {
	pg, err := dbtemplate.For("postgres", "")
	require.NoError(t, err)
	knobs := pg.Knobs()
	staticOnly, dynamicOnly := dbtemplate.SplitSettings(knobs, map[string]string{
		"shared_buffers": "512mb", // static
		"work_mem":       "64mb",  // dynamic
		"requirepass":    "evil",  // 白名单外（owned 键同理）——两侧都不落
	})
	assert.Equal(t, map[string]string{"shared_buffers": "512mb"}, staticOnly)
	assert.Equal(t, map[string]string{"work_mem": "64mb"}, dynamicOnly)

	// 空 settings：两侧皆空。
	staticOnly, dynamicOnly = dbtemplate.SplitSettings(knobs, nil)
	assert.Empty(t, staticOnly)
	assert.Empty(t, dynamicOnly)

	// 零旋钮引擎（mongo）：一切键两侧都不落。
	mo, err := dbtemplate.For("mongo", "")
	require.NoError(t, err)
	staticOnly, dynamicOnly = dbtemplate.SplitSettings(mo.Knobs(), map[string]string{"x": "1"})
	assert.Empty(t, staticOnly)
	assert.Empty(t, dynamicOnly)

	// ValidateSettings 对同值域的受理位口径（内核单源经引擎真值域的验收
	// 锚样例）：白名单外拒/合法通过。
	require.NoError(t, enginetemplate.ValidateSettings(knobs, map[string]string{"max_connections": "200"}))
	err = enginetemplate.ValidateSettings(knobs, map[string]string{"max_connections": "20001"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[1, 10000]")
}

// TestWorkloadStaticOverrides：B2d static 车道命令覆写面（design §3.2/§4.3）。
// postgres：`docker-entrypoint.sh postgres -c key=value`（Workload.Command
// 覆盖镜像入口——必须显式经官方 entrypoint：裸 `postgres` 首参绕过
// entrypoint，空卷不 initdb、root 身份起进程即拒启；mongo 先例）；值渲染
// 为 GUC 原生字面（512mb → 512MB）；空 settings 的 command = nil（零漂移）。
// pgvector：同集覆写挂在既有 bash -c init 脚本尾部。
func TestWorkloadStaticOverrides(t *testing.T) {
	pg, err := dbtemplate.For("postgres", "")
	require.NoError(t, err)
	env, command := pg.Workload(nil)
	assert.Nil(t, command, "no static settings keep the image entrypoint/CMD (zero drift)")
	assert.NotContains(t, env, "COMMAND")

	env, command = pg.Workload(map[string]string{
		"shared_buffers": "512mb", "max_connections": "200", "work_mem": "64mb",
	})
	assert.NotContains(t, env, "COMMAND", "knob overrides ride argv, not env")
	assert.Equal(t, []string{
		"docker-entrypoint.sh", "postgres",
		"-c", "max_connections=200", "-c", "shared_buffers=512MB",
	}, command, "static knobs ride the entrypoint as -c overrides with native GUC values (keys sorted; work_mem is dynamic and structurally absent)")

	pgv, err := dbtemplate.For("pgvector", "")
	require.NoError(t, err)
	_, command = pgv.Workload(map[string]string{"shared_buffers": "1gb"})
	require.Len(t, command, 3)
	assert.Contains(t, command[2], "CREATE EXTENSION IF NOT EXISTS vector")
	assert.Contains(t, command[2], "exec docker-entrypoint.sh postgres -c shared_buffers=1GB")

	// mysql/mongo：无 static 旋钮——命令面不随 settings 变化。
	my, err := dbtemplate.For("mysql", "")
	require.NoError(t, err)
	baseEnv, baseCommand := my.Workload(map[string]string{"max_connections": "500"})
	_, dynCommand := my.Workload(nil)
	assert.Equal(t, baseCommand, dynCommand)
	assert.NotEmpty(t, baseEnv)
}

// TestPGNativeValue：GUC 原生字面渲染单位表（受理口径不分大小写 → PG
// 单位名大小写敏感）——最大整除单位、无整除退 B。
func TestPGNativeValue(t *testing.T) {
	pg, err := dbtemplate.For("postgres", "")
	require.NoError(t, err)
	// 经面内消费面间接钉单位表：Materials 片段（静态）与 ApplyDynamicSettings
	// 语句（dynamic）同源 pgNativeValue。
	renders := map[string]string{}
	mats, err := pg.Materials("secretpw", map[string]string{"shared_buffers": "512mb"})
	require.NoError(t, err)
	renders["512mb"] = strings.TrimSuffix(strings.TrimPrefix(string(mats[dbtemplate.PostgreSQLConfFile]), "shared_buffers = "), "\n")
	mats, err = pg.Materials("secretpw", map[string]string{"shared_buffers": "1gb"})
	require.NoError(t, err)
	renders["1gb"] = strings.TrimSuffix(strings.TrimPrefix(string(mats[dbtemplate.PostgreSQLConfFile]), "shared_buffers = "), "\n")
	for _, tc := range []struct{ in, want string }{
		{"512mb", "512MB"}, {"1gb", "1GB"},
	} {
		got := renders[tc.in]
		assert.Equal(t, tc.want, got, tc.in)
	}
	// 无整除 GB 留在 MB；小值退 kB/B。
	for _, tc := range []struct{ in, want string }{
		{"1536mb", "1536MB"}, {"64kb", "64kB"}, {"1b", "1B"}, {"2GB", "2GB"},
	} {
		spec, err := pg.ApplyDynamicSettings("h", "secretpw", map[string]string{"work_mem": tc.in})
		require.NoError(t, err, tc.in)
		stmt := ""
		for _, a := range spec.Argv {
			if strings.HasPrefix(a, "ALTER SYSTEM SET work_mem") {
				stmt = a
			}
		}
		assert.Contains(t, stmt, "work_mem = '"+tc.want+"'", tc.in)
	}
}

// TestApplyDynamicSettings：dynamic 车道热应用渲染金样（B2d，ADR-0095
// 决策 4 / design §3.2）——PG ALTER SYSTEM + pg_reload_conf（值 = GUC
// 原生字面；ON_ERROR_STOP=1 使部分失败即整体非零）；MySQL SET PERSIST
// （size 折字节整数——SET 右值不收单位后缀）；redis/mongo = 无 dynamic
// 面哨兵；static 键到达热应用面 = fail-closed（纵深核）。
func TestApplyDynamicSettings(t *testing.T) {
	pg, err := dbtemplate.For("postgres", "")
	require.NoError(t, err)
	spec, err := pg.ApplyDynamicSettings("db-01jd0db0000000000000000000", "secretpw", map[string]string{"work_mem": "64mb"})
	require.NoError(t, err)
	require.NotNil(t, spec.SecretFiles["database-backup-pgpass"], "credentials ride the pgpass secret file")
	assert.Equal(t, []string{
		"psql", "-h", "db-01jd0db0000000000000000000", "-p", "5432", "-U", "fleetly", "-d", "fleetly",
		"-v", "ON_ERROR_STOP=1", "--no-password",
		"-c", "ALTER SYSTEM SET work_mem = '64MB'",
		"-c", "SELECT pg_reload_conf()",
	}, spec.Argv, "argv is a pure array (zero shell); values render as native GUC literals; reload closes the batch")

	my, err := dbtemplate.For("mysql", "")
	require.NoError(t, err)
	spec, err = my.ApplyDynamicSettings("db-01jd0db0000000000000000000", "secretpw",
		map[string]string{"max_connections": "500", "innodb_buffer_pool_size": "1gb"})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"mysql", "--defaults-extra-file=/run/secrets/" + "database-backup-defaults",
		"-e", "SET PERSIST innodb_buffer_pool_size = 1073741824; SET PERSIST max_connections = 500",
	}, spec.Argv, "SET statements sort by key; size values fold to plain byte integers (SET takes no unit suffix)")

	// 无 dynamic 面：redis / mongo / 空集——哨兵（正常分流不可达，防御态）。
	for _, engine := range []string{"redis", "mongo"} {
		tpl, err := dbtemplate.For(engine, "")
		require.NoError(t, err)
		_, err = tpl.ApplyDynamicSettings("h", "secretpw", map[string]string{"any": "1"})
		assert.ErrorIs(t, err, dbtemplate.ErrNoDynamicFace, engine)
	}
	_, err = pg.ApplyDynamicSettings("h", "secretpw", nil)
	assert.ErrorIs(t, err, dbtemplate.ErrNoDynamicFace, "empty settings reach the sentinel (defensive)")

	// static 键/白名单外键到达热应用面 = fail-closed（纵深核）。
	_, err = pg.ApplyDynamicSettings("h", "secretpw", map[string]string{"shared_buffers": "512mb"})
	assert.ErrorContains(t, err, "only dynamic knobs reach the hot-apply face")
	_, err = pg.ApplyDynamicSettings("h", "secretpw", map[string]string{"listen_addresses": "*"})
	assert.ErrorContains(t, err, "not a registered knob")
}
