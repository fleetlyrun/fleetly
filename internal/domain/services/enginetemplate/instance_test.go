package enginetemplate

// 实例标记契约单测（ADR-0097 决策 4；B2a 升格自 dbtemplate——字段实体
// 中性化后的口径）：铸/读往返、字符集闸（坏 id/坏 family/坏时间拒——载荷
// 进单引号 shell 串前的单一防线）、claim/read 两 argv 形态钉死（平台常量
// 串，零用户数据插值）。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// markerFixtureTime 是铸记时间的夹具锚（RFC3339）。
var markerFixtureTime = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// markerFixtureFamily 是夹具家族词面（数据库家族的消费词形）。
const markerFixtureFamily = "database"

func TestInstanceMarkerRenderParseRoundTrip(t *testing.T) {
	m := MintInstanceMarker("01jd0db0000000000000000000", markerFixtureFamily, markerFixtureTime)
	body, err := renderInstanceMarker(m)
	require.NoError(t, err)
	assert.JSONEq(t, `{"entity_id":"01jd0db0000000000000000000","family":"database","minted_at":"2026-11-04T12:00:00Z"}`, body,
		"entity-neutral field names are the terminal form (no compatibility layer, ADR-0094 preamble)")

	back, err := ParseInstanceMarker([]byte(body))
	require.NoError(t, err)
	assert.Equal(t, m, back)
}

func TestInstanceMarkerCharsetGate(t *testing.T) {
	// 坏 id 形态：敌意载荷（单引号/空/大写 ULID/错长）在渲染闸全拒——
	// 载荷进单引号 shell 串前无第二道防线。
	iso := markerFixtureTime.Format(time.RFC3339)
	for _, id := range []string{
		"",                            // 空
		"01JD0DB0000000000000000000",  // 大写（铸造只出小写，ADR-0092）
		"01jd0db0000000000000000",     // 25 位
		"evil'-injection",             // 引号注入形态（连字符段携带单引号）
		"01jd0db0000000000000000000'", // ULID 形 + 悬挂引号
	} {
		_, err := renderInstanceMarker(InstanceMarker{EntityID: id, Family: markerFixtureFamily, MintedAt: iso})
		require.Error(t, err, "id %q must fail the render gate", id)
	}
	// 坏 family 形态：空/大写/引号/连字符失形/超界全拒。
	for _, family := range []string{
		"",
		"Database",
		"pubsub'",
		"-pubsub",
		"pubsub-",
		"pu--bsub",
		"pub-sub!",
		strings.Repeat("x", 33),
	} {
		_, err := renderInstanceMarker(InstanceMarker{EntityID: "01jd0db0000000000000000000", Family: family, MintedAt: iso})
		require.Error(t, err, "family %q must fail the render gate", family)
	}
	// 坏时间形态。
	_, err := renderInstanceMarker(InstanceMarker{EntityID: "01jd0db0000000000000000000", Family: markerFixtureFamily, MintedAt: "not-a-time"})
	require.Error(t, err)
	// 旧名基形 id（ADR-0092 Valid 双形——快照恢复共存面）合法；带连字符的
	// 家族词面（未来家族词如 foo-bar）合法。
	_, err = renderInstanceMarker(InstanceMarker{EntityID: "shop-db-7", Family: markerFixtureFamily, MintedAt: iso})
	require.NoError(t, err, "legacy name-based ids stay legal on the restored-snapshot coexistence face")
	_, err = renderInstanceMarker(InstanceMarker{EntityID: "01jd0db0000000000000000000", Family: "bucket-store", MintedAt: iso})
	require.NoError(t, err)
}

func TestInstanceMarkerParseRejectsMalformed(t *testing.T) {
	_, err := ParseInstanceMarker([]byte("REDIS0011-rdb-bytes"))
	require.Error(t, err, "non-marker content occupying the file name is a foreign-origin fact")
	_, err = ParseInstanceMarker([]byte(`{"entity_id":"","family":"database","minted_at":"2026-11-04T12:00:00Z"}`))
	require.Error(t, err)
	_, err = ParseInstanceMarker([]byte(`{"entity_id":"01jd0db0000000000000000000","family":"database"}`))
	require.Error(t, err, "minted_at is required")
	_, err = ParseInstanceMarker([]byte(`{"entity_id":"01jd0db0000000000000000000","family":"DB","minted_at":"2026-11-04T12:00:00Z"}`))
	require.Error(t, err, "family must be a lowercase family word")
	// 旧字段名（database_id 形）不再是标记：解析拒绝——无兼容层前提的
	// 终态口径（ADR-0094 开发期裁定）。
	_, err = ParseInstanceMarker([]byte(`{"database_id":"01jd0db0000000000000000000","minted_at":"2026-11-04T12:00:00Z"}`))
	require.Error(t, err, "the pre-neutralization field shape must not parse")
}

func TestInstanceMarkerArgvShapes(t *testing.T) {
	m := MintInstanceMarker("01jd0db0000000000000000000", markerFixtureFamily, markerFixtureTime)
	claim, err := InstanceMarkerClaimArgv("/seed", "/var/lib/kafka/data", m)
	require.NoError(t, err)
	require.Len(t, claim, 3)
	assert.Equal(t, "sh", claim[0])
	assert.Equal(t, "-c", claim[1])
	body, err := json.Marshal(m)
	require.NoError(t, err)
	assert.Contains(t, claim[2], "printf %s '"+string(body)+"' > /seed/"+InstanceMarkerFile,
		"claim script embeds the canonical marker payload")
	assert.True(t, strings.HasPrefix(claim[2], "if [ -e /seed/"+InstanceMarkerFile+" ]"),
		"claim script reads-first (idempotent replay keeps the existing marker)")
	assert.Contains(t, claim[2], `chown "$(stat -c '%u:%g' /var/lib/kafka/data 2>/dev/null)" /seed 2>/dev/null || true`,
		"claim script seeds the volume-root ownership from the image data dir before minting (B8-fix: non-root engine users cannot write a root-owned fresh volume)")

	read := InstanceMarkerReadArgv("/seed")
	require.Len(t, read, 3)
	assert.Equal(t, "sh", read[0])
	assert.Contains(t, read[2], "cat /seed/"+InstanceMarkerFile+" 2>/dev/null || true",
		"read-only probe yields empty output (not an error) when the marker is absent")
}

func TestInstanceMarkerClaimPathGate(t *testing.T) {
	m := MintInstanceMarker("01jd0db0000000000000000000", markerFixtureFamily, markerFixtureTime)
	// 挂点/属主参照路径进 sh -c 串的字符集闸：相对路径/空白/引号/元字符全拒
	// （平台常量面——模板 DataTarget 与 SeedMountPoint 的纵深防线）。
	for _, bad := range []string{
		"", "seed", "/var/lib/x y", "/var/lib/x'y", `/var/lib/x\y`,
		"/var/lib/x;id", "/$(id)", "/a b", "/x&y",
	} {
		_, err := InstanceMarkerClaimArgv(bad, "/var/lib/kafka/data", m)
		require.Error(t, err, "mount point %q must fail the path gate", bad)
		_, err = InstanceMarkerClaimArgv("/seed", bad, m)
		require.Error(t, err, "data target %q must fail the path gate", bad)
	}
	// 合法平台路径形态（含点/下划线/连字符嵌段）放行。
	_, err := InstanceMarkerClaimArgv("/seed", "/var/lib/postgresql/data", m)
	require.NoError(t, err)
	_, err = InstanceMarkerClaimArgv("/seed", "/data", m)
	require.NoError(t, err)
}
