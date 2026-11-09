package dataservices

// 库卷异源 fail-closed 白盒（ADR-0097 决策 4 验收锚）：ensure 链标记门
// 两态（同源过/异源拒且不 Ensure、幂等重放零额外探测）+ 受理面前置读
// 两态（无标记存量卷过/异源标记拒）+ 坏标记形态拒 + 无 utility 面
// fail-closed。B4c 起 pubsub 类型同构面（实体中性化内核——孤儿/异源两态 +
// 跨类型互撞拒 + 受理前置读两态）。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// originFixture 在既有夹具上补一颗已收敛库（首拍 claim 铸记），返回卷行。
func originFixture(t *testing.T, engineName, url string) (*fixture, *volume.Volume) {
	t.Helper()
	fx := newFixture(t, engineName, url, false)
	ctx := context.Background()
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	require.NotEmpty(t, fx.rt.calls(), "fixture database must converge before the origin scenarios")
	row, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, err)
	return fx, row
}

// markerOf 铸一颗标记 JSON（id 注入面）。
func markerOf(t *testing.T, id string) []byte {
	t.Helper()
	body, err := databasetemplateRender(databasetemplate.MintInstanceMarker(id, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	return []byte(body)
}

// databasetemplateRender 是 renderInstanceMarker 的测试可达面（包内非导出——
// 同包白盒直访的等价小件）。
func databasetemplateRender(m databasetemplate.InstanceMarker) (string, error) {
	argv, err := databasetemplate.InstanceMarkerClaimArgv("/unused", "/var/lib/postgresql/data", m)
	if err != nil {
		return "", err
	}
	// claim argv[2] = "if [ -e ... ]; then cat ...; else printf %s '<json>' > ...";
	// 提取内嵌 JSON。
	s := argv[2]
	const lead = "printf %s '"
	i := indexOf(s, lead)
	if i < 0 {
		return "", fmt.Errorf("no payload")
	}
	rest := s[i+len(lead):]
	j := indexOf(rest, "' >")
	if j < 0 {
		return "", fmt.Errorf("no payload end")
	}
	return rest[:j], nil
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ① 异源拒：标记 database_id ≠ 当前库行 → 不 Ensure（载体保持现状）；
// 卷行/挂靠关系不动（拒绝不产生副作用）。
func TestVolumeOriginGateForeignMarkerRefuses(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, _ := originFixture(t, "postgres", url)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(markerOf(t, "01jd0fake00000000000000000"))
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)

	assert.Len(t, fx.rt.calls(), before, "a foreign instance marker must refuse the ensure")
	// 最近的 utility 探测携带挂卷锚（卷行 id + /seed 挂点）。
	reqs := fx.ut.requests()
	require.NotEmpty(t, reqs)
	last := reqs[len(reqs)-1]
	require.NotNil(t, last.Volume)
	assert.Equal(t, databasetemplate.SeedMountPoint, last.Volume.Target)
}

// ② 同源过 + 幂等：标记与当前库行一致 → Ensure 照常；重复收敛不再发
// 探测容器（ensure cache 短路先于标记门——探测只在真实确保拍发生）。
func TestVolumeOriginGateSameMarkerPasses(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, _ := originFixture(t, "postgres", url)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(markerOf(t, tDatabaseID))
	// 指纹变化（新增项目网）强制一次真实确保拍——同源标记照常放行。
	require.NoError(t, fx.stores.Networks.Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000002", ProjectID: tProjectID, Name: "edge",
	}))
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	assert.Greater(t, len(fx.rt.calls()), before, "a matching marker lets the ensure proceed")

	fx.d.Step(ctx, dataservicetemplate.KindDatabase) // ensure cache 短路拍：零探测
	probes := 0
	for _, r := range fx.ut.requests() {
		if r.Volume != nil {
			probes++
		}
	}
	assert.Equal(t, 2, probes, "exactly two origin probes total (first claim + one real ensure); the memo-short-circuited tick adds none")
}

// ③ 坏标记形态拒：卷内非标记内容占据文件名 = 异源事实。
func TestVolumeOriginGateUnreadableMarkerRefuses(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, _ := originFixture(t, "postgres", url)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker([]byte("REDIS0011-rdb-bytes"))
	fx.d.Step(ctx, dataservicetemplate.KindDatabase)
	assert.Len(t, fx.rt.calls(), before, "an unreadable marker is a foreign-origin fact and must refuse the ensure")
}

// ④ 受理面前置读两态：无标记（预建/无标记存量卷）过；异源标记拒
// （ErrVolumeForeignOrigin 判别锚）；全新卷名零探测。
func TestVolumeOriginForCreate(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, volRow := originFixture(t, "postgres", url)
	ctx := context.Background()

	// 无标记（探测回读空）→ 过。
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindDatabase, tProjectID, tDatabaseNm, "postgres"))

	// 异源标记 → 拒（哨兵可判别）。
	fx.ut.setVolumeOriginMarker(markerOf(t, "01jd0prev00000000000000000"))
	err := fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindDatabase, tProjectID, tDatabaseNm, "postgres")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVolumeForeignOrigin)

	// 全新卷名 → 零 utility 探测、直过。
	n := len(fx.ut.requests())
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindDatabase, tProjectID, "fresh-name", "postgres"))
	assert.Len(t, fx.ut.requests(), n, "a fresh volume name has no retained face to probe")
	_ = volRow
}

