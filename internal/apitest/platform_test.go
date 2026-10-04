package apitest

// PlatformService API 面（F2.3，ADR-0039 决策 10）：手动触发与列举的
// 契约断言。成功链路由 engine 假 restic 面单测与 dind-upgrade e2e（真
// restic）承载——本面钉：未配置/缺席的精确失败（升级脚本的硬停分支）、
// 授权 scope（platform:write/read）、幂等执法面挂载。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// 夹具引擎不带 PlatformBackup 配置：触发=精确失败 E_PLATFORM_BACKUP_FAILED
// （升级序的硬停分支——ADR-0015 备份前置不得被降级成内部错误）。
func TestPlatformBackupTriggerNotConfigured(t *testing.T) {
	h := New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	platform := systemv1.NewPlatformServiceClient(h.Conn)

	_, err := platform.TriggerPlatformBackup(ctx, &systemv1.TriggerPlatformBackupRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_PLATFORM_BACKUP_FAILED")
	assert.Contains(t, err.Error(), "not configured")
}

// 凭证执法：无 token 调用被拒（与 SystemService 公开诊断面分立——本面
// 全部要凭证）。
func TestPlatformBackupRequiresCredentials(t *testing.T) {
	h := New(t)
	platform := systemv1.NewPlatformServiceClient(h.Conn)

	_, err := platform.TriggerPlatformBackup(context.Background(), &systemv1.TriggerPlatformBackupRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
