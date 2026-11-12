package hooksgrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateGitRepoPrivateHost 钉 repo 受理位的私网/环回/链路本地拒绝
// （与 api_base 共享 grpcapi.RejectPrivateHost 单源）。
func TestValidateGitRepoPrivateHost(t *testing.T) {
	for _, u := range []string{
		"https://127.0.0.1/org/repo.git", "https://10.1.2.3/org/repo.git",
		"https://192.168.0.9/org/repo.git", "https://169.254.1.1/org/repo.git",
		"https://[::1]/org/repo.git", "https://[fe80::2]/org/repo.git",
		"https://localhost/org/repo.git",
	} {
		err := validateGitRepo(u)
		require.Error(t, err, "repo must reject %s", u)
		assert.Contains(t, err.Error(), "repo:", "rejection must name the field")
	}
	for _, u := range []string{
		"https://github.com/org/repo.git", "https://gitlab.example.com/org/repo.git",
		"https://203.0.113.7/org/repo.git",
	} {
		assert.NoError(t, validateGitRepo(u), "repo must accept %q", u)
	}
}