// ⑤ 无 utility 面 fail-closed：标记门是确保前置，面缺席 = 拒绝确保
// （Database 域要求 RuntimeUtility——rotate/preseed 同类型判据）。
func TestVolumeOriginGateRequiresUtilityFace(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, _ := originFixture(t, "postgres", url)
	ctx := context.Background()
	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	tpl, err := databasetemplate.For(row.Engine, "")
	require.NoError(t, err)
	vol, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, err)

	saved := fx.d.utility
	fx.d.utility = nil
	err = fx.d.ensureVolumeOrigin(ctx, row, tpl, vol)
	fx.d.utility = saved
	require.Error(t, err, "the origin gate must fail closed when the utility face is absent")
	assert.Contains(t, err.Error(), "utility face")
}

// ---- pubsub 类型同构面（B4c 实体中性化验收锚——照 database 类型多态形态） ----

// pubsub 夹具常量（database 类型夹具同源形态）。
const (
	tPubSubID = "01jdh0p5b50000000000000000"
	tPubSubNm = "bus"
)

// pubSubMarkerOf 铸 pubsub 类型标记 JSON（G101 纪律：拆串拼接）。
func pubSubMarkerOf(t *testing.T, id string) []byte {
	t.Helper()
	body, err := databasetemplateRender(dataservicetemplate.MintInstanceMarker(id, pubsubtemplate.InstanceKind, time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	return []byte(body)
}

// originPubSubFixture 在既有夹具上补一枚已收敛 pubsub 行（首拍 claim 铸
// kind:pubsub 标记，返回卷行。凭证 URL 拆串拼接（G101：字面量不带
// password 形态整串）。
func originPubSubFixture(t *testing.T) (*fixture, *volume.Volume) {
	t.Helper()
	// G101 纪律（B4b 验收先例）：nats 凭证 URL 拼接构造，源字面量不含
	// password 完整形态。
	connURL := "nats://fleetly:" + "secretpw@" + "pub-" + tPubSubID + ":4222"
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-01jd0db0000000000000000000:5432/fleetly", false)
	ctx := context.Background()
	secName := contract.PubCredentialSecretName(tPubSubNm)
	ct, err := fx.cipher.Seal([]byte(connURL))
	require.NoError(t, err)
	require.NoError(t, fx.stores.Secrets.Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000002", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connURL)),
	}))
	require.NoError(t, fx.stores.PubSubs.Create(ctx, &pubsub.PubSub{
		ID: tPubSubID, ProjectID: tProjectID, Name: tPubSubNm,
		Engine: "nats", Version: "2.11", CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	require.NotEmpty(t, fx.rt.calls(), "fixture pubsub must converge before the origin scenarios")
	vol, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tPubSubNm)
	require.NoError(t, err)
	return fx, vol
}

// ⑥ pubsub 异源拒：同类型异名标记 → 不 Ensure（载体保持现状）——照 ① 形态。
func TestPubSubVolumeOriginGateForeignMarkerRefuses(t *testing.T) {
	fx, _ := originPubSubFixture(t)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(pubSubMarkerOf(t, "01jdh0p5b59999999999999999"))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)

	assert.Len(t, fx.rt.calls(), before, "a foreign pubsub instance marker must refuse the ensure")
	reqs := fx.ut.requests()
	require.NotEmpty(t, reqs)
	last := reqs[len(reqs)-1]
	require.NotNil(t, last.Volume)
	assert.Equal(t, dataservicetemplate.SeedMountPoint, last.Volume.Target)
	assert.Equal(t, dataservicetemplate.KindPubSub, pubsubtemplate.InstanceKind, "pubsub marker kind word must be the descriptor-registered word")
}

// ⑦ pubsub 孤儿卷过 + 同源幂等：无标记（预建/存量）= 铸记收编照常 Ensure；
// 同源标记重确保照常。孤儿两态合测（孤儿即「缺席即铸」半边）。
func TestPubSubVolumeOriginGateOrphanAndSameMarkerPass(t *testing.T) {
	fx, _ := originPubSubFixture(t)
	ctx := context.Background()

	// 孤儿卷（标记回读空）+ 指纹变化（新增项目网）强制一次真实确保拍——
	// 缺席即铸，照常放行。
	fx.ut.setVolumeOriginMarker(nil)
	require.NoError(t, fx.stores.Networks.Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000003", ProjectID: tProjectID, Name: "edge",
	}))
	before := len(fx.rt.calls())
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Greater(t, len(fx.rt.calls()), before, "an unmarked (orphan) volume is claimed and the ensure proceeds")

	// 同源标记：照常 Ensure（claim 形态内嵌标记与行同源）——指纹再变一次
	// （第二张项目网）强制真实确保拍。
	fx.ut.setVolumeOriginMarker(pubSubMarkerOf(t, tPubSubID))
	require.NoError(t, fx.stores.Networks.Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000004", ProjectID: tProjectID, Name: "mesh",
	}))
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Greater(t, len(fx.rt.calls()), before+1, "a matching pubsub marker lets the ensure proceed")
}

