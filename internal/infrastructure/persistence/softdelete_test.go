package persistence_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 软删列不变量（架构 §6）：凡带 deleted_at 的表一律 TEXT NOT NULL
// 且默认空串。空串 = 活跃，非空 = UTC RFC3339 tombstone；读径以
// 空串判活跃，partial unique index 也只收该谓词为真的行——
// 可空列会让 NULL 行既不可见也不入唯一索引（等同已删）。00046 归一
// 七张早期表后，本测试防未来新表/重建表回退到可空形态。
func TestSoftDeleteColumnsAreNotNullWithEmptyDefault(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()

	rows, err := db.Reader().QueryContext(ctx, `
		SELECT m.name, p."notnull", p.dflt_value
		FROM sqlite_master AS m
		JOIN pragma_table_info(m.name) AS p
		WHERE m.type = 'table'
		  AND m.name NOT LIKE 'sqlite_%'
		  AND p.name = 'deleted_at'
		ORDER BY m.name`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	seen := make(map[string]bool)
	for rows.Next() {
		var table string
		var notNull int
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&table, &notNull, &dflt))
		seen[table] = true
		assert.Equal(t, 1, notNull, "%s.deleted_at must be NOT NULL", table)
		assert.Equal(t, "''", dflt.String, "%s.deleted_at must default to ''", table)
	}
	require.NoError(t, rows.Err())

	// 七张历史表是 00046 的归一对象；列被误删即红（新表由上面的全表
	// 扫描自动纳入，不需要逐表登记）。
	for _, table := range []string{
		"projects", "apps", "routes", "databases", "secrets", "volumes", "networks",
	} {
		assert.True(t, seen[table], "%s must keep a deleted_at column", table)
	}
}
