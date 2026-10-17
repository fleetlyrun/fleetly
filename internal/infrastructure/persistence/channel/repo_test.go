package channel_test

// Channel 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Create 落行
// 铸造面（ID = name+seq 派生、age 信封载荷 BLOB 读回、enabled 布尔）、重
// 名冲突归一 ErrAlreadyExists（name 唯一列把守）、Get 双态、ListEnabled/
// ListAll 派发面与管理面分立、RecordDelivery 失败回写与成功清位（
// last_failure 诊断面 + updated_at 推进）、Delete 缺行 ErrNotFound。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
	channelrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/channel"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// TestChannelCreateConflictAndListFaces：落行 + 冲突归一 + 双读径 + 双列表。
func TestChannelCreateConflictAndListFaces(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	channels := channelrepo.New(clock, db.Runner())

	// 首见落行：ID 由 name+seq 铸，age 信封载荷原样落 BLOB。
	envelope := []byte("age:encrypted-envelope-bytes")
	c1 := &channel.Channel{
		Name: "slack", Kind: channel.KindWebhook,
		ConfigCiphertext: envelope, Enabled: true,
	}
	require.NoError(t, channels.Create(ctx, c1))
	assert.Equal(t, "slack-1", c1.ID, "default id joins name and seq")
	assert.Equal(t, int64(1), c1.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), c1.CreatedAt)
	assert.Equal(t, c1.CreatedAt, c1.UpdatedAt)
	assert.Empty(t, c1.LastFailure, "fresh channel carries no delivery failure")

	// 重名冲突归一 ErrAlreadyExists（name UNIQUE 把守）。
	err := channels.Create(ctx, &channel.Channel{
		Name: "slack", Kind: channel.KindTelegram, ConfigCiphertext: envelope, Enabled: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, state.ErrAlreadyExists,
		"duplicate channel name must normalize to ErrAlreadyExists")

	// 禁用行 + 异名行。
	c2 := &channel.Channel{
		Name: "ops-bot", Kind: channel.KindTelegram,
		ConfigCiphertext: []byte("age:second-envelope"), Enabled: false,
	}
	require.NoError(t, channels.Create(ctx, c2))

	// Get：命中（BLOB/布尔/时间戳读回）与缺行 NotFound。
	got, err := channels.Get(ctx, c1.ID)
	require.NoError(t, err)
	assert.Equal(t, "slack", got.Name)
	assert.Equal(t, channel.KindWebhook, got.Kind)
	assert.Equal(t, envelope, got.ConfigCiphertext, "age envelope round-trips as BLOB")
	assert.True(t, got.Enabled)
	_, err = channels.Get(ctx, "missing-id")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// ListEnabled 只含启用行；ListAll 全量（均 seq 稳定序）。
	enabled, err := channels.ListEnabled(ctx)
	require.NoError(t, err)
	require.Len(t, enabled, 1)
	assert.Equal(t, "slack", enabled[0].Name)
	all, err := channels.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "ops-bot", all[1].Name)
}

// TestChannelRecordDeliveryAndDelete：派发结果回写面与删面。
func TestChannelRecordDeliveryAndDelete(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	channels := channelrepo.New(clock, db.Runner())

	c := &channel.Channel{
		Name: "slack", Kind: channel.KindWebhook,
		ConfigCiphertext: []byte("age:env"), Enabled: true,
	}
	require.NoError(t, channels.Create(ctx, c))

	// 失败回写：诊断文案入 last_failure，updated_at 推进。
	clock.Advance(10e9)
	require.NoError(t, channels.RecordDelivery(ctx, c.ID, "dial timeout"))
	got, err := channels.Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, "dial timeout", got.LastFailure)
	assert.Equal(t, state.FormatTime(clock.Now()), got.UpdatedAt, "recorded delivery refreshes updated_at")

	// 成功清位：空 err 文本把 last_failure 拉回空串。
	clock.Advance(10e9)
	require.NoError(t, channels.RecordDelivery(ctx, c.ID, ""))
	got, err = channels.Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Empty(t, got.LastFailure, "successful delivery clears the failure diagnostic")

	// Delete 删行即净；缺行 ErrNotFound。
	require.NoError(t, channels.Delete(ctx, c.ID))
	_, err = channels.Get(ctx, c.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
	err = channels.Delete(ctx, c.ID)
	assert.ErrorIs(t, err, state.ErrNotFound)
}
