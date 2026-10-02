package assembly

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/config"
)

// TestNewEngineOverlapPolicyFailsFast（ADR-0017 附录 A.4）：无效重叠策略
// 在触碰任何引擎依赖之前拒绝（fail-fast——配置错误不得静默回退 skip，
// 也不得拖到首个到期拍才暴露）。依赖参数保持 nil：校验先行，不物化引擎。
func TestNewEngineOverlapPolicyFailsFast(t *testing.T) {
	cfg := config.WithDefaults(&config.AppConfig{})
	cfg.Engine = &config.Engine{ScheduleOverlapPolicy: "queue"}
	_, err := NewEngine(nil, nil, nil, nil, nil, nil, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schedule_overlap_policy")
	assert.Contains(t, err.Error(), "queue")
}