// ⑧ 跨类型互撞拒：database 类型标记占卷 + pubsub 同名重建 → 拒（B-击三：
// 新类型不带标记门出厂 = database 类型刚修完的异源缺陷复发）。
func TestPubSubVolumeOriginGateCrossKindRefuses(t *testing.T) {
	fx, _ := originPubSubFixture(t)
	ctx := context.Background()
	before := len(fx.rt.calls())

	fx.ut.setVolumeOriginMarker(markerOf(t, tPubSubID)) // kind=database，同 entity_id 跨类型
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)
	assert.Len(t, fx.rt.calls(), before, "a database-kind marker on a pubsub volume is a foreign origin and must refuse the ensure")
}

// ⑨ pubsub 受理面前置读两态：无标记过；异源标记拒（ErrVolumeForeignOrigin
// 判别锚）；全新卷名零探测。
func TestPubSubVolumeOriginForCreate(t *testing.T) {
	fx, _ := originPubSubFixture(t)
	ctx := context.Background()

	// 无标记（探测回读空）→ 过。
	fx.ut.setVolumeOriginMarker(nil)
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindPubSub, tProjectID, tPubSubNm, "nats"))

	// 异源标记 → 拒（哨兵可判别）。
	fx.ut.setVolumeOriginMarker(pubSubMarkerOf(t, "01jdh0p5b58888888888888888"))
	err := fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindPubSub, tProjectID, tPubSubNm, "nats")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVolumeForeignOrigin)

	// 全新卷名 → 零 utility 探测、直过。
	n := len(fx.ut.requests())
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, dataservicetemplate.KindPubSub, tProjectID, "fresh-bus", "nats"))
	assert.Len(t, fx.ut.requests(), n, "a fresh volume name has no retained face to probe")
}

// ⑩ ToolUser 通道（B8-fix）：kafka 卷源标记门的 claim 探测以 root 运行、
// argv 携带镜像数据目录作属主参照（chown --reference 播种——非 root 引擎
// 用户对 root 属主新卷根的写障修复）；nats/postgres 等缺省声明面保持空
// （镜像自带用户，零行为变化）。
func TestVolumeOriginGateToolUserChannel(t *testing.T) {
	// kafka 行（k=v 方言连接材料——PasswordFromURL 双方言回读）。
	connMaterial := "bootstrap=pub-01jdh0p5b60000000000000000:9092;" +
		"mechanism=SCRAM-SHA-256;user=fleetly;password=secretpw"
	fx := newFixture(t, "postgres", "postgresql://fleetly:x@db-01jd0db0000000000000000000:5432/fleetly", false)
	ctx := context.Background()
	secName := contract.PubCredentialSecretName("kbus")
	ct, err := fx.cipher.Seal([]byte(connMaterial))
	require.NoError(t, err)
	require.NoError(t, fx.stores.Secrets.Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000003", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connMaterial)),
	}))
	require.NoError(t, fx.stores.PubSubs.Create(ctx, &pubsub.PubSub{
		ID: "01jdh0p5b60000000000000000", ProjectID: tProjectID, Name: "kbus",
		Engine: "kafka", CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	fx.ut.setVolumeOriginMarker(nil) // 缺席即铸：claim 形态全量走真链
	fx.d.Step(ctx, dataservicetemplate.KindPubSub)

	reqs := fx.ut.requests()
	var claim *capability.UtilityRequest
	for i := range reqs {
		if reqs[i].Volume != nil && strings.HasPrefix(reqs[i].ID, "volorigin-") {
			claim = &reqs[i]
		}
	}
	require.NotNil(t, claim, "the kafka ensure chain must run the origin claim probe")
	assert.Equal(t, "root", claim.User, "kafka tools declare root (ToolUser face): appuser cannot write the root-owned fresh volume root nor read 0600 materials")
	assert.Contains(t, strings.Join(claim.Argv, " "), "/var/lib/kafka/data",
		"claim argv carries the image data dir as the ownership-seeding reference")

	// 缺省面：postgres（database 类型）与 nats 的 claim 探测 User 空——
	// 镜像自带 root 用户，声明缺席 = 零行为变化。
	fx2, _ := originFixture(t, "postgres",
		"postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly")
	fx2.ut.setVolumeOriginMarker(nil)
	ctx2 := context.Background()
	require.NoError(t, fx2.stores.Networks.Create(ctx2, &network.Network{
		ID: "01JD0NET000000000000000005", ProjectID: tProjectID, Name: "extra",
	}))
	fx2.d.Step(ctx2, dataservicetemplate.KindDatabase)
	for _, r := range fx2.ut.requests() {
		if r.Volume != nil && strings.HasPrefix(r.ID, "volorigin-") {
			assert.Empty(t, r.User, "database-kind engines declare no tool user (image default)")
		}
	}
}
