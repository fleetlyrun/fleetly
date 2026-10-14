package fleetlygrpc

// 私网拒绝防线表驱动单测（P0-7.3）：rejectPrivateHost 单源 + 两个受理位
//（validateAPIBase / validateGitRepo）的负例表——环回/私网/链路本地/
// 未指定/localhost/数字面形态全拒，公网字面与 DNS 名放行。GHES 内网部署
// 的显式豁免留待真需求（豁免落地时本表同步翻面）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 拒绝表：字段名 → 非法 host 形态（IPv4/IPv6/映射形式/数字面/localhost）。
var privateHostRejects = map[string][]string{
	"api_base": {
		"https://127.0.0.1", "https://10.0.0.5/api/v3", "https://172.16.1.9",
		"https://192.168.1.1", "https://169.254.169.254", "https://0.0.0.0",
		"https://[::1]", "https://[fe80::1]", "https://[fc00::1]",
		"https://[::ffff:10.0.0.5]", "https://localhost", "https://git.localhost",
		"https://2130706433", "https://0177.0.0.1",
	},
	"repo": {
		"https://127.0.0.1/org/repo.git", "https://10.1.2.3/org/repo.git",
		"https://192.168.0.9/org/repo.git", "https://169.254.1.1/org/repo.git",
		"https://[::1]/org/repo.git", "https://[fe80::2]/org/repo.git",
		"https://localhost/org/repo.git",
	},
}

func TestPrivateHostRejection(t *testing.T) {
	for field, urls := range privateHostRejects {
		for _, u := range urls {
			var err error
			switch field {
			case "api_base":
				err = validateAPIBase(u)
			case "repo":
				err = validateGitRepo(u)
			}
			require.Error(t, err, "%s must reject %s", field, u)
			assert.Contains(t, err.Error(), field+":", "rejection must name the field")
		}
	}

	// 放行表：公网字面 IP、常规 DNS 名、端口形态与私有段外的 IPv6。
	for _, u := range []string{
		"", "https://github.example.com/api/v3", "https://ghes.corp.example.com",
		"https://203.0.113.7/api/v3", "https://[2001:db8::1]/api/v3",
		"https://8.8.8.8", "https://api.github.com",
	} {
		assert.NoError(t, validateAPIBase(u), "api_base must accept %q", u)
	}
	for _, u := range []string{
		"https://github.com/org/repo.git", "https://gitlab.example.com/org/repo.git",
		"https://203.0.113.7/org/repo.git",
	} {
		assert.NoError(t, validateGitRepo(u), "repo must accept %q", u)
	}
}
