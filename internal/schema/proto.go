package schema

// proto 消息 → JSON Schema（Spec 面）：protojson 是 Spec 的 JSON 形态
//（CLI --json 全局 UseProtoNames: snake_case），故字段名取 descriptor
// TextName（= proto 字段名）、枚举取值名列表、Duration 渲染为字符串——
// 与 protojson 输出一一对齐，Schema 描述的就是线上字节形状。

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ReflectProto 从 proto 消息（零值即可）生成 JSON Schema 树。
func ReflectProto(m proto.Message) map[string]any {
	return schemaForMessage(m.ProtoReflect().Descriptor())
}

func schemaForMessage(desc protoreflect.MessageDescriptor) map[string]any {
	properties := map[string]any{}
	for i := 0; i < desc.Fields().Len(); i++ {
		fd := desc.Fields().Get(i)
		properties[fd.TextName()] = schemaForField(fd)
	}

	tree := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}

	// oneof 互斥约束：分支字段全部进 properties（可选），互斥经 oneOf 表达
	//（protojson 语义：恰好一个分支出现）。message 含多个 oneof 时用
	// allOf 叠加（JSON Schema 单对象只许一个 oneOf 键）。
	var oneofClauses []map[string]any
	for i := 0; i < desc.Oneofs().Len(); i++ {
		od := desc.Oneofs().Get(i)
		var alts []map[string]any
		for j := 0; j < od.Fields().Len(); j++ {
			alts = append(alts, map[string]any{"required": []string{od.Fields().Get(j).TextName()}})
		}
		if len(alts) > 0 {
			oneofClauses = append(oneofClauses, map[string]any{"oneOf": alts})
		}
	}
	switch {
	case len(oneofClauses) == 1:
		tree["oneOf"] = oneofClauses[0]["oneOf"]
	case len(oneofClauses) > 1:
		tree["allOf"] = oneofClauses
	}
	return tree
}

// schemaForField 按 map / list / 单值三形态分发（map 元素经 MapValue 归一
// 到同一单值路径）。
func schemaForField(fd protoreflect.FieldDescriptor) map[string]any {
	if fd.IsMap() {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaForSingle(fd.MapValue()),
		}
	}
	if fd.IsList() {
		return map[string]any{"type": "array", "items": schemaForSingle(fd)}
	}
	return schemaForSingle(fd)
}

// schemaForSingle 映射 protojson 的单值序列化形态。
func schemaForSingle(fd protoreflect.FieldDescriptor) map[string]any {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		// protojson 渲染 bytes 为 base64 字符串。
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind,
		protoreflect.Sint64Kind, protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind,
		protoreflect.Fixed64Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.EnumKind:
		values := make([]string, 0, fd.Enum().Values().Len())
		for i := 0; i < fd.Enum().Values().Len(); i++ {
			values = append(values, string(fd.Enum().Values().Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": values}
	case protoreflect.MessageKind:
		// Well-Known Type 的 protojson 形态是字符串（Duration "3s"、
		// Timestamp RFC3339）；其余 message 递归。
		switch fd.Message().FullName() {
		case "google.protobuf.Duration":
			return map[string]any{"type": "string", "format": "duration"}
		case "google.protobuf.Timestamp":
			return map[string]any{"type": "string", "format": "date-time"}
		}
		return schemaForMessage(fd.Message())
	default:
		// GroupKind 等遗留形态不进 Spec 面；遇到即在此显式扩面。
		return map[string]any{"type": "string", "x-proto-kind": fd.Kind().String()}
	}
}
