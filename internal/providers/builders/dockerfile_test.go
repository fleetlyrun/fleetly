package builders

// dockerfile Provider 单测：前端属性组装（用户 build-args + 平台裁决的
// cache mount 命名空间）。solve 链是真机集成面（env-gated，build_test），
// 属性构造是纯函数面 hermetic 断言。

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func TestDockerfileFrontendAttrs(t *testing.T) {
	req := capability.BuildRequest{
		Args:   map[string]string{"VER": "1.2", "BUILDKIT_CACHE_MOUNT_NS": "forged"},
		Target: "10.124.0.3:5000/myapp_01:r7",
	}
	attrs := dockerfileFrontendAttrs(req, "deploy/Dockerfile")
	assert.Equal(t, "deploy/Dockerfile", attrs["filename"])
	assert.Equal(t, "1.2", attrs["build-arg:VER"], "user build args pass through")
	// cache mount 命名空间 = 推送目标 repo 前缀（与 railpack 轨 cache-key
	// 同粒度 per-App、跨 revision 稳定）；平台键后置覆写——用户同名
	// build-arg 不得漂移命名空间裁决。
	assert.Equal(t, "10.124.0.3:5000/myapp_01", attrs["build-arg:BUILDKIT_CACHE_MOUNT_NS"],
		"cache mount namespace must be the push target repo prefix")

	// 空 Target（入口校验前的防御路径）不设键——行为同历史。
	attrs = dockerfileFrontendAttrs(capability.BuildRequest{}, "Dockerfile")
	assert.Equal(t, "Dockerfile", attrs["filename"])
	_, ok := attrs["build-arg:BUILDKIT_CACHE_MOUNT_NS"]
	assert.False(t, ok, "empty target must not set a namespace")
}
