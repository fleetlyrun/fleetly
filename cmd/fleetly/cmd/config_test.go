package cmd

// 本地配置与解析矩阵的表驱动测试（v0.3 W1-S4 验收项）：优先级矩阵
//（flag > env > config，token/team/project 三轴）与 config.yaml 的读写
// 生命周期（roundtrip / 缺文件 / 损坏 fail-loud / 全空删文件）。全部经
// configPathOverride 注入临时目录——测试进程绝不触碰真实用户主目录。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// useTempConfig 把配置路径覆盖到本测试专属的临时路径并注册恢复，返回该
// 路径（saveCLIConfig/loadCLIConfig/clearCLIConfig 与解析矩阵随后都落在
// 它上面）。
func useTempConfig(t *testing.T) string {
	t.Helper()
	saved := configPathOverride
	configPathOverride = filepath.Join(t.TempDir(), "home", ".fleetly", "config.yaml")
	t.Cleanup(func() { configPathOverride = saved })
	return configPathOverride
}

// TestResolveTokenPriorityMatrix 验收：token 解析矩阵 flag > env > config
// 的全序覆盖。空 env 值 = 未设置（与既有 env 回落纪律一致）；config 是
// 缺省尾环（auth login 落盘凭据对全部远程动词生效，env 高于 config 保证
// 存量 CI 形态零行为变化）。
func TestResolveTokenPriorityMatrix(t *testing.T) {
	cases := []struct {
		name        string
		flagToken   string
		envToken    string
		configToken string
		wantToken   string
		wantSource  string
	}{
		{"flag beats env and config", "flt_flag", "flt_env", "flt_config", "flt_flag", sourceFlag},
		{"env beats config", "", "flt_env", "flt_config", "flt_env", sourceEnv},
		{"config is the last ring", "", "", "flt_config", "flt_config", sourceConfig},
		{"empty everywhere resolves to nothing", "", "", "", "", ""},
		{"empty env value counts as unset", "", "", "flt_config", "flt_config", sourceConfig},
		{"empty config field counts as unset", "", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			if tc.configToken != "" {
				if err := saveCLIConfig(cliConfig{Token: tc.configToken}); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}
			t.Setenv("FLEETLY_TOKEN", tc.envToken)
			got, source, err := resolveToken(tc.flagToken)
			if err != nil {
				t.Fatalf("resolveToken: %v", err)
			}
			if got != tc.wantToken || source != tc.wantSource {
				t.Fatalf("resolveToken = (%q, %q), want (%q, %q)", got, source, tc.wantToken, tc.wantSource)
			}
		})
	}
}

// TestResolveContextPriorityMatrix 验收：team/project 上下文解析矩阵——
// 同一读序，两轴独立回落（--team 显式覆盖不影响 project 的解析）。
func TestResolveContextPriorityMatrix(t *testing.T) {
	cases := []struct {
		name        string
		teamFlag    string
		projectFlag string
		teamEnv     string
		projectEnv  string
		config      cliConfig
		wantTeam    string
		wantTeamSrc string
		wantProject string
		wantProjSrc string
	}{
		{
			name:     "flags beat env and config on both axes",
			teamFlag: "flagteam", projectFlag: "flagprj",
			teamEnv: "envteam", projectEnv: "envprj",
			config:   cliConfig{CurrentTeam: "cfgteam", CurrentProject: "cfgprj"},
			wantTeam: "flagteam", wantTeamSrc: sourceFlag,
			wantProject: "flagprj", wantProjSrc: sourceFlag,
		},
		{
			name:    "env beats config, flag absent",
			teamEnv: "envteam", projectEnv: "envprj",
			config:   cliConfig{CurrentTeam: "cfgteam", CurrentProject: "cfgprj"},
			wantTeam: "envteam", wantTeamSrc: sourceEnv,
			wantProject: "envprj", wantProjSrc: sourceEnv,
		},
		{
			name:     "config is the last ring",
			config:   cliConfig{CurrentTeam: "cfgteam", CurrentProject: "cfgprj"},
			wantTeam: "cfgteam", wantTeamSrc: sourceConfig,
			wantProject: "cfgprj", wantProjSrc: sourceConfig,
		},
		{
			name:     "axes resolve independently (flag team does not pin project)",
			teamFlag: "flagteam",
			config:   cliConfig{CurrentProject: "cfgprj"},
			wantTeam: "flagteam", wantTeamSrc: sourceFlag,
			wantProject: "cfgprj", wantProjSrc: sourceConfig,
		},
		{
			name:     "nothing set resolves to empty context",
			wantTeam: "", wantTeamSrc: "",
			wantProject: "", wantProjSrc: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempConfig(t)
			t.Setenv("FLEETLY_TEAM", tc.teamEnv)
			t.Setenv("FLEETLY_PROJECT", tc.projectEnv)
			if tc.config != (cliConfig{}) {
				if err := saveCLIConfig(tc.config); err != nil {
					t.Fatalf("seed config: %v", err)
				}
			}
			got, err := resolveContext(tc.teamFlag, tc.projectFlag)
			if err != nil {
				t.Fatalf("resolveContext: %v", err)
			}
			if got.Team != tc.wantTeam || got.TeamSource != tc.wantTeamSrc ||
				got.Project != tc.wantProject || got.ProjectSource != tc.wantProjSrc {
				t.Fatalf("resolveContext = %+v, want team=(%q,%s) project=(%q,%s)",
					got, tc.wantTeam, tc.wantTeamSrc, tc.wantProject, tc.wantProjSrc)
			}
		})
	}
}

