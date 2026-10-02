package fleetlygrpc

import (
	"fmt"
	"strings"
	"testing"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/engine"
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

// mapStateError 的哨兵映射面（staging 真机实证补漏，2026-10-02：未批准的
// 跨 Project 网络引用此前落 E_INTERNAL——受理预检的 strict 拒绝是可编程
// 分支，不是内部错误）。
func TestMapStateErrorCrossProjectRefNotApproved(t *testing.T) {
	err := mapStateError(fmt.Errorf("wrap: %w", engine.ErrCrossProjectRefNotApproved), "deploy")
	if !strings.Contains(err.Error(), "E_INVALID_ARGUMENT") || !strings.Contains(err.Error(), "declare and approve the network peer") {
		t.Errorf("unapproved peer ref must map to an actionable E_INVALID_ARGUMENT, got: %v", err)
	}
	// 哨兵链上再包一层同样命中（errors.Is 语义）。
	err = mapStateError(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", engine.ErrCrossProjectRefNotApproved)), "deploy")
	if !strings.Contains(err.Error(), "E_INVALID_ARGUMENT") {
		t.Errorf("wrapped sentinel must still map via errors.Is, got: %v", err)
	}
}
