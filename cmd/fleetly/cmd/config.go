package cmd

// fleetly CLI 的本地配置（v0.3 W1-S4，rbac-teams 设计 §2.4）：此前 CLI 的
// token 只来自 --token flag 与 FLEETLY_TOKEN env（无任何落盘形态），本票
// 引入 `~/.fleetly/config.yaml` 持久化——auth login 验证通过后把 PAT 写进
// 此文件，并承载当前 team/project 上下文（current_team/current_project；
// 切换子命令 team use / project use 随 W2 团队/项目服务落地）。
//
// 读取优先级（全仓统一口径，resolveCredential/resolveContext 单点裁决）：
//
//	flag（--token/--team/--project）> env（FLEETLY_TOKEN/FLEETLY_TEAM/
//	FLEETLY_PROJECT）> config（本文件）。
//
// env 高于 config：既有 CI/脚本形态（export FLEETLY_TOKEN）零行为变化
//（读序只在「env 缺失」时新增 config 回落——存量命令零破坏的边界）。
//
// 文件本身是凭据载体：目录 0700、文件 0600（Windows 上权限位尽力而为，
// 语义与 state 层密钥文件同口径）；空值字段不落盘（写出前 omitempty 归零
// ——无残留空键），全部字段为空时删文件（logout 后回到 pristine 态，
// 不留空壳配置）。

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// cliConfig 是 ~/.fleetly/config.yaml 的映射（yaml 键 = snake_case，与
// 设计 §2.4 的 current_team/current_project 命名一致）。
type cliConfig struct {
	// Token 是 auth login 落盘的 PAT（明文——文件即凭据边界，见上）。
	Token string `yaml:"token,omitempty"`
	// CurrentTeam 是当前团队上下文（team slug；资源命令按上下文过滤是 W2）。
	CurrentTeam string `yaml:"current_team,omitempty"`
	// CurrentProject 是当前项目上下文（project slug；同上）。
	CurrentProject string `yaml:"current_project,omitempty"`
}

// empty 报告配置是否全空（空 = 文件不该存在——save 时删文件）。
func (c cliConfig) empty() bool {
	return c.Token == "" && c.CurrentTeam == "" && c.CurrentProject == ""
}

// configPathOverride 是测试注入点（与 extraDialOptions 同款纪律：生产恒
// 空，测试指到临时目录——测试进程绝不读写真实用户主目录）。空值回落
// fleetlyDefaultConfigPath。
var configPathOverride string

// fleetlyDefaultConfigPath 返回缺省配置路径 ~/.fleetly/config.yaml（home
// 目录解析失败返回可行动错误——不猜当前工作目录）。
func fleetlyDefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve the user home directory: %w (set HOME/USERPROFILE)", err)
	}
	return filepath.Join(home, ".fleetly", "config.yaml"), nil
}

// fleetlyConfigPath 返回生效配置路径（测试覆盖点优先）。
func fleetlyConfigPath() (string, error) {
	if configPathOverride != "" {
		return configPathOverride, nil
	}
	return fleetlyDefaultConfigPath()
}

// loadCLIConfig 读本地配置：文件不存在 = 零值配置（首登态，不是错误）；
// 文件存在但解析失败 = 可行动错误（fail-loud——静默当空配置会让「登录态
// 丢失」伪装成「未登录」，指引会误导用户重新 login 覆盖排障线索）。
func loadCLIConfig() (cliConfig, error) {
	path, err := fleetlyConfigPath()
	if err != nil {
		return cliConfig{}, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304：路径为配置点（用户主目录或测试注入），非外部输入
	if os.IsNotExist(err) {
		return cliConfig{}, nil
	}
	if err != nil {
		return cliConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c cliConfig
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return cliConfig{}, fmt.Errorf("parse %s: %w (fix or remove the file, or run 'fleetly auth logout')", path, err)
	}
	return c, nil
}

// saveCLIConfig 写本地配置（原子性以「写临时名后改名」保证——进程中断不
// 留半截 YAML；同目录改名保证同盘原子）。全空配置删文件（见 empty 注释）。
func saveCLIConfig(c cliConfig) error {
	path, err := fleetlyConfigPath()
	if err != nil {
		return err
	}
	if c.empty() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { //nolint:gosec // G304：同上，配置点路径；G306：0600 凭据文件显式收紧
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// clearCLIConfig 清空本地凭据与上下文（auth logout 的落盘面）：等价于
// save 零值——全空删文件，部分残留（他票未来加字段）时重写剩余键。
func clearCLIConfig() error {
	return saveCLIConfig(cliConfig{})
}
