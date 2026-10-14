package schema

// Go 结构体反射 → JSON Schema（事件 payload 面）：payload 结构体以 json
// tag 为契约（无 tag 即形状含糊，fail-fast 拒绝——钉形状的面不允许隐式
// 字段名）。required = 无 omitempty 的字段（encoding/json 语义：非
// omitempty 字段恒出现）；additionalProperties:false 表达"字段只增"的
// 封闭集（ADR-0026）。

import (
	"fmt"
	"reflect"
	"strings"
)

// Reflect 从 Go 值/类型生成 JSON Schema 树（v 传结构体零值即可；指针自动
// 解引用）。顶层必须是结构体（Outbox payload 是 JSON object 或 null——
// 后者用 Null()）；字段形状支持实际用语全谱：标量、具 json tag 的结构体、
// slice、map[string]T。其余（接口/通道/嵌入字段/无 tag 字段/顶层标量）
// panic——新形状进 payload 时在此显式扩面。
func Reflect(v any) map[string]any {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("schema: reflect: payload top level must be a struct, got %v (null payloads use Null())", t))
	}
	return schemaForStruct(t)
}

func schemaForType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return schemaForStruct(t)
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaForType(t.Elem())}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("schema: reflect: map key %s is not string (only string-keyed maps serialize to JSON objects)", t))
		}
		return map[string]any{"type": "object", "additionalProperties": schemaForType(t.Elem())}
	default:
		panic(fmt.Sprintf("schema: reflect: unsupported payload shape %s (extend schemaForType explicitly)", t))
	}
}

func schemaForStruct(t reflect.Type) map[string]any {
	properties := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			panic(fmt.Sprintf("schema: reflect: embedded field %s.%s is not a payload shape", t, f.Name))
		}
		if f.PkgPath != "" {
			continue // 未导出字段不参与 JSON 契约
		}
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			panic(fmt.Sprintf("schema: reflect: field %s.%s has no json tag (payload shapes are pinned by explicit tags)", t, f.Name))
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			panic(fmt.Sprintf("schema: reflect: field %s.%s has an empty json name", t, f.Name))
		}
		properties[name] = schemaForType(f.Type)
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	tree := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		tree["required"] = required
	}
	return tree
}
