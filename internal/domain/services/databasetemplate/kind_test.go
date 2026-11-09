package databasetemplate_test

// 数据库类型 descriptor 登记钉板（ADR-0096 决策 1 / B2e）：本包初始化期向
// dataservicetemplate 注册表登记的 Kind 必须可经 LookupKind 原值取回（声明
// 与登记一体），配额 16 与事件码前缀 "database" 是既有口径（ADR-0029 决策 10 /
// ADR-0017 配额族；事件四件对账在 internal/guards kinddescriptor 守卫）。

import (
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabaseKindDescriptorRegistered(t *testing.T) {
	got, ok := dataservicetemplate.LookupKind(dataservicetemplate.KindDatabase)
	require.True(t, ok, "databasetemplate must register the database kind at package init")
	assert.Equal(t, databasetemplate.Kind, got)
	assert.Equal(t, dataservicetemplate.KindDatabase, got.Kind)
	assert.Equal(t, 16, got.Quota, "per-Project database quota is the pre-B2e value (ADR-0029 decision 10)")
	assert.Equal(t, "database", got.EventPrefix)
	// B4a 能力位（红队 A9）：database 类型全员 backup——schedulePass 门与
	// TriggerBackup 受理位的真值源；kafka 类 false 值随 B6a 在引擎轴下沉
	// 分叉，本断言双向保鲜。
	assert.True(t, got.DataCapabilities.Backup, "the database kind carries the data backup face (B4a)")
	assert.True(t, dataservicetemplate.KindSupportsBackup(dataservicetemplate.KindDatabase))
	assert.Equal(t, dataservicetemplate.KindDatabase, databasetemplate.InstanceKind, "marker kind word and descriptor key are one word")
}
