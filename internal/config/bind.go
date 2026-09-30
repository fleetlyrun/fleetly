// Package config 承载 fleetlyd 配置：config.proto 生成结构为单一事实源，
// lynx ConfigSource（YAML + FLEETLY_ 前缀环境变量）解码进 AppConfig。
package config

import (
	"strings"

	"github.com/lynx-go/lynx"
	"github.com/spf13/pflag"
)

// EnvPrefix 是环境变量覆盖配置键的前缀（"data.root" → "FLEETLY_DATA_ROOT"）。
const EnvPrefix = "FLEETLY"

// ConfigureConfigSource 绑定默认 flags（config/config-type/config-dir/log-level）
// 并启用 FLEETLY_* 环境变量覆盖：点号/连字符键统一替换为下划线。
func ConfigureConfigSource(f *pflag.FlagSet, c lynx.ConfigSource) error {
	if err := lynx.DefaultBindConfigFunc(f, c); err != nil {
		return err
	}
	c.SetEnvPrefix(EnvPrefix)
	c.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	c.AutomaticEnv()
	return nil
}

// NewBindConfigFunc 返回 lynx.WithBindConfigFunc 注入物。
func NewBindConfigFunc() lynx.BindConfigFunc {
	return func(f *pflag.FlagSet, c lynx.ConfigSource) error {
		return ConfigureConfigSource(f, c)
	}
}

// UnmarshalConfig 把 lynx.Config 解码到 AppConfig 并应用缺省（WithDefaults）。
// proto 生成结构带 json tag（snake_case 键），lynx Unmarshal 按
// mapstructure → json → 小写字段名回退链逐叶取值。
func UnmarshalConfig(c lynx.Config, out *AppConfig) error {
	if err := c.Unmarshal(out); err != nil {
		return err
	}
	WithDefaults(out)
	return nil
}
