package fleetlygrpc

import (
	"strings"
	"testing"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// A4 回归（N0 修复批）：Revision 冻结体的序列化必须与 CLI --json / REST
// gateway 同策略（protojson snake_case）。冻结体经 DiffRevisions 的路径
// 输出直接用户可见，camelCase 会造成三面割裂；digest 内容寻址随本形态，
// 变更即一次性数据迁移（见 marshalSpec 注释）。
func TestMarshalSpecUsesProtoNames(t *testing.T) {
	spec := &specv1.AppSpec{
		SchemaVersion: 1,
		App:           &specv1.AppRef{Id: "01JD0APP000000000000000000", Project: "01JD0PROJ00000000000000000"},
		Processes: []*specv1.ProcessSpec{{
			Name: "web", Replicas: 1,
			ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"},
		}},
	}
	body, err := marshalSpec(spec)
	if err != nil {
		t.Fatalf("marshalSpec: %v", err)
	}
	s := string(body)
	for _, snake := range []string{"schema_version", "\"project\"", "replicas"} {
		if !strings.Contains(s, snake) {
			t.Errorf("freeze body must use proto field names: %q missing in %s", snake, s)
		}
	}
	for _, camel := range []string{"schemaVersion", "fromBuild"} {
		if strings.Contains(s, camel) {
			t.Errorf("freeze body must not use camelCase: %q present in %s", camel, s)
		}
	}
}
