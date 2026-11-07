package apitest_test

// BucketStoresService e2e（B5，ADR-0096）：创建受理（engine 值域/零旋钮拒
// settings/保留前缀）、凭证 Secret 铸造（bucketstore:<name>）与永不回显、收敛
// 到 Bucket Store 域载体（fleetly-bkt 载体名/bkt-<id> 寻址由 Provider 翻译，
// 此处断言 NamespaceRef 轴 + root AK/SK 经 Workload env 下发〔ADR-0014 A.2
// 追认〕+ 指纹脱敏）、Update 确认面（resources 唯一可变轴）、轮换=滚动
// （Generation 推进）、删除收口（载体拆除 + 卷与 Secret 残留）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestBucketStoreLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "objstore").GetId()
	buckets := structurev1.NewBucketStoresServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)

	// 值域外 engine → InvalidArgument（列合法值）；零旋钮族任何 settings 键拒。
	_, err := buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "assets", Engine: "rustfs",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "assets", Engine: "silo", Settings: map[string]string{"anything": "1"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "silo v1 carries no knobs: any settings key is refused")

	created, err := buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "assets", Engine: "silo",
	})
	require.NoError(t, err)
	row := created.GetBucketStore()
	assert.NotEmpty(t, row.GetId())
	assert.Equal(t, "silo", row.GetEngine())
	assert.Equal(t, "2026-10", row.GetVersion(), "silo single version is the default")
	assert.Equal(t, "bucketstore:"+"assets", row.GetCredentialsRef())
	assert.Equal(t, "bkt-"+strings.ToLower(row.GetId()), row.GetHost())
	assert.Equal(t, int32(9000), row.GetPort())
	assert.Equal(t, "pending", row.GetStatus())

	// 凭证 Secret 已铸造（指纹面；值永不回显）。
	secretList, err := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, secretList.GetSecrets(), 1)
	assert.Equal(t, "bucketstore:"+"assets", secretList.GetSecrets()[0].GetName())
	assert.NotEmpty(t, secretList.GetSecrets()[0].GetFingerprint())

	// 同名活跃行 → AlreadyExists；保留前缀 PutSecret → Conflict。
	_, err = buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "assets", Engine: "silo",
	})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{
		ProjectId: projectID, Name: "bucketstore:" + "assets", Value: "hijack",
	})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "E_CONFLICT maps to FailedPrecondition on gRPC")

	// 收敛：Bucket Store 轴 NamespaceRef + 挂项目网 + root AK/SK 经 env
	// 下发（ADR-0014 A.2 追认——env 名为 S3 系兼容形态）+ 指纹脱敏（行
	// spec_fingerprint 不含明文 SK）。
	h.Drive(ctx)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{
		Team: "default", Project: projectID, BucketStore: row.GetId(), BucketStoreName: "assets",
	}, last.NS)
	w := last.Spec["silo"]
	require.NotNil(t, w)
	assert.Equal(t, []string{"default"}, w.Networks)
	assert.Equal(t, []string{"silo", "server", "/data"}, w.Command)
	assert.Equal(t, "fleetly", w.Env["MINIO_ROOT_"+"USER"], "platform access key rides the carrier env")
	firstSK := w.Env["MINIO_ROOT_"+"PASSWORD"]
	require.NotEmpty(t, firstSK, "minted secret key rides the carrier env")
	assert.Empty(t, last.Materials.SecretFiles, "silo has no file material face")
	var fingerprint string
	require.NoError(t, h.DB.Runner().QueryRowContext(ctx,
		`SELECT spec_fingerprint FROM bucket_stores WHERE id = ?`, row.GetId()).Scan(&fingerprint))
	assert.True(t, strings.HasPrefix(fingerprint, "sha256:"), "projection fingerprint is digested")
	assert.NotContains(t, fingerprint, firstSK, "the secret key must never land in the row")
	firstGen := last.Gen

	// 观测 running → 状态列推进；卷行自愈补建（名 = 实例名）。
	h.Runtime.ReportRunning(row.GetId(), last.Gen)
	h.Drive(ctx)
	got, err := buckets.GetBucketStore(ctx, &structurev1.GetBucketStoreRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "running", got.GetBucketStore().GetStatus())
	volList, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, volList.GetVolumes(), 1)
	assert.Equal(t, "assets", volList.GetVolumes()[0].GetName())

	// Resolve 按名寻址（ADR-0092）。
	resolved, err := buckets.ResolveBucketStore(ctx, &structurev1.ResolveBucketStoreRequest{Project: "objstore", Name: "assets"})
	require.NoError(t, err)
	assert.Equal(t, row.GetId(), resolved.GetBucketStore().GetId())

	// Update 期望态（resources 唯一可变轴；ConfirmableVerbs 强制集）：无票
	// 无 skip → E_CONFIRMATION_REQUIRED；不可变轴拒；settings 键拒（零旋
	// 钮）；预演 = resources diff + replace + 铸票；带票直发落行；票单用途。
	_, err = buckets.UpdateBucketStore(ctx, &structurev1.UpdateBucketStoreRequest{
		BucketStoreId: row.GetId(),
		Resources:     &structurev1.DatabaseResources{CpuMillis: 500},
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "update without confirmation is refused")
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")
	_, err = buckets.UpdateBucketStore(ctx, &structurev1.UpdateBucketStoreRequest{
		BucketStoreId: row.GetId(), Engine: "silo",
		Confirmation: &deliveryv1.Confirmation{Skip: true},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "engine is immutable")
	_, err = buckets.UpdateBucketStore(ctx, &structurev1.UpdateBucketStoreRequest{
		BucketStoreId: row.GetId(),
		Settings:      &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"anything": "1"}},
		Confirmation:  &deliveryv1.Confirmation{Skip: true},
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "zero-knob family refuses any settings key")
	updateReq := &structurev1.UpdateBucketStoreRequest{
		BucketStoreId: row.GetId(),
		Resources:     &structurev1.DatabaseResources{CpuMillis: 500, MemoryMb: 512},
	}
	preview, err := buckets.PreviewUpdateBucketStore(ctx, &structurev1.PreviewUpdateBucketStoreRequest{Update: updateReq})
	require.NoError(t, err)
	assert.Empty(t, preview.GetSettingsChanges(), "zero knobs: no settings diff")
	assert.Empty(t, preview.GetDynamicKeys())
	assert.Empty(t, preview.GetStaticKeys())
	assert.Equal(t, "replace", preview.GetRollout(), "a resources change rolls the carrier")
	assert.Equal(t, int32(500), preview.GetResourcesTo().GetCpuMillis())
	require.NotEmpty(t, preview.GetTicket())
	updateReq.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
	updated, err := buckets.UpdateBucketStore(ctx, updateReq)
	require.NoError(t, err)
	assert.Equal(t, int32(500), updated.GetBucketStore().GetResources().GetCpuMillis())
	assert.Equal(t, int32(512), updated.GetBucketStore().GetResources().GetMemoryMb())
	assert.Empty(t, updated.GetStaticApplied())
	_, err = buckets.UpdateBucketStore(ctx, updateReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_MISMATCH")

	// Rotate 声明式轮换（在服前提）= 滚动：新 SK 进 env → Generation 推进。
	rotated, err := buckets.RotateBucketStorePassword(ctx, &structurev1.RotateBucketStorePasswordRequest{BucketStoreId: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "bucketstore:"+"assets", rotated.GetBucketStore().GetCredentialsRef(), "credential ref is stable; the value was replaced under it")
	h.Drive(ctx)
	calls = h.Runtime.Calls()
	last = calls[len(calls)-1]
	require.Equal(t, row.GetId(), last.NS.BucketStore)
	w = last.Spec["silo"]
	require.NotNil(t, w)
	assert.NotEqual(t, firstSK, w.Env["MINIO_ROOT_"+"PASSWORD"], "rotation re-delivers a fresh secret key through the carrier env")
	assert.Greater(t, uint64(last.Gen), uint64(firstGen), "rotation = roll: the projection fingerprint moved so the generation advanced")

	// 删除收口：载体拆除 + tombstone；卷与凭证 Secret 残留。
	_, err = buckets.DeleteBucketStore(ctx, &structurev1.DeleteBucketStoreRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
		Team: "default", Project: projectID, BucketStore: row.GetId(), BucketStoreName: "assets",
	})
	_, err = buckets.GetBucketStore(ctx, &structurev1.GetBucketStoreRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = buckets.DeleteBucketStore(ctx, &structurev1.DeleteBucketStoreRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err), "再删 = 不存在（幂等口径）")
	secretList, err = secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, secretList.GetSecrets(), 1, "credential secret is a project material and survives the delete")
}

// TestBucketStoreQuota 钉 per-Project 配额（ADR-0096 决策 8：bucket_stores 4
// ——descriptor Quota 真源）。
func TestBucketStoreQuota(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "quota").GetId()
	buckets := structurev1.NewBucketStoresServiceClient(h.Conn)
	for _, name := range []string{"a", "b", "c", "d"} {
		_, err := buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
			ProjectId: projectID, Name: name, Engine: "silo",
		})
		require.NoError(t, err, "bucket store %s within quota", name)
	}
	_, err := buckets.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "e", Engine: "silo",
	})
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	list, err := buckets.ListBucketStores(ctx, &structurev1.ListBucketStoresRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, list.GetBucketStores(), 4)
}
