package assembly

// 能力自描述面的两道守卫（F1.4，闭 ADR-0026 验收锚）：
//
//   - golden 漂移门：schema.Build() 全量文档钉死在 testdata——payload 字段
//     改名/删除（或 Spec 契约变更）即 CI 红，重生成须与变更同 commit；
//   - 完备性对账：eventcode 注册表在册事件 ↔ schema event 条目双向相等
//     ——新事件入册而漏注册 schema（自描述面静默缺角）在此红。
//
// 落点在 assembly：组合根链接全部贡献方（engine / api 层的 init 注册在此
// 活跃），任何更浅的包都只见部分注册表。

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/model/eventcode"
	"github.com/fleetlyrun/fleetly/internal/schema"
)

var schemaGoldenUpdate = flag.Bool("update", false, "rewrite golden snapshot files")

// TestSelfDescriptionGoldenPinned：自描述全量文档 golden（再生成：
// go test ./internal/assembly -update，与契约变更同 commit）。
func TestSelfDescriptionGoldenPinned(t *testing.T) {
	got := schema.Build().CanonicalJSON()
	path := filepath.Join("testdata", "selfdescription.golden.json")
	if *schemaGoldenUpdate {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/assembly -update")
	assert.Equal(t, string(want), got,
		"self-description document drifted from golden — a payload field rename/removal, a spec contract change, or a new registration needs an intentional golden update in the same commit (ADR-0026 shape pinning)")
}

// TestSchemaCoversEventcodeRegistry：事件 schema 条目与 eventcode 注册表
// 双向对账（漏报 = 新事件无 schema；多报 = 注册了不在册名字——后者本就
// 在注册期 panic，此处兜底）。
func TestSchemaCoversEventcodeRegistry(t *testing.T) {
	doc := schema.Build()
	byName := map[string]schema.Entry{}
	for _, e := range doc.Entries {
		if e.Kind == schema.KindEvent {
			byName[e.Name] = e
		}
	}
	for _, name := range eventcode.Names() {
		e, ok := byName[name]
		if !ok {
			t.Errorf("event %s is registered in eventcode but has no payload schema entry (register it in the owning package's schemareg)", name)
			continue
		}
		assert.Equal(t, e.Summary, eventcodeGetSummary(name), "event %s schema summary must stay single-sourced from eventcode", name)
	}
	for name := range byName {
		if _, ok := eventcode.Get(name); !ok {
			t.Errorf("schema event entry %s is not in the eventcode registry", name)
		}
	}
}

func eventcodeGetSummary(name string) string {
	e, _ := eventcode.Get(name)
	return e.Summary
}
