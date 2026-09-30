package dockerbuild

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
// 且本机 daemon 可用时执行；CI e2e job 与本机跑批设置，普通单测跳过——
// 不动本机 swarm 状态，仅用 daemon 内嵌 buildkit 构建一次）。
func TestBuildAgainstLocalDaemon(t *testing.T) {
	if os.Getenv("FLEETLY_TEST_DOCKER") == "" {
		t.Skip("set FLEETLY_TEST_DOCKER=1 to run the local-daemon build integration test")
	}
	ctx := context.Background()
	p, err := New(ctx, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	require.True(t, p.Health(ctx).Healthy, "daemon buildkit must be reachable")

	dir := t.TempDir()
	dockerfile := "FROM busybox:1.37\nRUN echo fleetly-build-smoke\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600))

	var lines []string
	res, err := p.Build(ctx, capability.BuildRequest{
		BuildID:    "build-smoke-1",
		ContextDir: dir,
		Target:     "fleetly.local/build-smoke:test",
	}, &logCollector{&lines})
	require.NoError(t, err)
	assert.NotEmpty(t, res.Digest, "build must return the local image digest")
	assert.NotEmpty(t, lines, "build log frames must stream (F0.9)")
}
