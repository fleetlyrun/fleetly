package swarm

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
)

// serviceSpecEqualJSON 是 no-op update 跳过面的判定（staging 真机自激环修复，
// 2026-10-02）：等价/仅 ForceUpdate 差异 = 跳过；实质差异 = update。期望
// canonical 由调用方单算传入（C19-4：断路器/落账/比对共用同一份）。
func TestServiceSpecEqual(t *testing.T) {
	base := func() (swarm.ServiceSpec, string) {
		spec := swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "fleetly-app-web", Labels: map[string]string{"a": "b"}},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27", Hostname: "{{.Service.Name}}"},
			},
		}
		return spec, canonicalSpecJSON(spec)
	}
	current := func(mutate func(*swarm.ServiceSpec)) swarm.ServiceSpec {
		spec, _ := base()
		if mutate != nil {
			mutate(&spec)
		}
		return spec
	}

	_, want := base()
	assert.True(t, serviceSpecEqualJSON(want, current(nil)), "identical specs are equal")
	assert.True(t, serviceSpecEqualJSON(want, current(nil)), "comparison is deterministic across calls")

	// 服务端回滚自增 ForceUpdate：不算语义差异。
	assert.True(t, serviceSpecEqualJSON(want, current(func(s *swarm.ServiceSpec) {
		s.TaskTemplate.ForceUpdate = 7
	})), "ForceUpdate alone must not count as a change")

	// map 键序差异（JSON canonical 化归一）。
	reordered := current(func(s *swarm.ServiceSpec) { s.Labels = map[string]string{"z": "1", "a": "b"} })
	assert.True(t, serviceSpecEqualJSON(canonicalSpecJSON(reordered), current(func(s *swarm.ServiceSpec) {
		s.Labels = map[string]string{"z": "1", "a": "b"}
	})), "map iteration order must not leak")

	// 实质差异：镜像变更。
	assert.False(t, serviceSpecEqualJSON(want, current(func(s *swarm.ServiceSpec) {
		s.TaskTemplate.ContainerSpec.Image = "nginx:1.28"
	})), "a real spec change must trigger update")

	// marshal 异常形态（期望串为空）：视为不等（保守：多一次 update 无害）。
	assert.False(t, serviceSpecEqualJSON("", current(nil)), "empty desired JSON must not read as equal")
}
