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

// TestInstallDataRootPermissions 是数据根权限守卫（R09-L5）：install.sh
// 必须以 0750 预建数据根并做一次幂等 chmod——旧版 `mkdir -p` 在默认
// umask 022 下落 0755，压过 daemon 侧 os.MkdirAll(0o750) 的意图，本地
// 任意用户可枚举 fleetly.db/uploads/bootstrap-token 的名字。已存在的老
// 部署（0755）由显式 chmod 幂等收敛，不依赖新建路径。
func TestInstallDataRootPermissions(t *testing.T) {
	install := readFileLF(t, "install.sh")
	if install == "" {
		t.Fatal("install.sh is empty — the guard is blind")
	}
	if !strings.Contains(install, `DATA_ROOT="${FLEETLY_DATA_ROOT:-/var/lib/fleetly}"`) {
		t.Errorf("install.sh no longer defines DATA_ROOT with the documented default — the permission assertions below would be checking the wrong path")
	}
	if !strings.Contains(install, `install -d -m 0750 "$DATA_ROOT"`) {
		t.Errorf("install.sh no longer creates the data root with install -d -m 0750 — a plain mkdir under umask 022 leaves it 0755 and defeats the daemon's 0750 intent (R09-L5)")
	}
	if !strings.Contains(install, `chmod 0750 "$DATA_ROOT"`) {
		t.Errorf("install.sh no longer runs an idempotent `chmod 0750` on the data root — pre-existing 0755 roots from older installs would keep their mode (R09-L5)")
	}
	if strings.Contains(install, `mkdir -p "$DATA_ROOT"`) {
		t.Errorf("install.sh still pre-creates the data root with `mkdir -p` — the mode is umask-dependent; use install -d -m 0750 plus the idempotent chmod (R09-L5)")
	}
}
