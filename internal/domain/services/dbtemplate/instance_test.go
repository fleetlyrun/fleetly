package dbtemplate

// 实例标记契约单测（ADR-0097 决策 4）：铸/读往返、字符集闸（坏 id/坏
// 时间拒——载荷进单引号 shell 串前的单一防线）、claim/read 两 argv 形态
// 钉死（平台常量串，零用户数据插值）。

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

func TestInstanceMarkerRenderParseRoundTrip(t *testing.T) {
	m := MintInstanceMarker("01jd0db0000000000000000000", markerFixtureTime)
	body, err := renderInstanceMarker(m)
	require.NoError(t, err)
	assert.JSONEq(t, `{"database_id":"01jd0db0000000000000000000","minted_at":"2026-11-04T12:00:00Z"}`, body)

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
		_, err := renderInstanceMarker(InstanceMarker{DatabaseID: id, MintedAt: iso})
		require.Error(t, err, "id %q must fail the render gate", id)
	}
	// 坏时间形态。
	_, err := renderInstanceMarker(InstanceMarker{DatabaseID: "01jd0db0000000000000000000", MintedAt: "not-a-time"})
	require.Error(t, err)
	// 旧名基形 id（ADR-0092 Valid 双形——快照恢复共存面）合法。
	_, err = renderInstanceMarker(InstanceMarker{DatabaseID: "shop-db-7", MintedAt: iso})
	require.NoError(t, err, "legacy name-based ids stay legal on the restored-snapshot coexistence face")
}

func TestInstanceMarkerParseRejectsMalformed(t *testing.T) {
	_, err := ParseInstanceMarker([]byte("REDIS0011-rdb-bytes"))
	require.Error(t, err, "non-marker content occupying the file name is a foreign-origin fact")
	_, err = ParseInstanceMarker([]byte(`{"database_id":"","minted_at":"2026-11-04T12:00:00Z"}`))
	require.Error(t, err)
	_, err = ParseInstanceMarker([]byte(`{"database_id":"01jd0db0000000000000000000"}`))
	require.Error(t, err, "minted_at is required")
}

func TestInstanceMarkerArgvShapes(t *testing.T) {
	m := MintInstanceMarker("01jd0db0000000000000000000", markerFixtureTime)
	claim, err := InstanceMarkerClaimArgv("/seed", m)
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

	read := InstanceMarkerReadArgv("/seed")
	require.Len(t, read, 3)
	assert.Equal(t, "sh", read[0])
	assert.Contains(t, read[2], "cat /seed/"+InstanceMarkerFile+" 2>/dev/null || true",
		"read-only probe yields empty output (not an error) when the marker is absent")
}
