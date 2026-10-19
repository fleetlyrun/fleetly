package fleetlygrpc

// Platform Backup 错误映射单测（H6）：引擎级单飞冲突（ErrPlatformBackupInProgress）
// 是 409 族状态冲突，不是备份失败——升级脚本不得因"已有备份在途"硬停；
// 其余引擎错误维持 E_PLATFORM_BACKUP_FAILED。

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

func TestPlatformBackupErrMapping(t *testing.T) {
	t.Run("in-progress sentinel maps to E_CONFLICT", func(t *testing.T) {
		err := platformBackupErr(fmt.Errorf("trigger: %w", engine.ErrPlatformBackupInProgress))
		var appErr *apperr.Error
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, "E_CONFLICT", appErr.Code())
		assert.Equal(t, "platform backup already in progress", appErr.Message())
		assert.True(t, errors.Is(err, engine.ErrPlatformBackupInProgress), "the cause chain stays inspectable")
	})

	t.Run("other failures keep E_PLATFORM_BACKUP_FAILED", func(t *testing.T) {
		cause := errors.New("restic check failed")
		err := platformBackupErr(cause)
		var appErr *apperr.Error
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, "E_PLATFORM_BACKUP_FAILED", appErr.Code())
		assert.Contains(t, appErr.Message(), "restic check failed")
		assert.ErrorIs(t, err, cause)
	})
}
