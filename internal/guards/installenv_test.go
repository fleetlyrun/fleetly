package guards

// H12（R09-H1）静态执法：install.sh 必须把 Provider 工厂依赖的受管面 env
// 物化进 systemd unit 与无 systemd 的 env 文件——只写注释或漏掉某一面，
// README 一行安装后的控制面就会静默停用对应能力（Proxy 面缺失 = Route/
// Console/API 发布全链失效，用户侧无错误）。
//
// 双向耦合：Proxy 端点 env 名同时钉在 install.sh 与 traefik 工厂读取点——
// 任一侧改名即红。registry/logging/metrics 三个既有物化 env 做存在性断言
// 防回归。文件缺失/零命中一律致命失败（守卫不可盲）。

import (
	"strings"
	"testing"
)

// materializedEnv 是一面受管面 env 的物化形态三元组：
// systemd unit 行、env 文件行、来源 shell 变量。
type materializedEnv struct {
	name string
	key  string // 来源 shell 变量名（物化行由它展开）
	// readerFile 非空时额外断言该文件仍以同名 env 作读取面（双向耦合）。
	readerFile string
}

func TestInstallMaterializesManagedProviderEnv(t *testing.T) {
	install := readFileLF(t, "install.sh")
	if len(install) == 0 {
		t.Fatal("install.sh is empty — the guard is blind")
	}

	for _, env := range []materializedEnv{
		// registry 物化面（既有）：systemd + env 文件双路。
		{name: "FLEETLY_REGISTRY_ADDR", key: "REGISTRY_ADDR"},
		// logging/metrics 物化面（既有，ADR-0040/0041）。
		{name: "FLEETLY_LOGGING_ADDR", key: "LOGGING_ADDR"},
		{name: "FLEETLY_METRICS_ADDR", key: "METRICS_ADDR"},
		// Proxy 拉取端点（H12 新增）：traefik 工厂的唯一注入面。
		{
			name:       "FLEETLY_PROXY_CONFIG_ENDPOINT",
			key:        "PROXY_CONFIG_ENDPOINT",
			readerFile: "internal/infrastructure/providers/traefik/provider.go",
		},
	} {
		// systemd 分支：Environment=<ENV>=$VAR（unit 文件即 materialization）。
		systemdLine := "Environment=" + env.name + "=$" + env.key
		if !strings.Contains(install, systemdLine) {
			t.Errorf("install.sh no longer materializes %s in the systemd unit (missing %q) — the managed capability stays silently disabled after a README install", env.name, systemdLine)
		}
		// 无 systemd 分支：env 文件行 <ENV>=${VAR:-}（崩溃恢复重建进程的同源面）。
		envFileLine := env.name + "=${" + env.key + ":-}"
		if !strings.Contains(install, envFileLine) {
			t.Errorf("install.sh no longer materializes %s in the background env file (missing %q) — restarts would silently drop the managed capability", env.name, envFileLine)
		}
		// 来源变量派生：默认值/透传的单一事实源。
		assignment := env.key + `="${` + env.name + `:-}"`
		if !strings.Contains(install, assignment) {
			t.Errorf("install.sh no longer derives %s from env %s (missing %q)", env.key, env.name, assignment)
		}
		if env.readerFile != "" {
			reader := readFileLF(t, env.readerFile)
			readExpr := `os.Getenv("` + env.name + `")`
			if !strings.Contains(reader, readExpr) {
				t.Errorf("%s no longer reads %s via %s — the installer and the provider factory drifted apart (rename one side and the proxy face silently disappears)", env.readerFile, env.name, readExpr)
			}
		}
	}

	// Proxy 端点缺省形态：<advertise>:9082 的 gateway proxy 拉取地址——派生
	// 公式漂移（端口/路径）同样是静默停用。
	if !strings.Contains(install, `"http://$ADDR:9082/proxy/config"`) {
		t.Errorf("install.sh no longer derives the default proxy config endpoint as http://$ADDR:9082/proxy/config — the installed control plane would run without a managed proxy")
	}
}
