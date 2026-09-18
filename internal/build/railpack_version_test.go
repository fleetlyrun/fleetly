package build

// 钉版纪律测试（Spike A E1）：railpack 版本常量与 go.mod require 行互钉——
// 单独升级任一侧（漂移、误升级、依赖传递升级）即测试失败。plan 可复现的
// 前提是版本由平台注入且永不漂移。

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestRailpackVersionPinnedMatchesGoMod 钉版常量必须与 go.mod 的 require
// 行一致（railpack v0.39.0，Spike A 验证版本）。
func TestRailpackVersionPinnedMatchesGoMod(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod") //nolint:gosec // 仓内固定相对路径
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	const needle = "github.com/railwayapp/railpack "
	var found string
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, needle); i >= 0 {
			found = strings.TrimSpace(line[i:])
			break
		}
	}
	if found == "" {
		t.Fatalf("go.mod has no railpack require line（依赖被移除？构建层无法交付）")
	}
	want := needle + RailpackVersion
	if found != want {
		t.Fatalf("railpack pin drift: go.mod says %q, code const says %q（两侧必须同票升级）",
			found, want)
	}
}

// TestBuildkitImagePinned buildkitd 钉版镜像与 Spike A 验证版本一致。
func TestBuildkitImagePinned(t *testing.T) {
	if BuildkitImage != "moby/buildkit:v0.32.2" {
		t.Fatalf("buildkit image = %q, want pinned moby/buildkit:v0.32.2", BuildkitImage)
	}
	if !bytes.Contains([]byte(BuildkitImage), []byte("v0.32.2")) {
		t.Fatal("unreachable")
	}
}
