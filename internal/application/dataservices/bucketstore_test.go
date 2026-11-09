package dataservices

// bucketstore 类型收敛步单测（B5，ADR-0096 决策 1/2/4/5）：投影〔root AK/SK 经
// Workload env 下发〕+ 指纹脱敏 + 轮换=滚动（gen 推进）+ 卷实例标记门多态
// （异源拒/孤儿过+同源幂等/跨类型互撞拒/受理前置两态——pubsub 类型 ⑥~⑨ 同构）
// + FLEETLY_BKT_* 注入。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// bucketstore 夹具常量（pubsub 类型夹具同源形态）。
const (
	tBucketID = "01jdh0b5b50000000000000000"
	tBucketNm = "assets"
	// tBucketSK 是夹具 secret key（hex 形态；G101：非真凭证的测试夹具）。
	tBucketSK = "0123456789abcdef0123456789abcdef"
)

// bucketMarkerOf 铸 bucketstore 类型标记 JSON（G101 纪律：拆串拼接）。
func bucketMarkerOf(t *testing.T, id string) []byte {
	t.Helper()
	body, err := databasetemplateRender(dataservicetemplate.MintInstanceMarker(id, bucketstoretemplate.InstanceKind, time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	return []byte(body)
}

// originBucketFixture 在既有夹具上补一枚已收敛 bucketstore 行（首拍 claim
// 铸 kind:bucketstore 标记，返回卷行。凭证 k=v 串经 RenderConnMaterial
// 铸（G101：字面量不带 secret 整串）。
func originBucketFixture(t *testing.T) (*fixture, *volume.Volume) {
	t.Helper()
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-01jd0db0000000000000000000:5432/fleetly", false)
	ctx := context.Background()
	secName := contract.BKTCredentialSecretName(tBucketNm)
	mat := bucketstoretemplate.RenderConnMaterial(bucketstoretemplate.ConnMaterial{
		Endpoint: contract.BucketStoreDNSName(tBucketID) + ":9000", AccessKey: bucketstoretemplate.SiloPlatformAccessKey, SecretKey: tBucketSK,
	})
	ct, err := fx.cipher.Seal([]byte(mat))
	require.NoError(t, err)
	require.NoError(t, fx.stores.Secrets.Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000003", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(mat)),
	}))
	require.NoError(t, fx.stores.BucketStores.Create(ctx, &bucketstore.BucketStore{
		ID: tBucketID, ProjectID: tProjectID, Name: tBucketNm,
		Engine: "silo", Version: "2026-10", CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	require.NotEmpty(t, fx.rt.calls(), "fixture bucket store must converge before the scenarios")
	vol, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tBucketNm)
	require.NoError(t, err)
	return fx, vol
}

// lastBucketCall 返回最近一次 bucketstore 轴 Ensure 调用。
func lastBucketCall(t *testing.T, fx *fixture) ensureCall {
	t.Helper()
	calls := fx.rt.calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].NS.BucketStore == tBucketID {
			return calls[i]
		}
	}
	t.Fatalf("no bucket store ensure call recorded")
	return ensureCall{}
}

