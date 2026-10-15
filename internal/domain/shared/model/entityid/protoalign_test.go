package entityid_test

// descriptor 对齐测试（P4-1 批 1 单源执法）：proto 面的 protovalidate CEL
// 注解无法引用 Go 常量，structure.v1 CreateProjectRequest.name 的 shape
// 规则与本包导出的 NamePatternSource / MaxNameLen 各存一份——字节一致由
// 本测试执法：从生成的请求 descriptor 反读 cel 选项，断言 shape 表达式
// 包含正则字面与长度界。改形态必须 proto 与本包同 commit 同改，此处即红。

import (
	"strconv"
	"testing"

	bufvalidate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
)

func TestCreateProjectNameAnnotationMatchesEntityIDSource(t *testing.T) {
	md := (&structurev1.CreateProjectRequest{}).ProtoReflect().Descriptor()
	fd := md.Fields().ByName("name")
	require.NotNil(t, fd, "CreateProjectRequest.name 字段缺席——proto 漂移")

	opts := fd.Options()
	fieldRules := proto.GetExtension(opts, bufvalidate.E_Field).(*bufvalidate.FieldRules)
	celRules := fieldRules.GetCel()
	require.NotEmpty(t, celRules, "name 字段缺 cel 注解——试点族注解被移除")

	// shape 规则（id string.dns_label）：表达式须包含本包导出的正则字面
	// 与长度界（本测试即 NamePatternSource 的第二份字面唯一合法居所——
	// 断言以导出常量为源，不重复硬编码）。
	var shape *bufvalidate.Rule
	for _, r := range celRules {
		if r.GetId() == "string.dns_label" {
			shape = r
			break
		}
	}
	require.NotNil(t, shape, "name 字段缺 string.dns_label 规则")
	assert.Contains(t, shape.GetExpression(), entityid.NamePatternSource,
		"shape 表达式与 entityid.NamePatternSource 漂移——proto 与本包必须同 commit 同改")
	assert.Contains(t, shape.GetExpression(), "this.size() <= "+strconv.Itoa(entityid.MaxNameLen),
		"shape 表达式与 entityid.MaxNameLen 漂移——proto 与本包必须同 commit 同改")
}