// TestCLIConfigLifecycle config.yaml 读写生命周期：缺文件 = 零值非错误
// （首登态）、roundtrip 保形、损坏 fail-loud（含可行动指引）、全空删文件
// （logout 回 pristine）、clear 幂等。
func TestCLIConfigLifecycle(t *testing.T) {
	t.Run("missing file is a zero-value config, not an error", func(t *testing.T) {
		useTempConfig(t)
		c, err := loadCLIConfig()
		if err != nil || c != (cliConfig{}) {
			t.Fatalf("load missing config = (%+v, %v), want zero/nil", c, err)
		}
	})

	t.Run("save then load roundtrip", func(t *testing.T) {
		useTempConfig(t)
		want := cliConfig{Token: "flt_abc", CurrentTeam: "acme", CurrentProject: "web"}
		if err := saveCLIConfig(want); err != nil {
			t.Fatalf("save: %v", err)
		}
		got, err := loadCLIConfig()
		if err != nil || got != want {
			t.Fatalf("roundtrip = (%+v, %v), want %+v", got, err, want)
		}
	})

	t.Run("stored file denies group and world access", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			// Windows 不承载 POSIX 位（os.Stat 恒报 0666，权限由 ACL 管辖）
			// ——0600 请求的强制面只在 POSIX 上可断言。
			t.Skip("POSIX permission bits are not enforced on windows")
		}
		path := useTempConfig(t)
		if err := saveCLIConfig(cliConfig{Token: "flt_abc"}); err != nil {
			t.Fatalf("save: %v", err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("config file mode = %o, want no group/world bits", fi.Mode().Perm())
		}
	})

	t.Run("malformed yaml fails loudly with actionable guidance", func(t *testing.T) {
		path := useTempConfig(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte("token: [unclosed"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		_, err := loadCLIConfig()
		if err == nil {
			t.Fatal("malformed config must fail, not silently read as empty")
		}
		for _, want := range []string{path, "fleetly auth logout"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error text missing %q: %v", want, err)
			}
		}
	})

	t.Run("saving an empty config removes the file", func(t *testing.T) {
		path := useTempConfig(t)
		if err := saveCLIConfig(cliConfig{Token: "flt_abc"}); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := saveCLIConfig(cliConfig{}); err != nil {
			t.Fatalf("save empty: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("config file must be gone after clearing, stat err = %v", err)
		}
	})

	t.Run("clear is idempotent on a missing file", func(t *testing.T) {
		useTempConfig(t)
		if err := clearCLIConfig(); err != nil {
			t.Fatalf("clear on missing file: %v", err)
		}
	})

	t.Run("partial config keeps remaining keys on clear", func(t *testing.T) {
		// 他票未来加字段时，logout 只清本票三键：模拟一个未知键残留的文件，
		// clear（= save 零值）应删文件（本票视野内零值 = 全清）。这里钉住
		// 现状契约：clear 后 load 为零值。
		useTempConfig(t)
		if err := saveCLIConfig(cliConfig{Token: "flt_abc", CurrentTeam: "acme"}); err != nil {
			t.Fatalf("save: %v", err)
		}
		if err := clearCLIConfig(); err != nil {
			t.Fatalf("clear: %v", err)
		}
		c, err := loadCLIConfig()
		if err != nil || c != (cliConfig{}) {
			t.Fatalf("after clear = (%+v, %v), want zero/nil", c, err)
		}
	})
}
