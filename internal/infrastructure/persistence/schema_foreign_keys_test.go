package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// fkSpec 是一条期望外键（列 → 父表.父列 + 删除动作）。
type fkSpec struct {
	parent    string
	parentCol string
	onDelete  string
}

// expectedForeignKeys 是核心资源图的 schema 不变量真源（00047 落地）：
// 表 → 子列 → 父表/父列/ON DELETE。identity 面既有外键保持 NO ACTION；
// 00047 新增图外键一律 RESTRICT。空表集合（change_freezes/github_apps 等）
// 显式登记为空，防未来误加会拒绝合法空串行的外键。
var expectedForeignKeys = map[string]map[string]fkSpec{
	"alert_rules": {
		"app_id": {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
	},
	"app_hooks": {
		"app_id": {parent: "apps", parentCol: "id", onDelete: "NO ACTION"},
	},
	"app_variables": {
		"app_id": {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
	},
	"apps": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"backups": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"browse_sessions": {
		"project_id":  {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
		"database_id": {parent: "databases", parentCol: "id", onDelete: "RESTRICT"},
	},
	"bucket_resources": {
		"bucket_store_id": {parent: "bucket_stores", parentCol: "id", onDelete: "RESTRICT"},
	},
	"bucket_stores": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"builds": {
		"app_id": {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
	},
	"configs": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"databases": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"deployments": {
		"app_id":      {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
		"to_revision": {parent: "revisions", parentCol: "id", onDelete: "RESTRICT"},
	},
	"hook_deliveries": {
		"app_id": {parent: "app_hooks", parentCol: "app_id", onDelete: "NO ACTION"},
	},
	"invitations": {
		"team_id": {parent: "teams", parentCol: "id", onDelete: "NO ACTION"},
		"role_id": {parent: "roles", parentCol: "id", onDelete: "NO ACTION"},
	},
	"memberships": {
		"user_id": {parent: "users", parentCol: "id", onDelete: "NO ACTION"},
		"team_id": {parent: "teams", parentCol: "id", onDelete: "NO ACTION"},
		"role_id": {parent: "roles", parentCol: "id", onDelete: "NO ACTION"},
	},
	"network_peers": {
		"network_id":      {parent: "networks", parentCol: "id", onDelete: "RESTRICT"},
		"peer_project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"networks": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"projects": {
		"team_id": {parent: "teams", parentCol: "id", onDelete: "RESTRICT"},
	},
	"pubsubs": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"revisions": {
		"app_id": {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
	},
	"routes": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
		"app_id":     {parent: "apps", parentCol: "id", onDelete: "RESTRICT"},
	},
	"runs": {
		"task_id":    {parent: "tasks", parentCol: "id", onDelete: "RESTRICT"},
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"schedules": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"secrets": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"shared_variables": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"source_uploads": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"streams": {
		"pubsub_id": {parent: "pubsubs", parentCol: "id", onDelete: "RESTRICT"},
	},
	"tasks": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	"tokens": {
		"team_id": {parent: "teams", parentCol: "id", onDelete: "NO ACTION"},
		"user_id": {parent: "users", parentCol: "id", onDelete: "NO ACTION"},
		"role_id": {parent: "roles", parentCol: "id", onDelete: "NO ACTION"},
	},
	"topics": {
		"pubsub_id": {parent: "pubsubs", parentCol: "id", onDelete: "RESTRICT"},
	},
	"volumes": {
		"project_id": {parent: "projects", parentCol: "id", onDelete: "RESTRICT"},
	},
	// 显式空集：这些表按 schema 事实不得有外键（多态/空串语义/平台级行）。
	"audit":                  {},
	"change_freezes":         {},
	"github_apps":            {},
	"idempotency_records":    {},
	"log_cursors":            {},
	"node_relay_credentials": {},
	"nodes":                  {},
	"notification_channels":  {},
	"outbox":                 {},
	"roles":                  {},
	"seq_counters":           {},
	"teams":                  {},
	"template_catalog":       {},
	"users":                  {},
}

// TestForeignKeysAreDeclared 是 schema 不变量测试：逐表读
// pragma_foreign_key_list，断言全库外键集合与期望完全一致（不多不少），
// 00047 新增图外键一律 ON DELETE RESTRICT（无级联）。不是 AST 守卫——
// 行为面孤儿拒插由 migrations 前向测试与各 repo 测试承载。
func TestForeignKeysAreDeclared(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()

	tables, err := db.Reader().QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'
		ORDER BY name`)
	require.NoError(t, err)
	defer func() { _ = tables.Close() }()
	var names []string
	for tables.Next() {
		var name string
		require.NoError(t, tables.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, tables.Err())

	seen := make(map[string]bool, len(names))
	for _, table := range names {
		seen[table] = true
		want := expectedForeignKeys[table]
		got := map[string]fkSpec{}
		rows, err := db.Reader().QueryContext(ctx,
			`SELECT "table", "from", "to", on_delete FROM pragma_foreign_key_list(?)`, table)
		require.NoError(t, err, "pragma_foreign_key_list(%s)", table)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var parent, from, to, onDelete string
			require.NoError(t, rows.Scan(&parent, &from, &to, &onDelete))
			got[from] = fkSpec{parent: parent, parentCol: to, onDelete: onDelete}
		}
		require.NoError(t, rows.Err())
		assert.Equal(t, want, got, "foreign keys of %s must match the schema invariant", table)
	}

	// 期望表全部在场（防表名漂移让断言空转）。
	for table := range expectedForeignKeys {
		assert.True(t, seen[table], "expected table %s must exist", table)
	}
}
