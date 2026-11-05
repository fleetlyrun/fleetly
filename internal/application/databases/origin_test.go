package databases

// 库卷异源 fail-closed 白盒（ADR-0097 决策 4 验收锚）：ensure 链标记门
// 两态（同源过/异源拒且不 Ensure、幂等重放零额外探测）+ 受理面前置读
// 两态（无标记存量卷过/异源标记拒）+ 坏标记形态拒 + 无 utility 面
// fail-closed。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// originFixture 在既有夹具上补一颗已收敛库（首拍 claim 铸记），返回卷行。
func originFixture(t *testing.T, engineName, url string) (*fixture, *volume.Volume) {
	t.Helper()
	fx := newFixture(t, engineName, url, false)
	ctx := context.Background()
	fx.d.Step(ctx)
	require.NotEmpty(t, fx.rt.calls(), "fixture database must converge before the origin scenarios")
	row, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, err)
	return fx, row
}

// markerOf 铸一颗标记 JSON（id 注入面）。
func markerOf(t *testing.T, id string) []byte {
	t.Helper()
	body, err := dbtemplateRender(dbtemplate.MintInstanceMarker(id, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	return []byte(body)
}

// dbtemplateRender 是 renderInstanceMarker 的测试可达面（包内非导出——
// 同包白盒直访的等价小件）。
func dbtemplateRender(m dbtemplate.InstanceMarker) (string, error) {
	argv, err := dbtemplate.InstanceMarkerClaimArgv("/unused", m)
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
	fx.d.Step(ctx)

	assert.Len(t, fx.rt.calls(), before, "a foreign instance marker must refuse the ensure")
	// 最近的 utility 探测携带挂卷锚（卷行 id + /seed 挂点）。
	reqs := fx.ut.requests()
	require.NotEmpty(t, reqs)
	last := reqs[len(reqs)-1]
	require.NotNil(t, last.Volume)
	assert.Equal(t, dbtemplate.SeedMountPoint, last.Volume.Target)
}

// ② 同源过 + 幂等：标记与当前库行一致 → Ensure 照常；重复收敛不再发
// 探测容器（memo 短路先于标记门——探测只在真实确保拍发生）。
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
	fx.d.Step(ctx)
	assert.Greater(t, len(fx.rt.calls()), before, "a matching marker lets the ensure proceed")

	fx.d.Step(ctx) // memo 短路拍：零探测
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
	fx.d.Step(ctx)
	assert.Len(t, fx.rt.calls(), before, "an unreadable marker is a foreign-origin fact and must refuse the ensure")
}

// ④ 受理面前置读两态：无标记（预建/无标记存量卷）过；异源标记拒
// （ErrVolumeForeignOrigin 判别锚）；全新卷名零探测。
func TestVolumeOriginForCreate(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, volRow := originFixture(t, "postgres", url)
	ctx := context.Background()

	// 无标记（探测回读空）→ 过。
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, tProjectID, tDatabaseNm, "postgres"))

	// 异源标记 → 拒（哨兵可判别）。
	fx.ut.setVolumeOriginMarker(markerOf(t, "01jd0prev00000000000000000"))
	err := fx.d.CheckVolumeOriginForCreate(ctx, tProjectID, tDatabaseNm, "postgres")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrVolumeForeignOrigin)

	// 全新卷名 → 零 utility 探测、直过。
	n := len(fx.ut.requests())
	require.NoError(t, fx.d.CheckVolumeOriginForCreate(ctx, tProjectID, "fresh-name", "postgres"))
	assert.Len(t, fx.ut.requests(), n, "a fresh volume name has no retained face to probe")
	_ = volRow
}

// ⑤ 无 utility 面 fail-closed：标记门是确保前置，面缺席 = 拒绝确保
// （Database 域要求 RuntimeUtility——rotate/preseed 同族判据）。
func TestVolumeOriginGateRequiresUtilityFace(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx, _ := originFixture(t, "postgres", url)
	ctx := context.Background()
	row, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	tpl, err := dbtemplate.For(row.Engine, "")
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
