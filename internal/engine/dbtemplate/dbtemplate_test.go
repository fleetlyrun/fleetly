package dbtemplate_test

// 模板面的零漂移钉板（架构评审第二轮候选 1）：per-engine adapter 的完整
// 输出面逐字钉死——重构批的行为不变证明。F2.1 加 mysql/mongo 时本表
// 追加行即验收面；F2.2/F2.7 空槽断言届时随实现改写。

import (
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
			dataTarget: "/var/lib/postgresql/data",
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
			dataTarget: "/var/lib/postgresql/data",
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
			assert.Equal(t, tc.materials, tpl.Materials("secretpw"))
			assert.Equal(t, tc.connURL, tpl.ConnURL("db-01j8", "secretpw"))
		})
	}
}

// TestReservedSlotsVacant：F2.2 备份命令与 F2.7 digest 钉定的预留空槽
// 现状（恒空）——接口一次定形、实现随批落。F2.2/F2.7 落地时本断言改写
// 为真实行为断言，空槽期不再有第二处半态。
func TestReservedSlotsVacant(t *testing.T) {
	for _, engine := range dbtemplate.Engines() {
		tpl, ok := dbtemplate.For(engine)
		require.True(t, ok)
		assert.Empty(t, tpl.BackupCommand(), "%s: backup command slot must stay vacant until F2.2", engine)
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
