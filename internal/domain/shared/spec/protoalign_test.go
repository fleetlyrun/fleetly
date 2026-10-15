package spec

// descriptor 对齐测试（P4-1 批 1 Run B1 单源执法）：proto 面的 protovalidate
// CEL 注解无法引用 Go 常量，structure.v1 PutSecretRequest.name 的 secret 名
// 白名单与本包 SecretNamePattern、PutSharedVariableRequest /
// PutAppVariableRequest.name 的 env 键白名单与本包 EnvNamePattern 各存
// 一份——字节一致由本测试执法：从生成的请求 descriptor 反读 cel 选项，
// 断言 shape 表达式包含本包导出的正则字面（与 ".." 拒等补充子句）。改形态
// 必须 proto 与本包同 commit 同改，此处即红。

import (
	"testing"

	bufvalidate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

// celRuleByID 从字段的 cel 规则集中按 id 取规则。
func celRuleByID(t *testing.T, rules []*bufvalidate.Rule, id string) *bufvalidate.Rule {
	t.Helper()
	for _, r := range rules {
		if r.GetId() == id {
			return r
		}
	}
	t.Fatalf("字段 cel 规则缺 id %q——proto 注解漂移", id)
	return nil
}

func fieldCEL(t *testing.T, md protoreflect.MessageDescriptor, fieldName string) []*bufvalidate.Rule {
	t.Helper()
	fd := md.Fields().ByName(protoreflect.Name(fieldName))
	require.NotNil(t, fd, "字段 %s 缺席——proto 漂移", fieldName)
	opts := fd.Options()
	fieldRules := proto.GetExtension(opts, bufvalidate.E_Field).(*bufvalidate.FieldRules)
	require.NotEmpty(t, fieldRules.GetCel(), "字段 %s 缺 cel 注解——退役族注解被移除", fieldName)
	return fieldRules.GetCel()
}

func TestPutSecretNameAnnotationMatchesSecretNamePattern(t *testing.T) {
	md := (&structurev1.PutSecretRequest{}).ProtoReflect().Descriptor()
	rules := fieldCEL(t, md, "name")

	// shape 规则（id string.secret_name）：表达式须包含本包导出的正则字面
	// 与 ".." 拒子句（ValidSecretName 的两半）。
	shape := celRuleByID(t, rules, "string.secret_name")
	assert.Contains(t, shape.GetExpression(), SecretNamePattern,
		"shape 表达式与 SecretNamePattern 漂移——proto 与本包必须同 commit 同改")
	assert.Contains(t, shape.GetExpression(), `!this.contains('..')`,
		"shape 表达式缺 \"..\" 拒子句——与 ValidSecretName 漂移")
}

func TestVariableNameAnnotationMatchesEnvNamePattern(t *testing.T) {
	// 共享变量与 App 变量同一名形态白名单（ADR-0072 三级合成同源）。
	for _, tc := range []struct {
		name string
		md   protoreflect.MessageDescriptor
	}{
		{"PutSharedVariableRequest", (&structurev1.PutSharedVariableRequest{}).ProtoReflect().Descriptor()},
		{"PutAppVariableRequest", (&structurev1.PutAppVariableRequest{}).ProtoReflect().Descriptor()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := fieldCEL(t, tc.md, "name")
			shape := celRuleByID(t, rules, "string.env_name")
			assert.Contains(t, shape.GetExpression(), EnvNamePattern,
				"shape 表达式与 EnvNamePattern 漂移——proto 与本包必须同 commit 同改")
		})
	}
}
