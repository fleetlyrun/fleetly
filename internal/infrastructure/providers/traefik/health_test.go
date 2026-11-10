package traefik

// Provider.Health 只报配置发布就绪面（H7）：首次成功发布前拉取端点 503
// （traefik 保留 last-known-good），发布后健康。受管 Workload 自身存活面
// 归引擎 "managed proxy" 健康行（单源），本方法不重复声明。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthReflectsConfigReadiness(t *testing.T) {
	p, err := New("http://fleetlyd:9082/proxy/config", "", "", "")
	require.NoError(t, err)

	report := p.Health(context.Background())
	assert.False(t, report.Healthy, "no config snapshot published yet: the pull endpoint is not ready")
	assert.Equal(t, "no config snapshot published yet", report.Details)

	require.NoError(t, p.PublishRoutes(context.Background(), nil))
	report = p.Health(context.Background())
	assert.True(t, report.Healthy, "a published config snapshot makes the config face ready")
	assert.Equal(t, "config snapshot published", report.Details)
}
