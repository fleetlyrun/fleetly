package apitest

// volumes purge 服务端链路（B7，ADR-0094 决策 5 / design §2.4 升级正典③）：
// 预览（清单 + 容量 + 关联库披露 + 铸票）/ 确认三腿（REQUIRED/MISMATCH/
// 兑换）/ 引用拒（活跃同名库 = E_CONFLICT）/ 执行（物理删除 + tombstone +
// 事件 + 审计）/ 幂等重入（行缺席 = E_NOT_FOUND；物理面幂等由 fake 载体集
// 承载——purge 后键即删）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

// purgeFixture 建项目 + 卷 + 假物理载体（容量 4096），返回 (h, ctx, 项目,
// 卷行, volumes 客户端)。
func purgeFixture(t *testing.T) (*Harness, context.Context, string, *structurev1.Volume, structurev1.VolumesServiceClient) {
	t.Helper()
	h := NewManual(t)
	ctx := h.Ctx()
	proj := h.CreateProject(t, "purge").GetId()
	vols := structurev1.NewVolumesServiceClient(h.Conn)
	created, err := vols.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: proj, Name: "pgshop"})
	require.NoError(t, err)
	vol := created.GetVolume()
	h.Runtime.SetVolumeCarriers(map[string]int64{vol.GetId(): 4096})
	return h, ctx, proj, vol, vols
}

// TestPurgeVolumeConfirmLegs 确认执法三腿：裸调用 REQUIRED；伪造票
// MISMATCH；预览票兑换成功。
func TestPurgeVolumeConfirmLegs(t *testing.T) {
	h, ctx, _, vol, vols := purgeFixture(t)

	// 裸调用：E_CONFIRMATION_REQUIRED（ConfirmableVerbs 拦截器）。
	_, err := vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{Id: vol.GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err)) // apperr 信封码经 status 面
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")

	// 伪造票：MISMATCH。
	_, err = vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: "forged-ticket"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_MISMATCH")

	// 预览 → 带票兑换。
	preview, err := vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "pgshop", preview.GetName())
	assert.Equal(t, vol.GetId(), preview.GetCarrierKey())
	assert.True(t, preview.GetUsageKnown(), "the fake carrier reports live usage")
	assert.Equal(t, int64(4096), preview.GetUsageBytes())
	assert.NotEmpty(t, preview.GetTicket())
	assert.Empty(t, preview.GetDatabaseId(), "no same-name database row exists - standalone volume")

	resp, err := vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
	})
	require.NoError(t, err)
	assert.Equal(t, "pgshop", resp.GetName())
	assert.Equal(t, vol.GetId(), resp.GetCarrierKey())
	assert.True(t, resp.GetUsageKnown())
	assert.Equal(t, int64(4096), resp.GetPurgedBytes())

	// 物理面：fake 载体集键已删 + 调用流水含现行/历史双键。
	assert.NotContains(t, h.Runtime.VolumePurgeCalls(), vol.GetId()+"-missing")
	assert.Contains(t, h.Runtime.VolumePurgeCalls(), vol.GetId())
	assert.Contains(t, h.Runtime.VolumePurgeCalls(), "pgshop", "the legacy name-keyed carrier form is purged too")

	// 行清：再删 = E_NOT_FOUND（tombstone 后不存在口径）。
	_, err = vols.DeleteVolume(ctx, &structurev1.DeleteVolumeRequest{Id: vol.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// 同票二次兑换：MISMATCH（tombstone 腿——幂等降级窗的预期）。
	_, err = vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_MISMATCH")
}

// TestPurgeVolumeReferencedConflict 引用拒：活跃同名 Database 挂靠卷 =
// E_CONFLICT（销毁动作的引用面必须先解——预览与执行同谓词）。
func TestPurgeVolumeReferencedConflict(t *testing.T) {
	h, ctx, proj, vol, vols := purgeFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	_, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{ProjectId: proj, Name: "pgshop", Engine: "postgres"})
	require.NoError(t, err)

	_, err = vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still referenced")
	assert.Contains(t, err.Error(), "database pgshop")

	_, err = vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still referenced")
}

// TestPurgeVolumeOwningDatabaseDisclosure 关联库披露：删库后（升级正典③
// 形态）预览携带 database_id/name（tombstone 行经 GetByNameAnyState 可见）。
func TestPurgeVolumeOwningDatabaseDisclosure(t *testing.T) {
	h, ctx, proj, vol, vols := purgeFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{ProjectId: proj, Name: "pgshop", Engine: "postgres"})
	require.NoError(t, err)
	dbID := created.GetDatabase().GetId()
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: dbID})
	require.NoError(t, err)

	preview, err := vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.NoError(t, err)
	assert.Equal(t, dbID, preview.GetDatabaseId())
	assert.Equal(t, "pgshop", preview.GetDatabaseName())

	// skip 通道（--yes）：物理删 + tombstone + 审计 proof=skipped。
	_, err = vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	require.NoError(t, err)
}

// TestPurgeVolumeTicketStaleOnWorldDrift 票面内容绑定：预览后世界漂移
// （容量变化）→ 旧票 MISMATCH（RequestDigest 绑清单本体）。
func TestPurgeVolumeTicketStaleOnWorldDrift(t *testing.T) {
	h, ctx, _, vol, vols := purgeFixture(t)
	preview, err := vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.NoError(t, err)

	// 世界漂移：物理卷容量变化（daemon 重算了用量）。
	h.Runtime.SetVolumeCarriers(map[string]int64{vol.GetId(): 8192})

	_, err = vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_MISMATCH")

	// 重预览取新票 → 兑换成功（漂移后的容量进回执）。
	fresh, err := vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.NoError(t, err)
	resp, err := vols.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
		Id: vol.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: fresh.GetTicket()},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(8192), resp.GetPurgedBytes())
}

// TestPurgeVolumeUsageUnknownHonestAbsence 容量不可得的诚实缺席：载体不
// 在（键缺席）→ usage_known=false、usage_bytes=0——披露降级不猜。
func TestPurgeVolumeUsageUnknownHonestAbsence(t *testing.T) {
	h, ctx, _, vol, vols := purgeFixture(t)
	h.Runtime.SetVolumeCarriers(nil)
	preview, err := vols.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: vol.GetId()})
	require.NoError(t, err)
	assert.False(t, preview.GetUsageKnown())
	assert.Zero(t, preview.GetUsageBytes())
}
