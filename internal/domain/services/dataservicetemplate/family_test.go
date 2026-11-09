package dataservicetemplate

// family descriptor 注册表白盒（ADR-0096 决策 1 / B2e）：形状闸、幂等重登、
// 不等值重登 panic、排序稳定、缺席判据、三枚家族词面在册形态。实例化注册表
// 独立构造——包级默认实例由各家族模板包初始化期登记，测试不碰它。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFamilyRegistryRegisterLookupFamilies(t *testing.T) {
	var reg familyRegistry
	_, ok := reg.lookup(FamilyDatabase)
	assert.False(t, ok, "empty registry answers absent")
	assert.Empty(t, reg.families())

	db := reg.register(FamilyDescriptor{Family: FamilyDatabase, Quota: 16, EventFamily: "database"})
	bkt := reg.register(FamilyDescriptor{Family: FamilyBucketStore, Quota: 4, EventFamily: "bucket_store"})
	got, ok := reg.lookup(FamilyDatabase)
	require.True(t, ok)
	assert.Equal(t, db, got)
	// 排序稳定（按 Family 字典序：bucketstore < database）。
	assert.Equal(t, []FamilyDescriptor{bkt, db}, reg.families())
	_, ok = reg.lookup(FamilyPubSub)
	assert.False(t, ok, "unregistered family stays absent (mechanism-first placeholder judgment)")
}

func TestFamilyRegistryIdempotentAndConflict(t *testing.T) {
	var reg familyRegistry
	d := FamilyDescriptor{Family: FamilyPubSub, Quota: 8, EventFamily: "pubsub"}
	assert.Equal(t, d, reg.register(d))
	assert.Equal(t, d, reg.register(d), "byte-equal re-registration is idempotent")
	assert.Len(t, reg.families(), 1)
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: FamilyPubSub, Quota: 9, EventFamily: "pubsub"}) },
		"a family has exactly one descriptor — a differing re-registration is a programming error")
}

func TestFamilyDescriptorShapeGate(t *testing.T) {
	var reg familyRegistry
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: "Database", Quota: 1, EventFamily: "database"}) }, "family word form")
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: "", Quota: 1, EventFamily: "database"}) }, "empty family")
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: FamilyDatabase, Quota: 0, EventFamily: "database"}) }, "zero quota")
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: FamilyDatabase, Quota: 1, EventFamily: "bucket-store"}) }, "event family: hyphen is outside the eventcode left-segment form")
	assert.Panics(t, func() { reg.register(FamilyDescriptor{Family: FamilyDatabase, Quota: 1, EventFamily: ""}) }, "empty event family")
	assert.Empty(t, reg.families(), "rejected descriptors never land")
}

func TestFamilyWordsAreValidMarkerFamilies(t *testing.T) {
	// 三枚家族词面同时是卷实例标记 family 轴的合法值（instance.go 判据同源）。
	for _, f := range []string{FamilyDatabase, FamilyPubSub, FamilyBucketStore} {
		assert.True(t, validFamily(f), f)
		assert.True(t, validEventFamily(f), f)
	}
	assert.True(t, validEventFamily("bucket_store"), "ADR-0096 decision 8 event family for bucket stores")
}

// FamilyBacksUp 门判据（B4a，红队 A9）：登记且 Backup:true 才放行——缺席
// 族（机制先行占位）与显式 false 族都关门。独立注册表构造（白盒实例面，
// 不碰包级默认实例——database 族由 databasetemplate 登记的事实在 databasetemplate
// family_test 钉板）。
func TestFamilyBacksUpGate(t *testing.T) {
	var reg familyRegistry
	assert.False(t, reg.backsUp(FamilyDatabase), "an absent family never backs up (mechanism-first placeholder)")
	assert.False(t, reg.backsUp("nonsense"))

	withBackup := FamilyDescriptor{Family: FamilyPubSub, Quota: 8, EventFamily: "pubsub", DataCapabilities: DataCapabilities{Backup: true}}
	reg.register(withBackup)
	withoutBackup := FamilyDescriptor{Family: FamilyBucketStore, Quota: 4, EventFamily: "bucket_store"}
	reg.register(withoutBackup)

	assert.True(t, reg.backsUp(FamilyPubSub), "a registered family with Backup:true passes the gate")
	assert.False(t, reg.backsUp(FamilyBucketStore), "a registered family with Backup:false stays closed (kafka-style honesty)")
}
