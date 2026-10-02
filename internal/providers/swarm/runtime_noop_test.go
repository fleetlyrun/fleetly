package swarm

import (
	"testing"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
)

// serviceSpecEqual 是 no-op update 跳过面的判定（staging 真机自激环修复，
// 2026-10-02）：等价/仅 ForceUpdate 差异 = 跳过；实质差异 = update。
func TestServiceSpecEqual(t *testing.T) {
	base := func() swarm.ServiceSpec {
		return swarm.ServiceSpec{
			Annotations: swarm.Annotations{Name: "fleetly-app-web", Labels: map[string]string{"a": "b"}},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{Image: "nginx:1.27", Hostname: "{{.Service.Name}}"},
			},
		}
	}

	assert.True(t, serviceSpecEqual(base(), base()), "identical specs are equal")
	assert.True(t, serviceSpecEqual(base(), base()), "comparison is deterministic across calls")

	// 服务端回滚自增 ForceUpdate：不算语义差异。
	bumped := base()
	bumped.TaskTemplate.ForceUpdate = 7
	assert.True(t, serviceSpecEqual(base(), bumped), "ForceUpdate alone must not count as a change")

	// map 键序差异（JSON canonical 化归一）。
	reordered := base()
	reordered.Labels = map[string]string{"z": "1", "a": "b"}
	changed := base()
	changed.Labels = map[string]string{"z": "1", "a": "b"}
	assert.True(t, serviceSpecEqual(reordered, changed), "map iteration order must not leak")

	// 实质差异：镜像变更。
	updated := base()
	updated.TaskTemplate.ContainerSpec.Image = "nginx:1.28"
	assert.False(t, serviceSpecEqual(base(), updated), "a real spec change must trigger update")
}
