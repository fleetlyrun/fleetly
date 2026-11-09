package backup

// manifest_test.go 是 manifest 锚定对象集的白盒测试（B4a，ADR-0096 决策 6；
// retention_test 同款白盒纪律：纯函数直测，时间/摘要全作参数）。覆盖面：
// 键形三公式（集前缀/manifest/成员）与安全闸、集键分类（legacy 形态不
// 入射程）、total_digest 有序聚合、ParseManifest 形状闸与自洽校验、
// TotalSize/WithinSet 聚合面、Encode 键序稳定。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manifestFixtureTime 是键形测试的固定时刻（KeyMint 测试同源口径）。
var manifestFixtureTime = time.Date(2026, 11, 7, 8, 9, 10, 0, time.UTC)

// memberDigestOf 铸载荷的成员摘要（hex；与 Put 流式铸造同口径）。
func memberDigestOf(t *testing.T, payload string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// 键形三公式：集前缀（KeyMint）、manifest 键、成员键——同 ts-id 段集内
// 同源；manifest.json 与成员名各住集前缀下。
func TestManifestKeyFormulas(t *testing.T) {
	set := KeyMint("01JPROJ", "01JDB", "01JBKP", manifestFixtureTime)
	assert.Equal(t, "backups/01JPROJ/01JDB/20261107T080910Z-01JBKP", set)
	assert.Equal(t, "backups/01JPROJ/01JDB/20261107T080910Z-01JBKP/manifest.json", ManifestKeyOf(set))
	member, err := MemberKeyOf(set, DefaultMemberName)
	require.NoError(t, err)
	assert.Equal(t, "backups/01JPROJ/01JDB/20261107T080910Z-01JBKP/data", member)
	member, err = MemberKeyOf(set, "ORDERS_stream.tgz") // 多流形态成员名（B4b 预演）
	require.NoError(t, err)
	assert.Equal(t, "backups/01JPROJ/01JDB/20261107T080910Z-01JBKP/ORDERS_stream.tgz", member)
}

// 成员名安全闸：空名/点段/斜杠/反斜杠拒绝（键段纵深防御）。
func TestMemberNameSafetyGate(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, strings.Repeat("x", 129)} {
		_, err := MemberKeyOf("backups/p/d/ts-id", bad)
		assert.Error(t, err, "member name %q must be rejected as a key segment", bad)
	}
	assert.NoError(t, ValidMemberName("data"))
}

// 集键分类：集内键（manifest/成员）可拆；legacy 单对象键（恰四段）与更
// 浅/更深的键不入集射程（orphanPass 的判据面）。
func TestSplitSetKeyClassification(t *testing.T) {
	set, name, ok := SplitSetKey("backups/p/d/20261107T080910Z-01JBKP/manifest.json")
	require.True(t, ok)
	assert.Equal(t, "backups/p/d/20261107T080910Z-01JBKP", set)
	assert.Equal(t, "manifest.json", name)

	set, name, ok = SplitSetKey("backups/p/d/20261107T080910Z-01JBKP/data")
	require.True(t, ok)
	assert.Equal(t, "backups/p/d/20261107T080910Z-01JBKP", set)
	assert.Equal(t, "data", name)

	// legacy 单对象键（B4a 前形态）：恰四段本体，不是集内键。
	_, _, ok = SplitSetKey("backups/p/d/20261107T080910Z-01JBKP")
	assert.False(t, ok, "a legacy single-object key never classifies as a set member")
	// 更浅/更深形态。
	_, _, ok = SplitSetKey("backups/p/d")
	assert.False(t, ok)
	_, _, ok = SplitSetKey("backups/p/d/ts-id/data/extra")
	assert.False(t, ok, "a key deeper than one member segment never classifies (member names carry no slash)")
	_, _, ok = SplitSetKey("backups/p/d/ts-id/")
	assert.False(t, ok, "empty member segment never classifies")
}

// total_digest 有序聚合：成员序变化即 digest 变化（成员序是对象集身份面）。
func TestTotalDigestOrderSensitive(t *testing.T) {
	a := ManifestMember{Key: "k-a", SHA256: memberDigestOf(t, "AAA"), SizeBytes: 3}
	b := ManifestMember{Key: "k-b", SHA256: memberDigestOf(t, "BBB"), SizeBytes: 3}
	assert.NotEqual(t, TotalDigest([]ManifestMember{a, b}), TotalDigest([]ManifestMember{b, a}),
		"member order is part of the set identity")
	assert.Equal(t, TotalDigest([]ManifestMember{a, b}), TotalDigest([]ManifestMember{a, b}))
	// 公式可复算：sha256(成员摘要 hex 原序拼接)。
	h := sha256.New()
	h.Write([]byte(a.SHA256))
	h.Write([]byte(b.SHA256))
	assert.Equal(t, hex.EncodeToString(h.Sum(nil)), TotalDigest([]ManifestMember{a, b}))
}

