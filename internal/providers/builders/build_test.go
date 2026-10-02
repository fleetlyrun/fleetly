package builders

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// TestBuildAgainstLocalDaemon 是真机集成测试（env-gated：FLEETLY_TEST_DOCKER=1
// 且 FLEETLY_TEST_REGISTRY_ADDR 指向 daemon 已信任（--insecure-registry）的
// 本地仓库时执行；推送面是 F1.11 起的硬路径——docker push 需要 daemon
// 信任目标，普通单测跳过。不起本机 swarm 状态。仓库准备示例：
//
//	docker run -d -p 5000:5000 --name test-reg registry:2
//	# 并把 <host>:5000 加入 daemon.json 的 insecure-registries 后重启。
func TestBuildAgainstLocalDaemon(t *testing.T) {
	if os.Getenv("FLEETLY_TEST_DOCKER") == "" {
		t.Skip("set FLEETLY_TEST_DOCKER=1 to run the local-daemon build integration test")
	}
	regAddr := os.Getenv("FLEETLY_TEST_REGISTRY_ADDR")
	if regAddr == "" {
		t.Skip("set FLEETLY_TEST_REGISTRY_ADDR to an insecure-registry-trusted local registry to exercise the push path")
	}
	ctx := context.Background()
	p, err := NewDockerfile(ctx, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	require.True(t, p.Health(ctx).Healthy, "daemon buildkit must be reachable")

	dir := t.TempDir()
	dockerfile := "FROM busybox:1.37\nRUN echo fleetly-build-smoke\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600))

	var lines []string
	target := regAddr + "/build-smoke:test"
	res, err := p.Build(ctx, capability.BuildRequest{
		BuildID:    "build-smoke-1",
		Builder:    "dockerfile",
		ContextDir: dir,
		Target:     target,
	}, &logCollector{&lines})
	require.NoError(t, err)
	// digest 是推送产物的 manifest digest（sha256: 形态；非本机镜像 ID）。
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, res.Digest, "push must return the registry manifest digest")
	assert.NotEmpty(t, lines, "build log frames must stream (F0.9)")
}
