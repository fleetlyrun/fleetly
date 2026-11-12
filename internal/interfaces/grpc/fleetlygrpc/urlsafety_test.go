package fleetlygrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivateHostRejection 钉 api_base 受理位的私网/环回/链路本地拒绝
// （repo 面同源断言随 hooksgrpc 拆分迁至该包单测）。
func TestPrivateHostRejection(t *testing.T) {
	for _, u := range []string{
		"https://127.0.0.1", "https://10.0.0.5/api/v3", "https://172.16.1.9",
		"https://192.168.1.1", "https://169.254.169.254", "https://0.0.0.0",
		"https://[::1]", "https://[fe80::1]", "https://[fc00::1]",
		"https://[::ffff:10.0.0.5]", "https://localhost", "https://git.localhost",
		"https://2130706433", "https://0177.0.0.1",
	} {
		err := validateAPIBase(u)
		require.Error(t, err, "api_base must reject %s", u)
		assert.Contains(t, err.Error(), "api_base:", "rejection must name the field")
	}
	for _, u := range []string{
		"", "https://github.example.com/api/v3", "https://ghes.corp.example.com",
		"https://203.0.113.7/api/v3", "https://[2001:db8::1]/api/v3",
		"https://8.8.8.8", "https://api.github.com",
	} {
		assert.NoError(t, validateAPIBase(u), "api_base must accept %q", u)
	}
}