// ParseManifest 形状闸：缺 kind/缺引擎/空成员/坏摘要/负 size/total_digest
// 不自洽全部 fail-closed；健康清单原值回。
func TestParseManifestShapeGate(t *testing.T) {
	healthy := func() *Manifest {
		return &Manifest{
			Kind: "database", Engine: "postgres", EngineVersion: "18",
			Members:   []ManifestMember{{Key: "backups/p/d/ts-id/data", SHA256: memberDigestOf(t, "PAYLOAD"), SizeBytes: 7}},
			CreatedAt: "2026-11-07T00:00:00Z",
		}
	}
	m := healthy()
	m.TotalDigest = TotalDigest(m.Members)
	got, err := ParseManifest(mustJSON(t, m))
	require.NoError(t, err)
	assert.Equal(t, m, got)
	assert.Equal(t, int64(7), got.TotalSize())
	assert.True(t, got.WithinSet("backups/p/d/ts-id"))
	assert.Equal(t, []string{"backups/p/d/ts-id/data"}, got.MemberKeys())

	// total_digest 先铸再坏（自洽校验面）。
	breakIt := func(mutate func(m *Manifest)) []byte {
		bad := healthy()
		bad.TotalDigest = TotalDigest(bad.Members)
		mutate(bad)
		return mustJSON(t, bad)
	}
	for name, payload := range map[string][]byte{
		"no kind":          breakIt(func(m *Manifest) { m.Kind = "" }),
		"no engine":        breakIt(func(m *Manifest) { m.Engine = "" }),
		"no members":       breakIt(func(m *Manifest) { m.Members = nil }),
		"no created_at":    breakIt(func(m *Manifest) { m.CreatedAt = "" }),
		"empty member key": breakIt(func(m *Manifest) { m.Members[0].Key = "" }),
		"short digest":     breakIt(func(m *Manifest) { m.Members[0].SHA256 = "abc" }),
		"non-hex digest":   breakIt(func(m *Manifest) { m.Members[0].SHA256 = strings.Repeat("z", 64) }),
		"negative size":    breakIt(func(m *Manifest) { m.Members[0].SizeBytes = -1 }),
		"empty total":      breakIt(func(m *Manifest) { m.TotalDigest = "" }),
		"inconsistent total": breakIt(func(m *Manifest) {
			m.Members[0].SHA256 = memberDigestOf(t, "OTHER") // 成员变了 total 未变
		}),
		"not json": []byte("PGDUMP-BYTES"),
	} {
		_, err := ParseManifest(payload)
		assert.Error(t, err, "manifest %q must fail the shape gate", name)
	}

	// 旧 wire 键（family 形）不再是清单：解析拒绝——无兼容层前提的终态
	// 口径（与实例标记同判据）。
	legacy := strings.Replace(string(mustJSON(t, m)), `"kind":`, `"family":`, 1)
	_, err = ParseManifest([]byte(legacy))
	require.Error(t, err, "the pre-rename family wire key must not parse")

	// WithinSet：集外成员键拒（防篡改 manifest 指向任意键）。
	outside := healthy()
	outside.TotalDigest = TotalDigest(outside.Members)
	assert.False(t, outside.WithinSet("backups/p/d/other-set"))
}

// Encode 键序稳定：字段声明序 = JSON 键序（对象可比对性）；成员三元组同序。
func TestManifestEncodeKeyOrder(t *testing.T) {
	m := &Manifest{
		Kind: "database", Engine: "postgres", EngineVersion: "18",
		Members:     []ManifestMember{{Key: "k", SHA256: strings.Repeat("a", 64), SizeBytes: 1}},
		TotalDigest: strings.Repeat("b", 64),
		CreatedAt:   "2026-11-07T00:00:00Z",
	}
	body, err := m.Encode()
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"database","engine":"postgres","engine_version":"18","members":[{"key":"k","sha256":"`+
		strings.Repeat("a", 64)+`","size_bytes":1}],"total_digest":"`+strings.Repeat("b", 64)+
		`","created_at":"2026-11-07T00:00:00Z"}`, string(body))
	// 键序逐字钉死（JSONEq 不查序——序本身是断言面）。
	assert.True(t, strings.HasPrefix(string(body), `{"kind":"database","engine":"postgres","engine_version":"18","members":[{"key":"k","sha256":"`), string(body))
}

// mustJSON 序列化夹具清单（测试铸造失败即测试代码错误）。
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}
