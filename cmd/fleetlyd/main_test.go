package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/lynx-go/commands"
	"github.com/stretchr/testify/assert"

	"github.com/lynx-go/lynx/buildinfo"
)

// TestHasVerbArgs（staging 实证 2026-10-03 的防线形态）：非旗标首参进
// 动词面，旗标形态与空参照常放行给 lynx runner（daemon 引导路径）。
func TestHasVerbArgs(t *testing.T) {
	assert.False(t, hasVerbArgs(nil))
	assert.False(t, hasVerbArgs([]string{"--config-dir", "/etc/fleetly"}))
	assert.False(t, hasVerbArgs([]string{"-c", "/etc/fleetly"}))
	assert.False(t, hasVerbArgs([]string{"--log-level", "debug"}))
	assert.False(t, hasVerbArgs([]string{"--"}))

	assert.True(t, hasVerbArgs([]string{"admin", "rewrap", "--data-root", "/var/lib/fleetly"}))
	assert.True(t, hasVerbArgs([]string{"relay", "--manager", "http://10.0.0.3:9081"}))
	assert.True(t, hasVerbArgs([]string{"help"}))
	// 杂散路径形态：一律进动词面（由分发器拒绝），不得落回 runner。
	for _, stray := range []string{"serve", "/etc/fleetly"} {
		assert.True(t, hasVerbArgs([]string{stray}), "stray positional %q must go through the verb dispatcher", stray)
	}
}

// TestVersionFlagArgs（staging 2026-10-11 二次实证的流浪 daemon 旗标半边）：
// 全数组扫描（`--` 终止符后停）——--version 不在任何动词面的旗标集，全局
// 拦截无占有冲突；main 在 runner 之前打印退 0，不触配置/数据根。
func TestVersionFlagArgs(t *testing.T) {
	assert.True(t, versionFlagArgs([]string{"--version"}))
	assert.True(t, versionFlagArgs([]string{"-version"}))
	assert.True(t, versionFlagArgs([]string{"--config-dir", "/etc/fleetly", "--version"}),
		"旗标值与动词位置参数无法在不识旗标语义下区分——全数组扫描兜住 daemon 命令行尾随 --version 形态")
	assert.True(t, versionFlagArgs([]string{"admin", "rewrap", "--version"}),
		"动词面无 --version 旗标（admin 拒它为 unknown flag）——拦截优先，探查语义不破坏动词")

	assert.False(t, versionFlagArgs(nil))
	assert.False(t, versionFlagArgs([]string{"--"}))
	assert.False(t, versionFlagArgs([]string{"--config-dir", "/etc/fleetly"}))
	// `--` 终止符后全是位置参数：字面 "--version" 的位置参数不拦。
	assert.False(t, versionFlagArgs([]string{"--", "--version"}))
	assert.False(t, versionFlagArgs([]string{"admin", "rewrap", "--", "--version"}))
}

// TestVerbAppRun 钉动词面的分发契约：未知动词退 2 并附 help（流浪 daemon
// 防线——本面只分发，绝不引导 daemon）、用法错退 2、help 面退 0。只走
// 校验级路径，不触真实数据根或 Provider。
func TestVerbAppRun(t *testing.T) {
	app := newVerbApp(buildinfo.BuildInfo{Version: "test-ver", Commit: "abc123", Date: "20261012"})
	ctx := context.Background()

	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		env := &commands.Environment{Stdout: &out, Stderr: &errOut}
		code := app.Run(ctx, env, args)
		return code, out.String(), errOut.String()
	}

	t.Run("version verb prints triple", func(t *testing.T) {
		code, out, _ := run("version")
		assert.Equal(t, commands.ExitOK, code)
		assert.Equal(t, "fleetlyd test-ver (commit abc123, built 20261012)\n", out)
	})

	t.Run("version verb rejects positionals", func(t *testing.T) {
		code, _, errOut := run("version", "stray")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, `unexpected argument "stray"`)
	})

	t.Run("unknown verb rejected with help", func(t *testing.T) {
		code, _, errOut := run("serve")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, `unknown verb "serve"`)
		assert.Contains(t, errOut, "admin") // help 屏列出动词面
		assert.Contains(t, errOut, "relay")
		assert.Contains(t, errOut, "version")
	})

	t.Run("stray path rejected", func(t *testing.T) {
		code, _, errOut := run("/etc/fleetly")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, `unknown verb "/etc/fleetly"`)
	})

	t.Run("help screen", func(t *testing.T) {
		code, out, _ := run("help")
		assert.Equal(t, commands.ExitOK, code)
		assert.Contains(t, out, "fleetlyd")
		assert.Contains(t, out, "admin")
		assert.Contains(t, out, "relay")
		assert.Contains(t, out, "test") // footer 版本串
	})

	t.Run("bare admin lists subcommands", func(t *testing.T) {
		code, out, _ := run("admin")
		assert.Equal(t, commands.ExitOK, code)
		assert.Contains(t, out, "rewrap")
	})

	t.Run("admin unknown subcommand", func(t *testing.T) {
		code, _, errOut := run("admin", "bogus")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, `unknown verb "bogus"`)
	})

	t.Run("rewrap rejects positionals", func(t *testing.T) {
		code, _, errOut := run("admin", "rewrap", "stray")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, `unexpected argument "stray"`)
		assert.Contains(t, errOut, "usage:")
	})

	t.Run("rewrap rejects unknown flags", func(t *testing.T) {
		code, _, errOut := run("admin", "rewrap", "--bogus")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, "bad arguments")
	})

	t.Run("rewrap help shows flags", func(t *testing.T) {
		code, out, _ := run("admin", "rewrap", "-h")
		assert.Equal(t, commands.ExitOK, code)
		assert.Contains(t, out, "usage:")
		assert.Contains(t, out, "-data-root")
		assert.Contains(t, out, "-execute")
		assert.Contains(t, out, "-json")
	})

	t.Run("relay missing required flags", func(t *testing.T) {
		code, _, errOut := run("relay")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, "--manager and --relay-credential are required")
		assert.Contains(t, errOut, "usage:")
	})

	t.Run("relay half flags still rejected", func(t *testing.T) {
		code, _, errOut := run("relay", "--manager", "http://10.0.0.3:9081")
		assert.Equal(t, commands.ExitUsage, code)
		assert.Contains(t, errOut, "--manager and --relay-credential are required")
	})

	t.Run("relay help shows flags", func(t *testing.T) {
		code, out, _ := run("help", "relay")
		assert.Equal(t, commands.ExitOK, code)
		assert.Contains(t, out, "-manager")
		assert.Contains(t, out, "-relay-credential")
		assert.Contains(t, out, "-runtime")
		assert.NotContains(t, out, "-join-token")
	})
}
