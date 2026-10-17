package volume_test

// Volume 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 同
// Project 同名冲突归一 ErrAlreadyExists（部分唯一索引只锁活跃行）、
// Get/GetByName 活跃行口径、ListByProject 项目隔离 + 字典序 + 只含活跃行、
// SoftDelete tombstone 幂等（二次删除不炸、删后双读径 NotFound、同
// Project 同名可重建）、Pin 一次性钉住（已钉住幂等不改锚、缺行/已删行
// NotFound）。volume repo 的列表面是 ListByProject（无分页参数——项目内
// 卷量是小集合，钳位语义不适用本聚合，不硬造）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
)

const (
	projA = "01JD0PROJ0000000000000000A"
	projB = "01JD0PROJ0000000000000000B"
)

// tombstoneAt 直读行内 deleted_at（repo 读径对 tombstone 全盲，tombstone
// 值面只能下探行——断言二次软删不覆写首次时间戳）。
func tombstoneAt(t *testing.T, db *persistence.DB, id string) string {
	t.Helper()
	var deletedAt string
	require.NoError(t, db.Runner().QueryRowContext(
		context.Background(), `SELECT deleted_at FROM volumes WHERE id = ?`, id).Scan(&deletedAt))
	return deletedAt
}

// TestVolumeCreateGetListDeleteLifecycle：Create → 冲突归一 → 双读径 →
// 项目隔离列表 → 软删幂等 → 同名可重建。
func TestVolumeCreateGetListDeleteLifecycle(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	volumes := volumerepo.New(clock, db.Runner())

	// 首见落行：ID 缺省由 name+seq 铸、时间戳取假时钟。
	first := &volume.Volume{ProjectID: projA, Name: "data-vol"}
	require.NoError(t, volumes.Create(ctx, first))
	assert.Equal(t, "data-vol-1", first.ID)
	assert.Equal(t, int64(1), first.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), first.CreatedAt)
	assert.Equal(t, first.CreatedAt, first.UpdatedAt)
	assert.Empty(t, first.PinnedNodeID)
	assert.False(t, first.Deleted())

	// 同 Project 同名 → 唯一索引命中归一 ErrAlreadyExists。
	err := volumes.Create(ctx, &volume.Volume{ProjectID: projA, Name: "data-vol"})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"same-name create in the same project must normalize to ErrAlreadyExists")

	// 异 Project 同名合法（唯一轴是 project_id+name）。
	other := &volume.Volume{ProjectID: projB, Name: "data-vol"}
	require.NoError(t, volumes.Create(ctx, other))
	require.NoError(t, volumes.Create(ctx, &volume.Volume{ProjectID: projA, Name: "cache-vol"}))

	// Get/GetByName 活跃行口径；缺行 NotFound。
	got, err := volumes.Get(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, "data-vol", got.Name)
	assert.Equal(t, projA, got.ProjectID)
	byName, err := volumes.GetByName(ctx, projA, "data-vol")
	require.NoError(t, err)
	assert.Equal(t, first.ID, byName.ID)
	_, err = volumes.Get(ctx, "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = volumes.GetByName(ctx, projA, "nope")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// ListByProject：项目隔离 + name 字典序 + 只含活跃行。
	list, err := volumes.ListByProject(ctx, projA)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "cache-vol", list[0].Name)
	assert.Equal(t, "data-vol", list[1].Name)

	// 软删落 tombstone（deleted_at 取当前假时刻），双读径即盲。
	clock.Advance(30e9)
	require.NoError(t, volumes.SoftDelete(ctx, first.ID))
	firstTomb := tombstoneAt(t, db, first.ID)
	assert.Equal(t, state.FormatTime(clock.Now()), firstTomb)
	_, err = volumes.Get(ctx, first.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	_, err = volumes.GetByName(ctx, projA, "data-vol")
	assert.ErrorIs(t, err, state.ErrNotFound)
	list, err = volumes.ListByProject(ctx, projA)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "cache-vol", list[0].Name, "tombstoned rows stay out of the list")

	// 异 Project 同名行不受影响。
	_, err = volumes.GetByName(ctx, projB, "data-vol")
	require.NoError(t, err, "tombstone must not leak across projects")

	// 二次软删幂等（活跃行条件 WHERE 不再命中，不炸且不覆写时间戳）。
	clock.Advance(30e9)
	require.NoError(t, volumes.SoftDelete(ctx, first.ID))
	assert.Equal(t, firstTomb, tombstoneAt(t, db, first.ID),
		"second soft delete must keep the first tombstone timestamp")

	// 同 Project 同名可重建（tombstone 不占活跃行唯一轴）；GetByName 只见新行。
	reborn := &volume.Volume{ProjectID: projA, Name: "data-vol"}
	require.NoError(t, volumes.Create(ctx, reborn))
	assert.NotEqual(t, first.ID, reborn.ID, "recreated volume gets a fresh identity")
	again, err := volumes.GetByName(ctx, projA, "data-vol")
	require.NoError(t, err)
	assert.Equal(t, reborn.ID, again.ID)
	list, err = volumes.ListByProject(ctx, projA)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "data-vol", list[1].Name)
}

// TestVolumePinOneShot：钉住是一次性语义——已钉住时幂等返回不改锚（节点
// ID 永不复用）；缺行/已删行报 NotFound。
func TestVolumePinOneShot(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	volumes := volumerepo.New(clock, db.Runner())

	v := &volume.Volume{ProjectID: projA, Name: "data-vol"}
	require.NoError(t, volumes.Create(ctx, v))

	clock.Advance(10e9)
	require.NoError(t, volumes.Pin(ctx, projA, "data-vol", "node-1"))
	got, err := volumes.GetByName(ctx, projA, "data-vol")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.PinnedNodeID)
	assert.Equal(t, state.FormatTime(clock.Now()), got.UpdatedAt, "pin refreshes updated_at")

	// 已钉住：换节点号的二次 Pin 是 no-op（锚不可变）。
	require.NoError(t, volumes.Pin(ctx, projA, "data-vol", "node-2"))
	got, err = volumes.GetByName(ctx, projA, "data-vol")
	require.NoError(t, err)
	assert.Equal(t, "node-1", got.PinnedNodeID, "pin is one-shot: an existing anchor must not change")

	// 缺行 NotFound。
	err = volumes.Pin(ctx, projA, "nope", "node-1")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 已删行 NotFound（活跃行口径）。
	require.NoError(t, volumes.SoftDelete(ctx, v.ID))
	err = volumes.Pin(ctx, projA, "data-vol", "node-3")
	assert.ErrorIs(t, err, state.ErrNotFound)
}
