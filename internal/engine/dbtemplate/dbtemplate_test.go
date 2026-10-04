package dbtemplate_test

// 模板面的零漂移钉板（架构评审第二轮候选 1）：per-engine adapter 的完整
// 输出面逐字钉死——重构批的行为不变证明。F2.1 加 mysql/mongo 时本表
// 追加行即验收面；F2.2/F2.7 空槽断言届时随实现改写。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryValueDomain(t *testing.T) {
	assert.Equal(t, []string{"mongo", "mysql", "pgvector", "postgres", "redis"}, dbtemplate.Engines())

	_, ok := dbtemplate.For("oracle")
	assert.False(t, ok, "unregistered engine must not resolve")

	info, ok := dbtemplate.InfoFor("postgres")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "17-bookworm", Port: 5432}, info)

	info, ok = dbtemplate.InfoFor("mysql")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "8.4", Port: 3306}, info)

	info, ok = dbtemplate.InfoFor("mongo")
	require.True(t, ok)
	assert.Equal(t, dbtemplate.Info{Version: "8.0", Port: 27017}, info)
}

func TestTemplateFaces(t *testing.T) {
	for _, tc := range []struct {
		engine     string
		meta       dbtemplate.Info
		image      string
		dataTarget string
		env        map[string]string
		command    []string
		probe      []string
		materials  map[string][]byte
		connURL    string
	}{
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "postgres",
			meta:       dbtemplate.Info{Version: "17-bookworm", Port: 5432},
			image:      "postgres:17-bookworm",
			dataTarget: "/var/lib/postgresql",
			env: map[string]string{
				"POSTGRES_USER":          "fleetly",
				"POSTGRES_DB":            "fleetly",
				"POSTGRES_PASSWORD_FILE": "/run/secrets/" + dbtemplate.PasswordFile,
			},
			probe:     []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"},
			materials: map[string][]byte{dbtemplate.PasswordFile: []byte("secretpw")},
			connURL:   "postgresql://fleetly:secretpw@db-01j8:5432/fleetly",
		},
		{ //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
			engine:     "pgvector",
			meta:       dbtemplate.Info{Version: "0.8.6-pg17-bookworm", Port: 5432},
			image:      "pgvector/pgvector:0.8.6-pg17-bookworm",
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
			meta:       dbtemplate.Info{Version: "7.4", Port: 6379},
			image:      "redis:7.4",
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
			meta:       dbtemplate.Info{Version: "8.4", Port: 3306},
			image:      "mysql:8.4",
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
			meta:       dbtemplate.Info{Version: "8.0", Port: 27017},
			image:      "mongo:8.0",
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
		t.Run(tc.engine, func(t *testing.T) {
			tpl, ok := dbtemplate.For(tc.engine)
			require.True(t, ok)
			assert.Equal(t, tc.engine, tpl.Engine())
			assert.Equal(t, tc.meta, tpl.Meta())
			assert.Equal(t, tc.image, tpl.Image())
			assert.Equal(t, tc.dataTarget, tpl.DataTarget())
			env, command := tpl.Workload()
			assert.Equal(t, tc.env, env)
			assert.Equal(t, tc.command, command)
			assert.Equal(t, tc.probe, tpl.Probe())
			materials, err := tpl.Materials("secretpw")
			require.NoError(t, err)
			assert.Equal(t, tc.materials, materials)
			assert.Equal(t, tc.connURL, tpl.ConnURL("db-01j8", "secretpw"))
		})
	}
}

// TestBackupRestoreFaces：备份/恢复渲染的零漂移钉板（F2.2 槽启用，
// ADR-0039 决策 5——argv 逐字钉死；改动引擎工具面即红）。
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
				"cp /backup-input /seed/dump.rdb; " +
					"redis-server --dir /seed --port 6399 --daemonize no --appendonly no & " +
					"i=0; until redis-cli -p 6399 ping >/dev/null 2>&1; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
					"redis-cli -p 6399 BGREWRITEAOF; " +
					"i=0; until [ -f /seed/appendonlydir/appendonly.aof.manifest ]; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
					"redis-cli -p 6399 shutdown nosave; rm -f /seed/dump.rdb"},
			restoreMode: dbtemplate.RestorePreseed,
		},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			tpl, ok := dbtemplate.For(tc.engine)
			require.True(t, ok)
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
// 校验"的结构化文本等价物；mongo config JSON 往返）。
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
		t.Run(engine, func(t *testing.T) {
			tpl, ok := dbtemplate.For(engine)
			require.True(t, ok)
			for _, pw := range hostilePasswords {
				_, err := tpl.Backup("db-01j8", pw)
				assert.Error(t, err, "%s: hostile password must be rejected at the render gate: %q", engine, pw)
				_, err = tpl.Restore("db-01j8", pw)
				assert.Error(t, err, "%s: hostile password must be rejected at the render gate: %q", engine, pw)
				_, err = tpl.Materials(pw)
				assert.Error(t, err, "%s: hostile password must be rejected at the render gate: %q", engine, pw)
			}
		})
	}

	// 敌意 host：形状不变性——argv 元素数与干净 host 渲染完全一致（host
	// 整体落进单元素，不分裂、不解释；平台铸名 db-<id> 到不了这种形态，
	// 纵深）。mysql defaults 里的 host 同样只进值位（解析器断言在下）。
	hostileHosts := []string{"db-x; touch /tmp/pwned", "db-x$(pwned)", "db-x -c shell"}
	for _, engine := range []string{"postgres", "mysql", "mongo", "redis"} {
		tpl, _ := dbtemplate.For(engine)
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
	mysqlTpl, _ := dbtemplate.For("mysql")
	mysqlBackup, err := mysqlTpl.Backup("db-01j8", "secretpw2")
	require.NoError(t, err)
	defaults := string(mysqlBackup.SecretFiles["database-backup-defaults"])
	sections, keys := parseINI(t, defaults)
	assert.Equal(t, []string{"client"}, sections, "defaults file must carry exactly one [client] section")
	assert.Equal(t, map[string]string{"host": "db-01j8", "user": "fleetly", "password": "secretpw2"}, keys)

	// mongo config：JSON 往返（uri 键回读 = ConnURL 单源）。
	mongoTpl, _ := dbtemplate.For("mongo")
	backup, err := mongoTpl.Backup("db-01j8", "secretpw3")
	require.NoError(t, err)
	var cfg map[string]string
	require.NoError(t, json.Unmarshal(backup.SecretFiles["database-backup-config"], &cfg))
	assert.Equal(t, mongoTpl.ConnURL("db-01j8", "secretpw3"), cfg["uri"])

	// redis conf：行级断言（requirepass 单 token + appendonly 布尔行）。
	redisTpl, _ := dbtemplate.For("redis")
	rmats, err := redisTpl.Materials("secretpw4")
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

// TestReservedSlotsVacant：F2.7 digest 钉定的预留空槽现状（恒空）——
// F2.2 备份槽已启用（见 TestBackupRestoreFaces），空槽只剩 digest 面。
func TestReservedSlotsVacant(t *testing.T) {
	for _, engine := range dbtemplate.Engines() {
		tpl, ok := dbtemplate.For(engine)
		require.True(t, ok)
		assert.Empty(t, tpl.ImageDigest(), "%s: digest slot must stay vacant until F2.7", engine)
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
