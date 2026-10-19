package guards

// S18b 跨平台守卫：console 生成链（console/src/api/gen.mjs）不得依赖
// node_modules/.bin 下的 POSIX 转发器。
//
// 事故锚：gen.mjs 曾以 execFileSync("node_modules/.bin/openapi-typescript")
// 调生成器——pnpm 在 .bin 放的无扩展名条目是 sh 脚本，Windows 上
// CreateProcess 不认（spawnSync ENOENT），本机只能靠「把 node_modules/.bin
// 里的转发器替换成 exe」的土办法绕过，换机/重装即复发。mise run console:gen
// 与 CI 必须一条调用三平台可跑，故把「用当前 node 直跑包内 bin/cli.js」
// 钉成可静态执法的事实。
//
// 双向保鲜：只断言「不用 .bin」，也断言「仍以 process.execPath + 包解析」
// 调用——哪天改成别的方式（例如 pnpm exec 包装）必须同批更新本守卫，而不是
// 静默退化回单平台形态。

import (
	"regexp"
	"strings"
	"testing"
)

// binShimCall 匹配「以 .bin 转发器作可执行文件」的调用点（注释里的叙述
// 不算——只执法 execFileSync/spawnSync 的首参形态）。
var binShimCall = regexp.MustCompile(`(?:execFileSync|execSync|spawnSync)\(\s*[\x60"']node_modules/\.bin/`)

func TestConsoleGenIsShellFreeAcrossPlatforms(t *testing.T) {
	gen := readFileLF(t, "console/src/api/gen.mjs")
	if len(gen) == 0 {
		t.Fatal("console/src/api/gen.mjs is empty — the guard is blind")
	}

	// 反例：node_modules/.bin 的转发器（Linux/macOS 是 sh，Windows 是 .cmd，
	// 直接 execFileSync 无扩展名条目在 Windows 上 ENOENT）。
	if binShimCall.MatchString(gen) {
		t.Errorf("console/src/api/gen.mjs must not invoke node_modules/.bin/* shims: " +
			"they are shell forwarders (spawnSync ENOENT on Windows) — resolve the package bin and run it with process.execPath")
	}

	// 正例：当前 node 直跑包内 bin 入口（三平台同一条路径，无 shell）。
	if !strings.Contains(gen, "process.execPath") {
		t.Errorf("console/src/api/gen.mjs must spawn the generator with process.execPath (the current node), not a shell/forwarder")
	}
	if !strings.Contains(gen, `require.resolve("openapi-typescript/package.json")`) {
		t.Errorf("console/src/api/gen.mjs must resolve the openapi-typescript package bin via require.resolve(...package.json) so the CLI path stays portable")
	}
}
