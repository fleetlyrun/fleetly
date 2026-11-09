package dataservicetemplate

// kind descriptor 注册表白盒（ADR-0096 决策 1 / B2e）：形状闸、幂等重登、
// 不等值重登 panic、排序稳定、缺席判据、三枚类型词面在册形态。实例化注册表
// 独立构造——包级默认实例由各类型模板包初始化期登记，测试不碰它。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKindRegistryRegisterLookupKinds(t *testing.T) {
	var reg kindRegistry
	_, ok := reg.lookup(KindDatabase)
	assert.False(t, ok, "empty registry answers absent")
	assert.Empty(t, reg.kinds())

	db := reg.register(KindDescriptor{Kind: KindDatabase, Quota: 16, EventPrefix: "database"})
	bkt := reg.register(KindDescriptor{Kind: KindBucketStore, Quota: 4, EventPrefix: "bucket_store"})
	got, ok := reg.lookup(KindDatabase)
	require.True(t, ok)
	assert.Equal(t, db, got)
	// 排序稳定（按 Kind 字典序：bucketstore < database）。
	assert.Equal(t, []KindDescriptor{bkt, db}, reg.kinds())
	_, ok = reg.lookup(KindPubSub)
	assert.False(t, ok, "unregistered kind stays absent (mechanism-first placeholder judgment)")
}

func TestKindRegistryIdempotentAndConflict(t *testing.T) {
	var reg kindRegistry
	d := KindDescriptor{Kind: KindPubSub, Quota: 8, EventPrefix: "pubsub"}
	assert.Equal(t, d, reg.register(d))
	assert.Equal(t, d, reg.register(d), "byte-equal re-registration is idempotent")
	assert.Len(t, reg.kinds(), 1)
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: KindPubSub, Quota: 9, EventPrefix: "pubsub"}) },
		"a kind has exactly one descriptor — a differing re-registration is a programming error")
}

func TestKindDescriptorShapeGate(t *testing.T) {
	var reg kindRegistry
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: "Database", Quota: 1, EventPrefix: "database"}) }, "kind word form")
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: "", Quota: 1, EventPrefix: "database"}) }, "empty kind")
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: KindDatabase, Quota: 0, EventPrefix: "database"}) }, "zero quota")
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: KindDatabase, Quota: 1, EventPrefix: "bucket-store"}) }, "event prefix: hyphen is outside the eventcode left-segment form")
	assert.Panics(t, func() { reg.register(KindDescriptor{Kind: KindDatabase, Quota: 1, EventPrefix: ""}) }, "empty event prefix")
	assert.Empty(t, reg.kinds(), "rejected descriptors never land")
}

func TestKindWordsAreValidMarkerKinds(t *testing.T) {
	// 三枚类型词面同时是卷实例标记 kind 轴的合法值（instance.go 判据同源）。
	for _, f := range []string{KindDatabase, KindPubSub, KindBucketStore} {
		assert.True(t, validKind(f), f)
		assert.True(t, validEventPrefix(f), f)
	}
	assert.True(t, validEventPrefix("bucket_store"), "ADR-0096 decision 8 event prefix for bucket stores")
}

// KindSupportsBackup 门判据（B4a，红队 A9）：登记且 Backup:true 才放行——缺席
// 类型（机制先行占位）与显式 false 类型都关门。独立注册表构造（白盒实例面，
// 不碰包级默认实例——database 类型由 databasetemplate 登记的事实在 databasetemplate
// kind_test 钉板）。
func TestKindSupportsBackupGate(t *testing.T) {
	var reg kindRegistry
	assert.False(t, reg.backsUp(KindDatabase), "an absent kind never backs up (mechanism-first placeholder)")
	assert.False(t, reg.backsUp("nonsense"))

	withBackup := KindDescriptor{Kind: KindPubSub, Quota: 8, EventPrefix: "pubsub", DataCapabilities: DataCapabilities{Backup: true}}
	reg.register(withBackup)
	withoutBackup := KindDescriptor{Kind: KindBucketStore, Quota: 4, EventPrefix: "bucket_store"}
	reg.register(withoutBackup)

	assert.True(t, reg.backsUp(KindPubSub), "a registered kind with Backup:true passes the gate")
	assert.False(t, reg.backsUp(KindBucketStore), "a registered kind with Backup:false stays closed (kafka-style honesty)")
}