// TestBucketStoreProjectionCarriesCredentialEnv 钉投影面：NamespaceRef
// bucketstore 轴、silo server /data 命令、root AK/SK 经 env 下发（ADR-0014
// A.2 追认）、材料面恒空、指纹脱敏（行 spec_fingerprint 是 sha256 摘要、不
// 含明文 SK）、DNS 寻址 bkt-<id>。
func TestBucketStoreProjectionCarriesCredentialEnv(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	call := lastBucketCall(t, fx)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, BucketStore: tBucketID, BucketStoreName: tBucketNm}, call.NS)
	require.Len(t, call.Spec, 1)
	w := call.Spec["silo"]
	assert.Equal(t, tBucketID, w.ID)
	assert.Equal(t, "silo", w.Process)
	assert.Equal(t, []string{"silo", "server", "/data"}, w.Command)
	assert.Equal(t, bucketstoretemplate.SiloPlatformAccessKey, w.Env["MINIO_ROOT_"+"USER"])
	assert.Equal(t, tBucketSK, w.Env["MINIO_ROOT_"+"PASSWORD"])
	assert.Equal(t, []capability.Address{{Name: "bkt-" + tBucketID}}, w.Addressing)
	assert.Equal(t, int32(9000), w.Ports[0].Port)
	assert.Equal(t, "/data", w.Volumes[0].Target)
	assert.Empty(t, call.Materials.SecretFiles, "silo has no file material face")

	row, err := fx.stores.BucketStores.Get(ctx, tBucketID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(row.SpecFingerprint, "sha256:"), "fingerprint is digested")
	assert.NotContains(t, row.SpecFingerprint, tBucketSK, "the secret key must never land in the row")
	assert.Equal(t, uint64(1), row.Generation)
}

// TestBucketStoreRotationIsARoll 钉轮换=滚动：Secret 重写 → 下拍以新 SK 重
// 投影 → 指纹变 → Generation 推进（database/pubsub 类型的材料面轮换不推进
// gen——本类型 env 形态的结构性差异）。
func TestBucketStoreRotationIsARoll(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	before := lastBucketCall(t, fx)

	require.NoError(t, fx.d.RotateBucketStorePassword(ctx, tBucketID))
	assert.Equal(t, 1, fx.bktKicks, "rotation kicks the kind ring")
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	after := lastBucketCall(t, fx)
	assert.NotEqual(t, tBucketSK, after.Spec["silo"].Env["MINIO_ROOT_"+"PASSWORD"], "the carrier env carries the rotated key")
	assert.Greater(t, uint64(after.Gen), uint64(before.Gen), "rotation advances the generation (stop-first roll)")
	for _, req := range fx.ut.requests() {
		assert.False(t, strings.HasPrefix(req.ID, "rotate-"), "declarative rotation runs no rotate utility container (only the origin-gate marker probe), got %s", req.ID)
	}

	// 同拍重放：签名一致 → fresh 短路，零新 Ensure。
	n := len(fx.rt.calls())
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	assert.Len(t, fx.rt.calls(), n, "an unchanged row replays as a memo hit")
}

// TestBucketStoreInjectEnv 钉 FLEETLY_BKT_<NAME>_{ENDPOINT|ACCESS_KEY|
// SECRET_KEY} 注入（ADR-0096 决策 5；引用缺失精确失败）。
func TestBucketStoreInjectEnv(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	spec := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{Name: "web", BucketstoreRefs: []string{tBucketNm}}}}
	ws := []capability.Workload{{Process: "web"}}
	require.NoError(t, fx.d.InjectBucketStoreEnv(ctx, spec, ws, tProjectID))
	assert.Equal(t, "bkt-"+tBucketID+":9000", ws[0].Env["FLEETLY_BKT_ASSETS_ENDPOINT"])
	assert.Equal(t, bucketstoretemplate.SiloPlatformAccessKey, ws[0].Env["FLEETLY_BKT_ASSETS_"+"ACCESS_KEY"])
	assert.Equal(t, tBucketSK, ws[0].Env["FLEETLY_BKT_ASSETS_"+"SECRET_KEY"])

	missing := &specv1.AppSpec{Processes: []*specv1.ProcessSpec{{Name: "web", BucketstoreRefs: []string{"nope"}}}}
	err := fx.d.InjectBucketStoreEnv(ctx, missing, []capability.Workload{{Process: "web"}}, tProjectID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucketstore_refs")
}

