package schema

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// reflectPayload 是反射面的测试形状（覆盖事件 payload 实际用语全谱）。
type reflectPayload struct {
	ID     string            `json:"id"`
	Count  int               `json:"count,omitempty"`
	Items  []string          `json:"items,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

func TestReflectPinsRequiredAndOptional(t *testing.T) {
	tree := Reflect(reflectPayload{})
	assert.Equal(t, "object", tree["type"])
	assert.Equal(t, false, tree["additionalProperties"])
	props := tree["properties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "string"}, props["id"])
	assert.Equal(t, map[string]any{"type": "integer"}, props["count"])
	assert.Equal(t,
		map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		props["items"])
	assert.Equal(t,
		map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		props["labels"])
	// 无 omitempty 的字段恒出现（encoding/json 语义）→ required。
	assert.Equal(t, []string{"id"}, tree["required"])
}

func TestReflectRejectsImplicitShapes(t *testing.T) {
	type untagged struct {
		Name string
	}
	assert.Panics(t, func() { Reflect(untagged{}) })

	type embedded struct {
		reflectPayload
	}
	assert.Panics(t, func() { Reflect(embedded{}) })

	assert.Panics(t, func() { Reflect(1) }) // 标量不是 payload 形状
}

func TestReflectProtoMatchesProtoJSONShape(t *testing.T) {
	tree := ReflectProto(&specv1.Source{})
	assert.Equal(t, "object", tree["type"])
	props := tree["properties"].(map[string]any)
	require.Contains(t, props, "git")
	require.Contains(t, props, "image")
	require.Contains(t, props, "upload")
	// oneof 恰好一枝：三分支互斥。
	oneOf := tree["oneOf"].([]map[string]any)
	require.Len(t, oneOf, 3)
	assert.Equal(t, map[string]any{"required": []string{"git"}}, oneOf[0])

	port := ReflectProto(&specv1.PortSpec{})
	assert.Equal(t,
		map[string]any{"type": "string", "enum": []string{"PROTOCOL_UNSPECIFIED", "PROTOCOL_HTTP", "PROTOCOL_H2C", "PROTOCOL_TCP"}},
		port["properties"].(map[string]any)["protocol"])

	proc := ReflectProto(&specv1.ProcessSpec{})
	procProps := proc["properties"].(map[string]any)
	hc := procProps["healthcheck"].(map[string]any)
	assert.Equal(t,
		map[string]any{"type": "string", "format": "duration"},
		hc["properties"].(map[string]any)["interval"])
}

func TestRegisterFailsFast(t *testing.T) {
	assert.Panics(t, func() { Register(KindEvent, "", "s", Null()) })
	assert.Panics(t, func() { Register(KindEvent, "x.y", "", Null()) })
	assert.Panics(t, func() { Register(KindEvent, "x.y", "s", nil) })
	assert.Panics(t, func() { Register(Kind("other"), "x.y", "s", Null()) })
}

// registryIsolated 把注册表状态换进换出（重名 panic 断言不污染全局）。
func TestRegisterDuplicatePanics(t *testing.T) {
	snapshot := registry.entries
	t.Cleanup(func() { registry.entries = snapshot })
	registry.entries = map[string]Entry{}
	Register(KindEvent, "x.y", "s", Null())
	assert.Panics(t, func() { Register(KindEvent, "x.y", "s", Null()) })
}

func TestBuildIsDeterministicAndCarriesDialect(t *testing.T) {
	doc := Build()
	require.NotEmpty(t, doc.Entries)
	// 字典序 + $schema 自报键 + canonical JSON 确定性（两次序列化同字节）。
	for i := 1; i < len(doc.Entries); i++ {
		assert.Less(t, doc.Entries[i-1].Name, doc.Entries[i].Name)
	}
	first := doc.Entries[0].SchemaJSON()
	require.Contains(t, doc.Entries[0].Schema, "$schema")
	assert.Equal(t, schemaDialect, doc.Entries[0].Schema["$schema"])
	assert.Equal(t, first, doc.Entries[0].SchemaJSON())

	// Lookup 与 Build 同源。
	e, ok := Lookup("app")
	require.True(t, ok)
	assert.Equal(t, KindSpec, e.Kind)
	_, ok = Lookup("no.such")
	assert.False(t, ok)
}

func TestNullSchema(t *testing.T) {
	out, err := json.Marshal(Null())
	require.NoError(t, err)
	assert.Equal(t, `{"type":"null"}`, string(out))
}
