package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateFirstArg（staging 实证 2026-10-03）：`fleetlyd version` 一类
// 笔误不得静默引导流浪 daemon——非旗标首参直接拒绝；旗标形态与空参照常
// 放行给 runner。
func TestValidateFirstArg(t *testing.T) {
	assert.NoError(t, validateFirstArg(nil))
	assert.NoError(t, validateFirstArg([]string{"admin", "rewrap", "--data-root", "/var/lib/fleetly"}))
	assert.NoError(t, validateFirstArg([]string{"--config-dir", "/etc/fleetly"}))
	assert.NoError(t, validateFirstArg([]string{"-c", "/etc/fleetly"}))
	assert.NoError(t, validateFirstArg([]string{"--log-level", "debug"}))
	assert.NoError(t, validateFirstArg([]string{"--"}))

	for _, stray := range []string{"version", "serve", "help", "/etc/fleetly"} {
		assert.Error(t, validateFirstArg([]string{stray}), "stray positional %q must be rejected", stray)
	}
}