// TestBucketStoreUpdateResourcesRolls 钉 Update 单车道：resources 落行 +
// Kick；零旋钮类型 settings 键经 SplitSettings 丢弃（受理位 ValidateSettings
// 已拒，应用模块面不二次执法）。
func TestBucketStoreUpdateResourcesRolls(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	cpu := int32(250)
	res, err := fx.d.UpdateBucketStore(ctx, tBucketID, BucketStoreUpdateRequest{Resources: &ResourceUpdate{CPUMillis: &cpu}})
	require.NoError(t, err)
	assert.Empty(t, res.StaticRolled)
	assert.Equal(t, 1, fx.bktKicks)
	row, err := fx.stores.BucketStores.Get(ctx, tBucketID)
	require.NoError(t, err)
	require.NotNil(t, row.CPUMillis)
	assert.Equal(t, int32(250), *row.CPUMillis)
	before := lastBucketCall(t, fx)
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	after := lastBucketCall(t, fx)
	assert.Greater(t, uint64(after.Gen), uint64(before.Gen), "a resources change rolls the carrier")
	assert.Equal(t, int64(250), after.Spec["silo"].Resources.CPUMillis)
}

// ---- 卷实例标记门多态面（pubsub ⑥~⑨ 同构） ----

// ⑩ bucketstore 异源拒：同类型异名标记 → 不 Ensure（载体保持现状）。
func TestBucketStoreVolumeOriginGateForeignMarkerRefuses(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(bucketMarkerOf(t, "01jdh0b5b59999999999999999"))
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)

	assert.Len(t, fx.rt.calls(), before, "a foreign bucket store instance marker must refuse the ensure")
	reqs := fx.ut.requests()
	require.NotEmpty(t, reqs)
	last := reqs[len(reqs)-1]
	require.NotNil(t, last.Volume)
	assert.Equal(t, dataservicetemplate.SeedMountPoint, last.Volume.Target)
	assert.Equal(t, dataservicetemplate.KindBucketStore, bucketstoretemplate.InstanceKind, "bucketstore marker kind word must be the descriptor-registered word")
}

// ⑪ bucketstore 孤儿卷过 + 同源幂等。
func TestBucketStoreVolumeOriginGateOrphanAndSameMarkerPass(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()

	fx.ut.setVolumeOriginMarker(nil)
	require.NoError(t, fx.stores.Networks.Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000005", ProjectID: tProjectID, Name: "edge",
	}))
	before := len(fx.rt.calls())
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	assert.Greater(t, len(fx.rt.calls()), before, "an unmarked (orphan) volume is claimed and the ensure proceeds")

	fx.ut.setVolumeOriginMarker(bucketMarkerOf(t, tBucketID))
	require.NoError(t, fx.stores.Networks.Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000006", ProjectID: tProjectID, Name: "mesh",
	}))
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	assert.Greater(t, len(fx.rt.calls()), before+1, "a matching bucket store marker lets the ensure proceed")
}

// ⑫ 跨类型互撞拒：pubsub 类型标记占卷 + bucketstore 同名重建 → 拒。
func TestBucketStoreVolumeOriginGateCrossKindRefuses(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(pubSubMarkerOf(t, tBucketID)) // kind=pubsub，同 entity_id 跨类型
	fx.d.Step(ctx, dataservicetemplate.KindBucketStore)
	assert.Len(t, fx.rt.calls(), before, "a pubsub-kind marker on a bucket store volume is a foreign origin and must refuse the ensure")
}

// ⑬ bucketstore 受理面前置读两态。
func TestBucketStoreVolumeOriginForCreate(t *testing.T) {
	fx, _ := originBucketFixture(t)
	ctx := context.Background()

	fx.ut.setVolumeOriginMarker(nil)
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindBucketStore, tProjectID, tBucketNm, "silo"))

	fx.ut.setVolumeOriginMarker(bucketMarkerOf(t, "01jdh0b5b58888888888888888"))
	err := fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindBucketStore, tProjectID, tBucketNm, "silo")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVolumeForeignOrigin)

	n := len(fx.ut.requests())
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindBucketStore, tProjectID, "fresh-assets", "silo"))
	assert.Len(t, fx.ut.requests(), n, "a fresh volume name has no retained face to probe")
}
