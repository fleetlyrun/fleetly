package apivalidate_test

// 客户端 descriptor 校验的文案契约（P4-1 批 1）：试点族
// CreateProjectRequest.name 的 required/dns 标签双规则在服务端由 validate
// 拦截器执法、客户端经本包 Validator 预检——两面同一 descriptor 法源，
// 文案逐字节一致由本测试钉死（对齐真源见 entityid.NamePatternSource 的
// descriptor 对齐测试）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apivalidate"
)

// TestValidatePilotFamily 钉试点族三态：合法通过、空名报 required 文案、
// 坏形态报 dns label 文案（均逐字节，与服务端拦截器同构）。
func TestValidatePilotFamily(t *testing.T) {
	v, err := apivalidate.NewValidator()
	require.NoError(t, err, "protovalidate 构造常态不可达（CEL 环境配置级故障才失败）")

	assert.NoError(t, v.Validate(&structurev1.CreateProjectRequest{Name: "web"}),
		"合法 dns 标签必须通过")
	assert.NoError(t, v.Validate(&structurev1.CreateProjectRequest{Name: "nightly-backup-42"}),
		"含连字符的合法 dns 标签必须通过")

	t.Run("empty name reports required message", func(t *testing.T) {
		err := v.Validate(&structurev1.CreateProjectRequest{Name: ""})
		require.Error(t, err)
		// 未注册码不可能（注册表只增，E_INVALID_ARGUMENT 首发在册），
		// 直接以类型断言还原。
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
		assert.Equal(t, "name: must not be empty", e.Message())
	})

	t.Run("bad shape reports dns label message", func(t *testing.T) {
		err := v.Validate(&structurev1.CreateProjectRequest{Name: "Bad_Name"})
		require.Error(t, err)
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
		assert.Equal(t,
			`name: must be a dns label (lowercase letters, digits, hyphens; start and end alphanumeric, e.g. "web" or "nightly-backup")`,
			e.Message())
	})
}
