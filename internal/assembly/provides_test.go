package assembly

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/config"
)

// TestNewEngineOverlapPolicyFailsFast（ADR-0017 附录 A.4）：无效重叠策略
// 在触碰任何引擎依赖之前拒绝（fail-fast——配置错误不得静默回退 skip，
// 也不得拖到首个到期拍才暴露）。依赖参数保持 nil：校验先行，不物化引擎。
func TestNewEngineOverlapPolicyFailsFast(t *testing.T) {
	cfg := config.WithDefaults(&config.AppConfig{})
	cfg.Engine = &config.Engine{ScheduleOverlapPolicy: "queue"}
	_, err := NewEngine(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schedule_overlap_policy")
	assert.Contains(t, err.Error(), "queue")
}

// TestObjectStoreSelection（ADR-0042 决策 2 装配选择锚）：platform_backup.s3
// 五元组缺席 → local、不注入配置（升级零扰动）；在场 → s3 且五元组经装配
// ctx 完整注入（含 endpoint/bucket/凭证——config 是唯一契约源）。选择谓词
// 与告警解除谓词同源（PlatformBackupS3 访问器单真源）。
func TestObjectStoreSelection(t *testing.T) {
	name, ctx := objectStoreSelection(config.WithDefaults(&config.AppConfig{}))
	assert.Equal(t, "local", name)
	assert.Nil(t, capability.ObjectStoreS3FromContext(ctx), "absent five-tuple must not inject the s3 config")

	cfg := config.WithDefaults(&config.AppConfig{})
	cfg.PlatformBackup = &config.PlatformBackup{S3: &config.PlatformBackupS3{
		Endpoint: "minio.example.com:9000", Bucket: "fleetly-backups",
		AccessKeyId: "ak", SecretAccessKey: "sk",
	}}
	name, ctx = objectStoreSelection(cfg)
	assert.Equal(t, "s3", name)
	got := capability.ObjectStoreS3FromContext(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "minio.example.com:9000", got.Endpoint)
	assert.Equal(t, "fleetly-backups", got.Bucket)
	assert.Equal(t, "ak", got.AccessKeyID)
	assert.Equal(t, "sk", got.SecretAccessKey)

	// 半配置（endpoint 或 bucket 空）= 访问器判缺席 = local（与 restic 仓
	// 的 nil 语义同一访问器，不出现"配了半截切 s3"形态）。
	cfg.PlatformBackup.S3.Endpoint = ""
	name, ctx = objectStoreSelection(cfg)
	assert.Equal(t, "local", name)
	assert.Nil(t, capability.ObjectStoreS3FromContext(ctx))
}
