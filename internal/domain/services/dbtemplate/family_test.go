package dbtemplate_test

// 数据库家族 descriptor 登记钉板（ADR-0096 决策 1 / B2e）：本包初始化期向
// enginetemplate 注册表登记的 Family 必须可经 LookupFamily 原值取回（声明
// 与登记一体），配额 16 与事件码族 "database" 是既有口径（ADR-0029 决策 10 /
// ADR-0017 配额族；事件四件对账在 internal/guards familydescriptor 守卫）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabaseFamilyDescriptorRegistered(t *testing.T) {
	got, ok := enginetemplate.LookupFamily(enginetemplate.FamilyDatabase)
	require.True(t, ok, "dbtemplate must register the database family at package init")
	assert.Equal(t, dbtemplate.Family, got)
	assert.Equal(t, enginetemplate.FamilyDatabase, got.Family)
	assert.Equal(t, 16, got.Quota, "per-Project database quota is the pre-B2e value (ADR-0029 decision 10)")
	assert.Equal(t, "database", got.EventFamily)
	assert.Equal(t, enginetemplate.FamilyDatabase, dbtemplate.InstanceFamily, "marker family word and descriptor key are one word")
}
